package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/basemap"
)

type basemapUpdateDTO struct {
	// Available is whether this deployment can trigger an update at all —
	// false on a laptop, or any deployment without the tiles component and
	// its RBAC wired up (basemapUpdate.enabled in domestique-chart). False
	// hides the form rather than offering a 403, same idiom as
	// garminConsumerDTO.CanManage.
	Available bool `json:"available"`
	// CanManage is whether *this caller* may trigger an update — admin.
	CanManage   bool   `json:"canManage"`
	Unavailable string `json:"unavailable,omitempty"`

	HasRun bool   `json:"hasRun"`
	Status string `json:"status,omitempty"`
	// Progress is the download's own percentage, parsed from the extract
	// container's log — nil while nothing parseable has been printed yet,
	// never present once the update has finished (Status moves on to
	// succeeded/failed instead).
	Progress    *int    `json:"progress,omitempty"`
	West        float64 `json:"west,omitempty"`
	South       float64 `json:"south,omitempty"`
	East        float64 `json:"east,omitempty"`
	North       float64 `json:"north,omitempty"`
	MaxZoom     int     `json:"maxZoom,omitempty"`
	BuildDate   string  `json:"buildDate,omitempty"`
	Error       string  `json:"error,omitempty"`
	SizeBytes   int64   `json:"sizeBytes,omitempty"`
	RequestedBy string  `json:"requestedBy,omitempty"`
	CreatedAt   string  `json:"createdAt,omitempty"`
	CompletedAt string  `json:"completedAt,omitempty"`
}

// refreshLatestBasemap reads the latest record and, if it was last seen
// pending or running, checks the live Job and stamps its outcome before
// returning — polled on demand rather than by a background goroutine, so
// there is nothing to keep running between requests nobody is looking at.
// Shared by the status endpoint (to report current state) and the trigger
// endpoint (to decide whether one is already in progress): both need the
// same "is this actually still running" answer, not two copies of it that
// could disagree.
func (s *Server) refreshLatestBasemap(ctx context.Context) (basemap.Record, error) {
	rec, err := s.Basemap.Latest()
	if err != nil {
		return basemap.Record{}, err
	}

	if rec.Status != basemapStatusPending && rec.Status != basemapStatusRunning {
		return rec, nil
	}

	outcome, err := s.BasemapJobs.Outcome(ctx, rec.JobName)
	if err != nil || !outcome.Done {
		return rec, nil
	}
	if outcome.Succeeded {
		_ = s.Basemap.MarkSucceeded(rec.ID, outcome.SizeBytes)
	} else {
		_ = s.Basemap.MarkFailed(rec.ID, outcome.Message)
	}
	// Re-read rather than mutate rec in place: Mark* stamps completedAt
	// server-side, and callers should see exactly what is now stored, not
	// a guess at it.
	if refreshed, err := s.Basemap.Latest(); err == nil {
		rec = refreshed
	}
	return rec, nil
}

// basemapDTOFor reports the latest update's state for this caller.
func (s *Server) basemapDTOFor(r *http.Request) basemapUpdateDTO {
	dto := basemapUpdateDTO{Available: s.Basemap != nil && s.BasemapJobs != nil}

	id := auth.FromContext(r.Context())
	if !id.Role.Can(auth.PermManageSettings) {
		// Not an error worth explaining, same reasoning as
		// garminConsumerDTOFor: a rider has no business here.
		return dto
	}
	dto.CanManage = dto.Available
	if !dto.Available {
		dto.Unavailable = "this deployment has no basemap update Job configured"
		return dto
	}

	rec, err := s.refreshLatestBasemap(r.Context())
	if err != nil {
		// basemap.ErrNoRecord means nobody has ever triggered one —
		// Available/CanManage true, HasRun false is the whole story.
		return dto
	}

	dto.HasRun = true
	dto.Status = string(rec.Status)
	if rec.Status == basemapStatusPending || rec.Status == basemapStatusRunning {
		if percent, ok := s.BasemapJobs.Progress(r.Context(), rec.JobName); ok {
			dto.Progress = &percent
		}
	}
	dto.West, dto.South, dto.East, dto.North = rec.BBox.West, rec.BBox.South, rec.BBox.East, rec.BBox.North
	dto.MaxZoom = rec.MaxZoom
	dto.BuildDate = rec.BuildDate
	dto.Error = rec.Error
	dto.SizeBytes = rec.SizeBytes
	dto.RequestedBy = rec.RequestedBy
	dto.CreatedAt = rec.CreatedAt.Format(time.RFC3339)
	if rec.CompletedAt != nil {
		dto.CompletedAt = rec.CompletedAt.Format(time.RFC3339)
	}
	return dto
}

