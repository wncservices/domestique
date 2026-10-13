package garmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// coursePath is the course service. Connect's own Training → Courses → Import
// reaches it as `https://connect.garmin.com/gc-api/course-service/course`; the
// `/gc-api` prefix is the web app's proxy onto the same service, and with an
// OAuth2 bearer the service is reached directly on connectapi.
//
// Importing is **two calls**, which is not obvious and cost a full library's
// worth of failed pushes to discover:
//
//  1. POST /import with the file. This only *parses* it. The response is a
//     course object with the name read out of the file and `courseId: null` —
//     nothing has been saved, and the 200 says nothing about whether it will
//     be.
//  2. POST that object back to the service. This is what creates the course,
//     and this is the response that carries the id.
//
// Undocumented and liable to move. When it does, the failure is one push
// returning an error — the route stays in the library and nothing else stops.
const coursePath = "/course-service/course"

// ImportCourse uploads a course file and returns Garmin's id for it.
//
// The file may be FIT, GPX or TCX as far as Connect is concerned. Domestique
// sends FIT: a GPX navigates as a breadcrumb line with nothing said at a
// junction, and a FIT course can carry turn cues.
func (c *Client) ImportCourse(ctx context.Context, filename string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("garmin: refusing to upload an empty course")
	}
	if filename == "" {
		filename = "course.fit"
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := form.Close(); err != nil {
		return "", err
	}

	raw, status, err := c.do(ctx, http.MethodPost, c.APIBase+coursePath+"/import", &body,
		form.FormDataContentType(),
		header{"Authorization", "Bearer " + bearer},
		// Connect's services answer HTML to a browser-shaped request and JSON
		// to this one; both headers are what its own web app sends.
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return "", err
	}

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "", ErrSessionRefused
	case status >= 300:
		return "", fmt.Errorf("garmin: course import returned %d: %s", status, snippet(raw))
	}

	// A parse that already carries an id would mean Connect had folded the
	// two calls into one. It does not today, but taking the id when it is
	// there costs nothing and means this keeps working if that changes.
	if id, err := courseID(raw); err == nil {
		return id, nil
	}
	return c.saveCourse(ctx, raw)
}

// saveCourse turns a parsed course into a saved one.
//
// The body is what /import handed back with one addition: /import never sets
// coursePrivacy, but the save endpoint rejects the DTO without a valid value.
func (c *Client) saveCourse(ctx context.Context, parsed []byte) (string, error) {
	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return "", err
	}

	body, err := withPrivacy(parsed)
	if err != nil {
		return "", err
	}

	raw, status, err := c.do(ctx, http.MethodPost, c.APIBase+coursePath, bytes.NewReader(body),
		"application/json",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return "", err
	}

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "", ErrSessionRefused
	case status >= 300:
		// Not snippet(): this body is a Bean Validation dump that names every
		// field of the DTO, which is the only description of that shape that
		// exists anywhere. Truncating it at 200 characters is what made the
		// privacy field a guess rather than a lookup.
		return "", fmt.Errorf("garmin: saving the course returned %d: %s", status, longSnippet(raw))
	}

	id, err := courseID(raw)
	if err != nil {
		return "", fmt.Errorf("garmin: the course was saved but no id came back: %w", err)
	}
	return id, nil
}

// DeleteCourse removes a course from the account.
func (c *Client) DeleteCourse(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("garmin: no course id to delete")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return err
	}

	raw, status, err := c.do(ctx, http.MethodDelete, c.APIBase+coursePath+"/"+id, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return err
	}

	switch {
	case status == http.StatusNotFound:
		// Already gone is the outcome the caller wanted.
		return nil
	case status >= 300:
		return fmt.Errorf("garmin: deleting course %s returned %d: %s", id, status, snippet(raw))
	}
	return nil
}

// privacyPrivate is Garmin's id for a course only its owner can see.
//
// The service enumerates 1=Public, 2=Private, 4=Group. Private is the right
// default for somebody else's route library: a push should not publish a
// rider's routes, and making a private course public later is a click, while
// the reverse is a course strangers already saw.
const privacyPrivate = 2

