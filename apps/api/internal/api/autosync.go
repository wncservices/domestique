package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// FlagAutoSync is settings.Store's flag name for auto-sync — see
// settings.Store.Flag/SetFlag's own doc comments for why this lives in the
// plain flags table rather than the encrypted settings one.
const FlagAutoSync = "auto_sync"

type autoSyncDTO struct {
	Enabled bool `json:"enabled"`
	// CanManage is whether *this caller* may change it — admin, and a place
	// to keep it, the same shape garminConsumerDTO's own CanManage already
	// uses. False hides the toggle rather than offering a 403.
	CanManage bool   `json:"canManage"`
	UpdatedBy string `json:"updatedBy,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func (s *Server) autoSyncDTOFor(r *http.Request) (autoSyncDTO, error) {
	enabled, err := s.Settings.Flag(FlagAutoSync)
	if err != nil {
		return autoSyncDTO{}, err
	}
	dto := autoSyncDTO{
		Enabled:   enabled,
		CanManage: auth.FromContext(r.Context()).Role.Can(auth.PermManageSettings),
	}
	if meta, err := s.Settings.DescribeFlag(FlagAutoSync); err == nil {
		dto.UpdatedBy = meta.UpdatedBy
		dto.UpdatedAt = meta.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto, nil
}

// handleAutoSync reports whether auto-sync is on.
func (s *Server) handleAutoSync(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}
	dto, err := s.autoSyncDTOFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleSetAutoSync flips it. Admin-only: this changes every rider's
// upload/edit behavior at once, not just the caller's own.
func (s *Server) handleSetAutoSync(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	who := auth.FromContext(r.Context()).User
	if err := s.Settings.SetFlag(FlagAutoSync, body.Enabled, who); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("auto-sync setting changed", "enabled", body.Enabled, "by", who)

	dto, err := s.autoSyncDTOFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// autoSyncIfEnabled runs a push in the background after an upload, edit, or
// delete succeeds, if — and only if — an admin has turned auto-sync on. It
// never blocks or fails the request that triggered it: the write to the
// library already succeeded and has already been reported to the caller by
// the time this runs, so a slow or failing push here must not turn a
// successful upload into an error response, or make the caller wait on
// Garmin/Wahoo's own network round trip before they get their own answer
// back. Reuses runPush exactly as handlePush does — no selection narrows it,
// so it re-diffs the whole library and pushes only what is actually stale,
// which is always at least the route that just changed and nothing that
// was already in sync.
//
// context.Background(), not the request's own context: the request context
// is cancelled the moment this handler returns and writes its response,
// which would race this goroutine out from under it on every single call.
func (s *Server) autoSyncIfEnabled(rider string) {
	if s.Settings == nil {
		return
	}
	enabled, err := s.Settings.Flag(FlagAutoSync)
	if err != nil {
		s.logger().Warn("checking auto-sync setting failed", "err", err)
		return
	}
	if !enabled {
		return
	}

	go func() {
		defer s.guard("auto-sync push")
		// Shares backgroundSyncLockKey with AutoImportTick's own push — see
		// that key's comment for why the same lock has to cover both paths.
		withDBLock(context.Background(), s.dbConn(), backgroundSyncLockKey, func() {
			resp, err := s.runPush(context.Background(), nil, true, nil)
			if err != nil {
				s.logger().Error("auto-sync push failed", "triggered_by", rider, "err", err)
				return
			}
			s.logger().Info("auto-sync push finished",
				"triggered_by", rider, "changes", len(resp.Items), "failures", len(resp.Failures))
		})
	}()
}
