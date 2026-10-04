package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/rideimport"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Ride-history import: a rider uploads a Strava or Garmin export (or loose FIT
// files) and their past rides become sessions, analyses and fitness history.
// See docs/superpowers/specs/2026-09-29-export-and-import-design.md.
//
// What this file must keep true, because the upload is large, hostile by
// default, and full of GPS and health data:
//   - the rider is the session's, never the form's;
//   - nothing from inside the upload is persisted, and no file name, coordinate
//     or heart-rate or power value reaches a log line or a table: only ids and
//     counts;
//   - the only disk use is a 0600 temp spool per file part, which the job (or,
//     before the job exists, the handler) removes on every path.

const (
	// defaultImportMaxUploadBytes is the request cap: 1 GiB.
	defaultImportMaxUploadBytes = 1 << 30
	// importCooldown is how long after a finished import the next may start.
	importCooldown = 10 * time.Minute
	// importHeartbeat is how often a running job refreshes updated_at, so a
	// long read that emits nothing is not mistaken for a dead one.
	importHeartbeat = 30 * time.Second
	// importProgressEvery is how many rides pass between count updates.
	importProgressEvery = 20
)

// rideImportDTO mirrors RideImport in apps/web/src/api/types.ts.
type rideImportDTO struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Phase string `json:"phase"`

	Added        int `json:"added"`
	Duplicate    int `json:"duplicate"`
	SkippedSport int `json:"skippedSport"`
	Unsupported  int `json:"unsupported"`
	Unreadable   int `json:"unreadable"`

	// Error is a short class ("too large"), never a message from the upload.
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"startedAt"`
	UpdatedAt string `json:"updatedAt"`
	// EarliestDate is how far back the rider's history now goes; set once a
	// job is done.
	EarliestDate string `json:"earliestDate,omitempty"`
	// CooldownUntil is when the next import may start, while one is held back.
	CooldownUntil string `json:"cooldownUntil,omitempty"`
}

func (s *Server) importMaxBytes() int64 {
	if s.ImportMaxUploadBytes > 0 {
		return s.ImportMaxUploadBytes
	}
	return defaultImportMaxUploadBytes
}

func (s *Server) importLimits() rideimport.Limits {
	if s.ImportLimits == (rideimport.Limits{}) {
		return rideimport.DefaultLimits
	}
	return s.ImportLimits
}

// cooldownEnd is when a finished job stops holding the rider back, and false
// when it holds nothing: a running job has its own answer, and one a restart
// interrupted must be re-uploadable at once, or the rider could not finish it.
func cooldownEnd(ri workout.RideImport) (time.Time, bool) {
	over := ri.State == workout.ImportDone || (ri.State == workout.ImportFailed && ri.Error != "interrupted")
	if !over {
		return time.Time{}, false
	}
	finished, err := time.Parse(time.RFC3339, ri.UpdatedAt)
	if err != nil {
		return time.Time{}, false
	}
	return finished.Add(importCooldown), true
}

func (s *Server) handleImportStatus(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	now := s.now()
	ri, ok, err := s.Training.LatestRideImport(r.Context(), rider, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	dto := rideImportDTO{
		ID: ri.ID, State: ri.State, Phase: ri.Phase,
		Added: ri.Added, Duplicate: ri.Duplicate, SkippedSport: ri.SkippedSport,
		Unsupported: ri.Unsupported, Unreadable: ri.Unreadable,
		Error: ri.Error, StartedAt: ri.StartedAt, UpdatedAt: ri.UpdatedAt,
	}
	if ri.State == workout.ImportDone {
		if dto.EarliestDate, err = s.Training.EarliestSessionDate(r.Context(), rider); err != nil {
			s.fail(w, err)
			return
		}
	}
	if end, held := cooldownEnd(ri); held && now.Before(end) {
		dto.CooldownUntil = end.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, dto)
}

// spooled is one uploaded file part, held in a 0600 temp file.
type spooled struct {
	f    *os.File
	size int64
}

func removeSpools(spools []spooled) {
	for _, sp := range spools {
		_ = sp.f.Close()
		_ = os.Remove(sp.f.Name()) // best effort: the spool holds only the upload
	}
}

func (s *Server) handleImportUpload(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	// The rider is the session's. A `rider` field in the form is never read.
	rider := auth.FromContext(ctx).User
	now := s.now()

	// Refuse before reading a gigabyte: one running import per rider, and a
	// pause after a finished one.
	if latest, ok, err := s.Training.LatestRideImport(ctx, rider, now); err != nil {
		s.fail(w, err)
		return
	} else if ok {
		if latest.State == workout.ImportRunning {
			s.logger().Info("ride import refused: one is running", "rider", rider, "job", latest.ID)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "An import is already running. Wait for it to finish."})
			return
		}
		if end, held := cooldownEnd(latest); held && now.Before(end) {
			wait := end.Sub(now)
			s.logger().Info("ride import refused: cooling down", "rider", rider, "job", latest.ID)
			w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf(
				"You imported recently. Try again in %d minutes.", int((wait+time.Minute-1)/time.Minute))})
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.importMaxBytes())
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Send the file as a multipart upload."})
		return
	}

	var spools []spooled
	handedOff := false
	defer func() {
		if !handedOff {
			removeSpools(spools)
		}
	}()

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.importReadFailed(w, rider, err)
			return
		}
		// Only file parts are read; any other field (a stray `rider` among them)
		// is ignored. A file's own name is never looked at.
		if part.FormName() != "file" || part.FileName() == "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<20))
			continue
		}
		tmp, err := os.CreateTemp("", "domestique-upload-*")
		if err != nil {
			s.logger().Error("ride import: could not create a spool", "rider", rider, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the upload for processing"})
			return
		}
		spools = append(spools, spooled{f: tmp})
		n, err := io.Copy(tmp, part)
		spools[len(spools)-1].size = n
		if err != nil {
			s.importReadFailed(w, rider, err)
			return
		}
	}
	if len(spools) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose a file to import."})
		return
	}

	id, err := newImportID()
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Training.StartRideImport(ctx, rider, id, now); err != nil {
		if errors.Is(err, workout.ErrImportRunning) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "An import is already running. Wait for it to finish."})
			return
		}
		s.fail(w, err)
		return
	}

	// The job owns the spools from here. It runs on the server's lifecycle,
	// not the request's, which ends the moment this response is written.
	handedOff = true
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		s.runRideImport(s.lifecycle(), rider, id, spools)
	}()

	s.logger().Info("ride import started", "rider", rider, "job", id, "files", len(spools))
	writeJSON(w, http.StatusAccepted, rideImportDTO{ID: id, State: workout.ImportRunning, Phase: workout.ImportReading, StartedAt: now.UTC().Format(time.RFC3339), UpdatedAt: now.UTC().Format(time.RFC3339)})
}

// importReadFailed answers a failed read of the upload: 413 over the cap, 400
// otherwise (a dropped connection, a malformed body).
func (s *Server) importReadFailed(w http.ResponseWriter, rider string, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		s.logger().Info("ride import refused: the upload is over the cap", "rider", rider)
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "That upload is larger than 1 GiB. Pick fewer files, or split the export."})
		return
	}
	s.logger().Info("ride import refused: the upload could not be read", "rider", rider)
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "The upload could not be read."})
}

func newImportID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// importProgress is the job's running tally, shared with its heartbeat.
type importProgress struct {
	mu     sync.Mutex
	phase  string
	counts workout.RideImportCounts
}

func (p *importProgress) snapshot() (string, workout.RideImportCounts) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase, p.counts
}

func (p *importProgress) update(f func(*workout.RideImportCounts)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(&p.counts)
}

func (p *importProgress) setPhase(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
}

// importErrorClass is the short class a failed job is stored under.
func importErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, rideimport.ErrTotalSize):
		return "too large"
	case errors.Is(err, rideimport.ErrTooManyEntries):
		return "too many files"
	case errors.Is(err, rideimport.ErrCompressionRatio):
		return "unsafe archive"
	case errors.Is(err, rideimport.ErrBadArchive):
		return "unreadable archive"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	}
	return "internal"
}

// runRideImport reads every spooled file, files each ride, then recomputes the
// rider's fitness history once and re-runs threshold detection. It takes
// ownership of spools and removes them however it ends.
func (s *Server) runRideImport(ctx context.Context, rider, id string, spools []spooled) {
	defer removeSpools(spools)

	prog := &importProgress{phase: workout.ImportReading}
	// Final writes must land even when the server is shutting down.
	final := context.WithoutCancel(ctx)

	save := func(c context.Context) {
		phase, counts := prog.snapshot()
		if err := s.Training.UpdateRideImport(c, id, phase, counts, s.now()); err != nil {
			s.logger().Warn("ride import: saving progress failed", "rider", rider, "job", id, "err", err)
		}
	}

	stop := make(chan struct{})
	var beat sync.WaitGroup
	beat.Add(1)
	go func() {
		defer beat.Done()
		t := time.NewTicker(importHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				save(final)
			}
		}
	}()

	class := s.importJob(ctx, rider, id, spools, prog, save)

	close(stop)
	beat.Wait()

	state := workout.ImportDone
	if class != "" {
		state = workout.ImportFailed
	}
	_, counts := prog.snapshot()
	if err := s.Training.FinishRideImport(final, id, state, class, counts, s.now()); err != nil {
		// The rides are in but the rider is not told: a write that should have
		// landed did not.
		s.logger().Error("ride import: closing the job failed", "rider", rider, "job", id, "err", err)
	}
	level := s.logger().Info
	if class == "internal" {
		level = s.logger().Error
	}
	level("ride import finished", "rider", rider, "job", id, "state", state, "error", class,
		"added", counts.Added, "duplicate", counts.Duplicate, "skipped_sport", counts.SkippedSport,
		"unsupported", counts.Unsupported, "unreadable", counts.Unreadable)
}

// importJob is the work of a job. It returns the error class that ended it,
// "" for a job that ran to the end. A panic is recovered into "internal": one
// bad file must not take the server down, and the spools are removed by the
// caller either way.
func (s *Server) importJob(ctx context.Context, rider, id string, spools []spooled, prog *importProgress, save func(context.Context)) (class string) {
	defer func() {
		if rec := recover(); rec != nil {
			// The type only: a panic value could carry something from the upload.
			s.logger().Error("ride import: the job panicked", "rider", rider, "job", id, "panic", fmt.Sprintf("%T", rec))
			class = "internal"
		}
	}()
	if s.BeforeImportJob != nil {
		s.BeforeImportJob()
	}

	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.logger().Error("ride import: reading the profile failed", "rider", rider, "job", id, "err", err)
		return "internal"
	}

	seen := 0
	emit := func(fit []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen == 0 {
			prog.setPhase(workout.ImportAnalysing)
			save(ctx)
		}
		seen++

		ride, err := rideimport.Parse(fit, profile)
		switch {
		case errors.Is(err, rideimport.ErrSport):
			prog.update(func(c *workout.RideImportCounts) { c.SkippedSport++ })
			return nil
		case err != nil:
			prog.update(func(c *workout.RideImportCounts) { c.Unreadable++ })
			return nil
		}
		outcome, err := rideimport.Save(ctx, s.Training, rider, ride)
		if err != nil {
			return err
		}
		prog.update(func(c *workout.RideImportCounts) {
			if outcome == rideimport.Added {
				c.Added++
			} else {
				c.Duplicate++
			}
		})
		if seen%importProgressEvery == 0 {
			save(ctx)
		}
		return nil
	}

	limits := s.importLimits()
	for _, sp := range spools {
		rep := rideimport.Read(sp.f, sp.size, limits, emit)
		prog.update(func(c *workout.RideImportCounts) {
			c.Unsupported += rep.Unsupported
			// A file skipped for its size or damaged in transit is one the rider
			// should hear about like a broken one.
			c.Unreadable += rep.Unreadable + rep.Oversize
		})
		if rep.Err != nil {
			class = importErrorClass(rep.Err)
			if class == "internal" {
				s.logger().Error("ride import: filing a ride failed", "rider", rider, "job", id, "err", rep.Err)
			}
			break
		}
	}

	// Whatever was filed reaches the fitness history and detection, even when a
	// cap stopped the job part-way: those rides are in, and a retry that adds
	// nothing must not be the only thing that ever recomputes them.
	if _, c := prog.snapshot(); c.Added+c.Duplicate > 0 {
		prog.setPhase(workout.ImportRecomputing)
		save(ctx)
		final := context.WithoutCancel(ctx)
		if err := s.Training.RecomputeFitnessSnapshots(final, rider); err != nil {
			s.logger().Error("ride import: recomputing the fitness history failed", "rider", rider, "job", id, "err", err)
			return "internal"
		}
		if err := s.detectAfterImport(final, rider, profile); err != nil {
			s.logger().Error("ride import: threshold detection failed", "rider", rider, "job", id, "err", err)
			return "internal"
		}
	}
	return class
}

// detectAfterImport re-runs threshold detection over the rider's history the
// way a sync does: a finding on an empty or estimated field is applied, one on
// a value the rider typed is stored as a suggestion. Detection windows are
// relative to today, so rides from years ago only matter to its 90-day history
// rule and to the fitness chart. FTP tests are not read from an import.
func (s *Server) detectAfterImport(ctx context.Context, rider string, before workout.RiderProfile) error {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return err
	}
	tdr, err := s.detectThresholdsFresh(ctx, rider, before, sessions, s.now(), nil)
	if err != nil {
		return err
	}
	if len(tdr.AutoFields) == 0 {
		return nil
	}
	if _, err := s.Training.SaveProfile(ctx, tdr.Profile); err != nil {
		return err
	}
	s.logger().Info("training profile auto-filled by an import", "rider", rider, "fields", tdr.AutoFields)
	s.recordDetectedThresholds(ctx, rider, tdr.Detected)
	if tdr.Profile.FTPWatts != before.FTPWatts {
		if _, _, err := s.recalibrateLevelsForFTP(ctx, rider, before, "auto_applied"); err != nil {
			// The profile write has landed, so this is logged, not failed: the
			// same call a sync makes.
			s.logger().Error("level recalibration failed", "rider", rider, "err", err)
		}
	}
	return nil
}