// sourceTypeIDCourse is not a privacy choice — every course carries it, and
// every course carries the same value. It appears alongside rulePK in the
// same validation failure and looked, before it was confirmed, like it might
// be part of the privacy question. It is not: rulePK alone is what 1/2/4
// means. This is closer to a discriminator saying "this row is a course" than
// anything a rider or an operator would ever choose.
const sourceTypeIDCourse = 3

// withPrivacy gives the parsed DTO the two fields the save endpoint requires
// and /import never sets:
//
//	'createCourse.arg3.rulePK' must not be null
//	'createCourse.arg3.sourceTypeId' must not be null
//
// Both names are confirmed, not guessed. Four prior names — coursePrivacy,
// privacyType, and privacyRule/coursePrivacyRule nested as {typeId, typeKey}
// — were deployed across two changes and produced the identical rejection
// each time, which is what unrecognised JSON properties being silently
// dropped looks like. The two names here come from capturing the request
// Connect's own web app sends when creating a course through Training →
// Courses → Create and saving it as Private: rulePK arrived as 2, the same
// integer the class-level message already used for private, under a name
// nothing in the service's own errors had said out loud until the rejection
// started naming properties instead of the whole DTO.
func withPrivacy(parsed []byte) ([]byte, error) {
	var dto map[string]any
	if err := json.Unmarshal(parsed, &dto); err != nil {
		return nil, fmt.Errorf("garmin: unreadable course response: %s", snippet(parsed))
	}

	if !validPrivacy(dto["rulePK"]) {
		// Connect already saying what it wants beats our default.
		dto["rulePK"] = privacyPrivate
	}
	dto["sourceTypeId"] = sourceTypeIDCourse

	out, err := json.Marshal(dto)
	if err != nil {
		return nil, fmt.Errorf("garmin: could not re-encode course: %w", err)
	}
	return out, nil
}

// validPrivacy reports whether a value is already one of the service's three
// privacy ids.
func validPrivacy(value any) bool {
	v, ok := value.(float64)
	return ok && (v == 1 || v == 2 || v == 4)
}

// courseID digs the new course's id out of the response.
//
// The response shape is not documented and has more than one plausible
// spelling, so rather than pin one and break on the next deploy, look for the
// ones Connect is known to use and say clearly when none is present.
func courseID(raw []byte) (string, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("garmin: unreadable course response: %s", snippet(raw))
	}

	for _, key := range []string{"courseId", "id", "coursePk"} {
		switch value := body[key].(type) {
		case string:
			if value != "" {
				return value, nil
			}
		case float64:
			// Every JSON number decodes as float64; course ids are integers.
			return fmt.Sprintf("%.0f", value), nil
		}
	}
	return "", fmt.Errorf("garmin: the course was accepted but the response named no id: %s", snippet(raw))
}

// longSnippet keeps far more of a body than snippet, for the one case where
// the body is the documentation: a validation failure from an undocumented
// service lists the fields it was given, and nothing else does.
//
// geoPoints is dropped first. A course-privacy rejection dumps the whole
// CourseDTO, and geoPoints — one entry per track point, hundreds of them —
// is both the first field and, on its own, longer than any limit worth
// logging. Every other field sits after it, so a fixed character limit never
// reached them: 2000 characters covered perhaps a dozen points and nothing
// else. That is what made the real cause a guess instead of a lookup.
func longSnippet(raw []byte) string {
	const limit = 2000
	text := strings.TrimSpace(withoutGeoPoints(string(raw)))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// withoutGeoPoints replaces a `geoPoints=[...]` array with a placeholder,
// tracking bracket depth rather than assuming the array has no nested
// brackets of its own — a GeoPointDTO entry does not today, but a size limit
// should not depend on that staying true.
//
// Text with no such array — any response that is not this one specific
// rejection — is returned unchanged.
func withoutGeoPoints(text string) string {
	const marker = "geoPoints=["
	start := strings.Index(text, marker)
	if start < 0 {
		return text
	}
	open := start + len(marker) - 1 // index of the marker's own '['

	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return text[:open+1] + "…" + text[i:]
			}
		}
	}
	// No matching close within the body: it was cut off mid-array by some
	// earlier truncation. Nothing after it would be readable either.
	return text[:open+1] + "…"
}

