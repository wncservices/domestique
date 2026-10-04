package api

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutexport"
)

// exportFormat is a trainer-app file format a workout can be written as.
type exportFormat struct {
	name string // the `format` query value
	ext  string
	mime string
	// needsFTP is true for the formats that store targets relative to FTP.
	needsFTP bool
	write    func([]workout.WorkoutStep, float64, workoutexport.Meta) ([]byte, error)
}

var exportFormats = map[string]exportFormat{
	"zwo": {"zwo", ".zwo", "application/xml", true, workoutexport.Zwo},
	"mrc": {"mrc", ".mrc", "text/plain", true, workoutexport.Mrc},
	"erg": {"erg", ".erg", "text/plain", false, workoutexport.Erg},
}

// exportRefusal is a workout that cannot be written in a format: Status is
// 422 when the workout itself cannot be expressed, 409 when only the rider's
// FTP is missing.
type exportRefusal struct {
	Status  int
	Class   string // for the log line; never the message, which names the workout's shape
	Message string
}

const (
	exportNotCyclingMessage = "Only cycling workouts export to trainer files."
	exportHRMessage         = "This workout is paced by heart rate and can't be turned into power. Set your FTP first."
	exportNoFTPMessage      = "Set your FTP to export this as .zwo or .mrc."
)

// renderExport writes wk in format, from its indoor form: a trainer holds
// time and power, so distance and open-duration steps are already time-based
// and heart-rate steps already power when FTP and zone are known. Whatever is
// still not time-and-power after that is refused, never guessed. The
// converter's note goes into the description, so a rider sees why three hours
// became two.
func renderExport(wk workout.Workout, profile workout.RiderProfile, f exportFormat) ([]byte, *exportRefusal) {
	res, err := indoor.Convert(wk, profile)
	if err != nil {
		return nil, &exportRefusal{http.StatusUnprocessableEntity, "not_cycling", exportNotCyclingMessage}
	}
	description := wk.Description
	if res.Note != "" {
		description = addNote(description, res.Note)
	}
	name := workout.DeviceName(wk.Name, wk.Indoor)
	file := source.Slugify(name)
	if file == "" {
		file = "workout"
	}
	body, err := f.write(res.Steps, profile.FTPWatts, workoutexport.Meta{Name: name, Description: description, FileName: file})
	if err == nil {
		return body, nil
	}

	var open *workoutexport.OpenStepError
	switch {
	case errors.Is(err, workoutexport.ErrHRTarget):
		return nil, &exportRefusal{http.StatusUnprocessableEntity, "heart_rate", exportHRMessage}
	case errors.Is(err, workoutexport.ErrNoFTP):
		return nil, &exportRefusal{http.StatusConflict, "no_ftp", exportNoFTPMessage}
	case errors.As(err, &open):
		return nil, &exportRefusal{http.StatusUnprocessableEntity, "open_step", fmt.Sprintf(
			"This workout has an open-effort step, such as an FTP test, that has to stay in resistance mode, and %s files can't hold it: they would lock the effort to a target. Export it as .zwo instead.",
			open.Format)}
	case errors.Is(err, workoutexport.ErrNotTimed):
		return nil, &exportRefusal{http.StatusUnprocessableEntity, "not_timed", "This workout has a step with no fixed length, so it can't be turned into a trainer file."}
	case errors.Is(err, workoutexport.ErrEmpty):
		return nil, &exportRefusal{http.StatusUnprocessableEntity, "empty", "This workout has no steps to export."}
	}
	// Anything else is a bug in an encoder, not something the rider can fix.
	return nil, &exportRefusal{http.StatusInternalServerError, "encode", "could not write the file"}
}

// exportFormatOf reads ?format=, answering 400 itself when it is missing or
// unknown. Case-sensitive on purpose: the three values are a closed list.
func exportFormatOf(w http.ResponseWriter, r *http.Request) (exportFormat, bool) {
	f, ok := exportFormats[r.URL.Query().Get("format")]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be zwo, mrc or erg"})
	}
	return f, ok
}

func writeExportAttachment(log *slog.Logger, w http.ResponseWriter, filename, mime string, body []byte) {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- served as an attachment with a fixed content type and
	// nosniff, never rendered as a page.
	if _, err := w.Write(body); err != nil {
		log.Error("write export", "err", err)
	}
}