// Local aliases so this file does not need "basemap." on every status
// comparison — the exported constants still live in the basemap package,
// this just shortens the reads above.
const (
	basemapStatusPending = basemap.StatusPending
	basemapStatusRunning = basemap.StatusRunning
)

// handleBasemap reports the latest update's status.
func (s *Server) handleBasemap(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}
	writeJSON(w, http.StatusOK, s.basemapDTOFor(r))
}

// handleBasemapUpdate triggers a new update.
func (s *Server) handleBasemapUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}
	if s.Basemap == nil || s.BasemapJobs == nil {
		s.logger().Warn("basemap update requested but no Job is configured",
			"by", auth.FromContext(r.Context()).User)
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no basemap update Job configured",
		})
		return
	}

	var body struct {
		West, South, East, North float64
		MaxZoom                  int `json:"maxZoom"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	bbox := basemap.BBox{West: body.West, South: body.South, East: body.East, North: body.North}
	if err := bbox.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := basemap.ValidateMaxZoom(body.MaxZoom); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Refuse a second trigger while one is already in flight: two
	// concurrently-running Jobs (a double-click, two admins, a retried
	// request) would both kubectl cp + mv into the exact same path on the
	// exact same tiles pod with no coordination between them, and
	// whichever mv lands second silently wins over a file that might not
	// even be the other Job's complete download. refreshLatestBasemap
	// checks the live Job first, so a record that's actually finished
	// (just not yet observed as such) does not block a new trigger.
	if existing, err := s.refreshLatestBasemap(r.Context()); err == nil {
		if existing.Status == basemapStatusPending || existing.Status == basemapStatusRunning {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "an update is already in progress — wait for it to finish before starting another",
			})
			return
		}
	}

	admin := auth.FromContext(r.Context()).User
	buildDate := time.Now().UTC().Format("20060102")

	id, err := s.Basemap.Create(bbox, body.MaxZoom, buildDate, admin)
	if errors.Is(err, basemap.ErrAlreadyInProgress) {
		// The pre-check above missed a request that landed at nearly the
		// same instant — this is the database's own constraint catching
		// what the pre-check alone could not guarantee.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "an update is already in progress — wait for it to finish before starting another",
		})
		return
	}
	if err != nil {
		s.logger().Error("recording basemap update failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record the update"})
		return
	}

	jobName, err := s.BasemapJobs.Trigger(r.Context(), bbox, body.MaxZoom, buildDate)
	if err != nil {
		s.logger().Error("triggering basemap update job failed", "err", err)
		_ = s.Basemap.MarkFailed(id, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the cluster refused the update job: " + err.Error(),
		})
		return
	}
	if err := s.Basemap.SetJobName(id, jobName); err != nil {
		// Warn, not error: the job itself was already triggered
		// successfully above — this only loses the name this record would
		// otherwise use to correlate with the job's own status later.
		s.logger().Warn("recording basemap job name failed", "err", err)
	}

	s.logger().Info("basemap update triggered", "by", admin, "job", jobName,
		"bbox", bbox, "maxZoom", body.MaxZoom)
	writeJSON(w, http.StatusAccepted, s.basemapDTOFor(r))
}