// snippet keeps an error readable when the body is an HTML error page.
func snippet(raw []byte) string {
	const limit = 200
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// coursesOwnerPath lists every course already on the account — what
// Connect's own Training → Courses page shows, not scoped to anything this
// app pushed there itself.
//
// Under a different service ("web-gateway") than the rest of this file's
// course-service endpoints — found by inspecting that page's own network
// traffic, not documented anywhere, same as everything else here. That
// traffic goes through connect.garmin.com's /gc-api/ proxy with a browser's
// cookie session; this package authenticates with a Bearer token straight
// against connectapi.garmin.com instead (see bearerToken), a different
// surface that was not itself exercised during discovery. The shape below
// is confirmed; whether this specific path accepts a Bearer token the same
// way the rest of course-service does is not, until a real first call
// proves it.
const coursesOwnerPath = "/web-gateway/course/owner/"

// Course is one course already on the account, as returned by ListCourses.
type Course struct {
	ID           string
	Name         string
	DistanceM    float64
	AscentM      float64
	StartLat     float64
	StartLng     float64
	ActivityType string
	CreatedAt    time.Time
}

// coursesOwnerResponse is the shape ListCourses reads back — a single
// top-level key wrapping the array, not a bare list.
type coursesOwnerResponse struct {
	CoursesForUser []courseSummaryDTO `json:"coursesForUser"`
}

type courseSummaryDTO struct {
	CourseID              json.Number `json:"courseId"`
	CourseName            string      `json:"courseName"`
	DistanceInMeters      float64     `json:"distanceInMeters"`
	ElevationGainInMeters float64     `json:"elevationGainInMeters"`
	StartLatitude         float64     `json:"startLatitude"`
	StartLongitude        float64     `json:"startLongitude"`
	ActivityType          struct {
		TypeKey string `json:"typeKey"`
	} `json:"activityType"`
	// Milliseconds since the epoch, Connect's own convention — same as
	// devices.go's lastSyncTime.
	CreatedDate int64 `json:"createdDate"`
}

// ListCourses lists every course already on the account.
func (c *Client) ListCourses(ctx context.Context) ([]Course, error) {
	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}

	raw, status, err := c.do(ctx, http.MethodGet, c.APIBase+coursesOwnerPath, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return nil, err
	}

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return nil, ErrSessionRefused
	case status >= 300:
		return nil, fmt.Errorf("garmin: listing courses returned %d: %s", status, snippet(raw))
	}

	var body coursesOwnerResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("garmin: unreadable course list: %s", snippet(raw))
	}

	out := make([]Course, 0, len(body.CoursesForUser))
	for _, dto := range body.CoursesForUser {
		out = append(out, Course{
			ID:           dto.CourseID.String(),
			Name:         dto.CourseName,
			DistanceM:    dto.DistanceInMeters,
			AscentM:      dto.ElevationGainInMeters,
			StartLat:     dto.StartLatitude,
			StartLng:     dto.StartLongitude,
			ActivityType: dto.ActivityType.TypeKey,
			CreatedAt:    time.UnixMilli(dto.CreatedDate).UTC(),
		})
	}
	return out, nil
}

// DownloadGPX fetches a course's track as GPX — the format this app already
// parses everywhere else (internal/gpx), so a downloaded course needs no new
// parser to become a Route.
func (c *Client) DownloadGPX(ctx context.Context, courseID string) ([]byte, error) {
	if strings.TrimSpace(courseID) == "" {
		return nil, errors.New("garmin: no course id to download")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}

	raw, status, err := c.do(ctx, http.MethodGet, c.APIBase+coursePath+"/gpx/"+courseID, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "*/*"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return nil, err
	}

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return nil, ErrSessionRefused
	case status == http.StatusNotFound:
		return nil, fmt.Errorf("garmin: course %s not found", courseID)
	case status >= 300:
		return nil, fmt.Errorf("garmin: downloading course %s returned %d: %s", courseID, status, snippet(raw))
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("garmin: course %s downloaded empty", courseID)
	}
	return raw, nil
}
