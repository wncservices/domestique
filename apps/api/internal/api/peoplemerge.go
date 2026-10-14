package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// riderDB is the library's database, which a merge needs raw: it walks every
// table in riderTables in one transaction, which no store owns. False when the
// library is not a database (it always is outside tests).
func (s *Server) riderDB() (*sql.DB, dbx.Dialect, bool) {
	lib, ok := s.Source.(interface {
		Conn() *sql.DB
		DSN() string
	})
	if !ok {
		return nil, dbx.Dialect{}, false
	}
	d, err := dbx.For(lib.DSN())
	if err != nil {
		return nil, dbx.Dialect{}, false
	}
	return lib.Conn(), d, true
}

type mergeTableDTO struct {
	Table         string `json:"table"`
	Moved         int    `json:"moved"`
	KeptTarget    int    `json:"keptTarget"`
	DroppedSource int    `json:"droppedSource"`
	Merged        int    `json:"merged"`
}

type mergeDropDTO struct {
	Table       string `json:"table"`
	What        string `json:"what,omitempty"`
	Consequence string `json:"consequence"`
}

type mergeDTO struct {
	From   string `json:"from"`
	Into   string `json:"into"`
	DryRun bool   `json:"dryRun"`
	// Tables is what moved, or collided, per table; tables with nothing in them
	// are left out.
	Tables        []mergeTableDTO `json:"tables"`
	Moved         int             `json:"moved"`
	KeptTarget    int             `json:"keptTarget"`
	DroppedSource int             `json:"droppedSource"`
	Merged        int             `json:"merged"`
	SessionsEnded int             `json:"sessionsEnded"`
	// Dropped names the rows that cost something to lose, and Warnings is what to
	// read before confirming (two plans that overlap) or after (a follow-up step
	// that failed once the data had moved). Both are always arrays.
	Dropped  []mergeDropDTO `json:"dropped"`
	Warnings []string       `json:"warnings"`
}

// handlePeopleMergeRider moves everything one rider owns onto another, for a
// person who ended up as two riders: the old name-derived one that holds their
// data and the one their account is bound to. See MergeRider for the rules.
//
// With dryRun the same work is done and rolled back, so the page can show what
// would happen (and the admin confirm it) before anything is written. A real
// merge is admin-only like every People action, ends the sessions of both
// riders, and is logged at Warn with the two rider names and the counts: it is
// not undoable, and the audit trail is the only record of what was dropped.
func (s *Server) handlePeopleMergeRider(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	db, d, ok := s.riderDB()
	if !ok {
		s.logger().Warn("rider merge requested but this deployment's library is not a database")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot merge riders",
		})
		return
	}

	from := strings.ToLower(strings.TrimSpace(r.PathValue("rider")))
	var body struct {
		Into   string `json:"into"`
		DryRun bool   `json:"dryRun"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	into := strings.ToLower(strings.TrimSpace(body.Into))
	switch {
	case from == "" || into == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name the rider to merge and the rider to merge it into"})
		return
	case from == into:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a rider cannot be merged into itself"})
		return
	case !accounts.RiderPattern.MatchString(from) || !accounts.RiderPattern.MatchString(into):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rider must be letters, digits and . _ @ | -"})
		return
	}

	opt := MergeOptions{DryRun: body.DryRun, Now: s.now}
	if !body.DryRun {
		if s.Sessions != nil {
			opt.EndSessions = s.Sessions.DeleteRider
		}
		if s.Training != nil {
			opt.RecomputeFitness = s.Training.RecomputeFitnessSnapshots
		}
	}
	sum, err := MergeRider(r.Context(), db, d, from, into, opt)
	if errors.Is(err, ErrMergeBusy) {
		s.logger().Warn("rider merge refused: a background job holds the lock", "from", from, "into", into)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a background job is running just now; try again in a minute"})
		return
	}
	if err != nil {
		s.logger().Error("merging riders failed; nothing was changed unless the error says only the sessions were not ended",
			"from", from, "into", into, "dryRun", body.DryRun, "err", err)
		s.fail(w, err)
		return
	}

	totals := sum.Totals()
	caller := auth.FromContext(r.Context()).User
	if body.DryRun {
		s.logger().Info("rider merge previewed", "from", from, "into", into,
			"moved", totals.Moved, "keptTarget", totals.KeptTarget, "droppedSource", totals.DroppedSource, "by", caller)
	} else {
		// From here the data has moved: this is a 200 whatever else happened, with
		// the warnings telling the admin what still needs doing. Generated loops
		// held in memory for either rider describe data that has just moved.
		s.candidateStore().Forget(from)
		s.candidateStore().Forget(into)
		s.logger().Warn("riders merged", "from", from, "into", into,
			"moved", totals.Moved, "keptTarget", totals.KeptTarget, "droppedSource", totals.DroppedSource, "merged", totals.Merged,
			"sessionsEnded", sum.SessionsEnded, "warnings", len(sum.Warnings), "by", caller)
	}

	out := mergeDTO{
		From: from, Into: into, DryRun: body.DryRun, Tables: []mergeTableDTO{},
		Moved: totals.Moved, KeptTarget: totals.KeptTarget, DroppedSource: totals.DroppedSource, Merged: totals.Merged,
		SessionsEnded: sum.SessionsEnded, Dropped: []mergeDropDTO{}, Warnings: []string{},
	}
	out.Warnings = append(out.Warnings, sum.Warnings...)
	for _, dr := range sum.Dropped {
		out.Dropped = append(out.Dropped, mergeDropDTO(dr))
	}
	for _, table := range sum.TableNames() {
		c := sum.Tables[table]
		out.Tables = append(out.Tables, mergeTableDTO{Table: table, Moved: c.Moved, KeptTarget: c.KeptTarget, DroppedSource: c.DroppedSource, Merged: c.Merged})
	}
	writeJSON(w, http.StatusOK, out)
}