// handleExportWorkout downloads one workout as .zwo, .mrc or .erg. Its
// sibling handleDownloadWorkoutFIT keeps its own route and shape, so nothing
// that links to it can break.
func (s *Server) handleExportWorkout(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	f, ok := exportFormatOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	wk, err := s.Training.GetWorkout(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, wk.Rider) {
		s.forbidTraining(w, r)
		return
	}
	profile, _, err := s.Training.GetProfile(r.Context(), wk.Rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	body, refusal := renderExport(wk, profile, f)
	if refusal != nil {
		// Rider, id, format and the class of refusal: no steps, no watts.
		s.logger().Info("workout export refused", "rider", identity.User, "id", id, "format", f.name, "reason", refusal.Class)
		if refusal.Status >= 500 {
			s.logger().Error("workout export failed", "rider", identity.User, "id", id, "format", f.name)
		}
		writeJSON(w, refusal.Status, map[string]string{"error": refusal.Message})
		return
	}
	s.logger().Info("workout exported", "rider", identity.User, "id", id, "format", f.name)
	file := source.Slugify(workout.DeviceName(wk.Name, wk.Indoor))
	if file == "" {
		file = "workout"
	}
	writeExportAttachment(s.logger(), w, file+f.ext, f.mime, body)
}

// handleExportWeek downloads the caller's workouts of one week as a zip, one
// file per exportable workout. A workout that refuses is skipped and named in
// SKIPPED.txt with the reason, so a rider with one heart-rate-only day still
// gets the other six. Only the caller's own workouts are read.
func (s *Server) handleExportWeek(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	f, ok := exportFormatOf(w, r)
	if !ok {
		return
	}
	day, err := time.Parse(dateLayout, r.PathValue("monday"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the week must be a date, YYYY-MM-DD"})
		return
	}
	start := periodization.MondayOf(day)
	first, last := start.Format(dateLayout), start.AddDate(0, 0, 6).Format(dateLayout)

	rider := auth.FromContext(r.Context()).User
	all, err := s.Training.ListWorkouts(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	var week []workout.Workout
	for _, wk := range all {
		if wk.Date >= first && wk.Date <= last {
			week = append(week, wk)
		}
	}
	if len(week) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no workouts that week"})
		return
	}
	sort.SliceStable(week, func(i, j int) bool { return week[i].Date < week[j].Date })

	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var skipped []string
	used := map[string]int{}
	exported := 0
	for _, wk := range week {
		base := wk.Date + "-" + source.Slugify(workout.DeviceName(wk.Name, wk.Indoor))
		if strings.HasSuffix(base, "-") {
			base += "workout"
		}
		body, refusal := renderExport(wk, profile, f)
		if refusal != nil {
			if refusal.Status >= 500 {
				s.fail(w, errors.New(refusal.Message))
				return
			}
			skipped = append(skipped, base+": "+refusal.Message)
			continue
		}
		used[base]++
		entry := base
		if n := used[base]; n > 1 {
			entry = fmt.Sprintf("%s-%d", base, n)
		}
		if !addZipFile(zw, entry+f.ext, body) {
			s.logger().Error("week export could not be zipped", "rider", rider, "week", first, "format", f.name)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the zip"})
			return
		}
		exported++
	}
	if len(skipped) > 0 {
		text := "These workouts could not be exported as ." + f.name + ":\r\n\r\n" + strings.Join(skipped, "\r\n") + "\r\n"
		if !addZipFile(zw, "SKIPPED.txt", []byte(text)) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the zip"})
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.logger().Error("week export could not be zipped", "rider", rider, "week", first, "format", f.name)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the zip"})
		return
	}
	s.logger().Info("week exported", "rider", rider, "week", first, "format", f.name, "exported", exported, "skipped", len(skipped))
	if exported == 0 {
		// Still a 200 (the zip explains itself in SKIPPED.txt), but the page
		// can tell the rider instead of saving a zip with nothing in it.
		w.Header().Set("X-Domestique-Skipped", "all")
	}
	writeExportAttachment(s.logger(), w, fmt.Sprintf("week-%s-%s.zip", first, f.name), "application/zip", buf.Bytes())
}

func addZipFile(zw *zip.Writer, name string, body []byte) bool {
	fw, err := zw.Create(name)
	if err != nil {
		return false
	}
	_, err = fw.Write(body)
	return err == nil
}
