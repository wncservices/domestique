package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestUpdateRoutePointsReplacesTheTrack proves the actual point of the
// endpoint: a route's own track can be replaced in place — the same slug,
// same name, but a new path — rather than the only option being deleting
// it and creating a new one.
func TestUpdateRoutePointsReplacesTheTrack(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+route.Slug+"/points", `{
		"points": [
			{"lat": 50.90, "lon": 4.40},
			{"lat": 50.91, "lon": 4.41},
			{"lat": 50.92, "lon": 4.42}
		]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out routeDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "Hill Loop" {
		t.Errorf("name = %q, want unchanged from before the edit", out.Name)
	}
	if out.PointCount != 3 {
		t.Errorf("pointCount = %d, want 3 — the new track, not the old one", out.PointCount)
	}
	if out.ContentHash == route.ContentHash {
		t.Error("contentHash unchanged — a different path must hash differently")
	}

	trackResp := h.as("wilant", "cyclists", http.MethodGet, "/api/tracks/"+route.Slug, "")
	if trackResp.StatusCode != http.StatusOK {
		t.Fatalf("track: status = %d, want 200", trackResp.StatusCode)
	}
	var track struct {
		Points [][2]float64 `json:"points"`
	}
	if err := json.NewDecoder(trackResp.Body).Decode(&track); err != nil {
		t.Fatal(err)
	}
	if len(track.Points) != 3 || track.Points[0][0] != 50.90 || track.Points[0][1] != 4.40 {
		t.Errorf("track.points = %+v, want the 3 new points starting at [50.90, 4.40]", track.Points)
	}
}

// TestUpdateRoutePointsInvalidatesTheTrackCache proves handleTrack's own
// ETag actually changes when the points do — the whole point of switching
// it from a flat day-long max-age to a validator, once an edit made "the
// points never change" false. A cached ETag from before the edit must not
// match after it.
func TestUpdateRoutePointsInvalidatesTheTrackCache(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	before := h.as("wilant", "cyclists", http.MethodGet, "/api/tracks/"+route.Slug, "")
	beforeETag := before.Header.Get("ETag")
	if beforeETag == "" {
		t.Fatal("no ETag on the track response before the edit")
	}

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+route.Slug+"/points", `{
		"points": [{"lat": 50.90, "lon": 4.40}, {"lat": 50.91, "lon": 4.41}]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit: status = %d, want 200", resp.StatusCode)
	}

	after := h.as("wilant", "cyclists", http.MethodGet, "/api/tracks/"+route.Slug, "")
	afterETag := after.Header.Get("ETag")
	if afterETag == "" {
		t.Fatal("no ETag on the track response after the edit")
	}
	if afterETag == beforeETag {
		t.Error("ETag unchanged after the edit — a stale cached copy would never be invalidated")
	}
}

// TestUpdateRoutePointsRoundTripsPois proves a saved route's own named
// waypoint markers come back from handleTrack exactly as sent, and that
// they don't perturb the track's own point count or geometry.
func TestUpdateRoutePointsRoundTripsPois(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+route.Slug+"/points", `{
		"points": [{"lat": 50.90, "lon": 4.40}, {"lat": 50.91, "lon": 4.41}],
		"pois": [{"lat": 50.905, "lon": 4.405, "name": "Café stop", "type": "food"}]
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	trackResp := h.as("wilant", "cyclists", http.MethodGet, "/api/tracks/"+route.Slug, "")
	if trackResp.StatusCode != http.StatusOK {
		t.Fatalf("track: status = %d, want 200", trackResp.StatusCode)
	}
	var track struct {
		Points [][2]float64 `json:"points"`
		Pois   []struct {
			Lat  float64 `json:"lat"`
			Lon  float64 `json:"lon"`
			Name string  `json:"name"`
			Type string  `json:"type"`
		} `json:"pois"`
	}
	if err := json.NewDecoder(trackResp.Body).Decode(&track); err != nil {
		t.Fatal(err)
	}
	if len(track.Points) != 2 {
		t.Errorf("pointCount = %d, want 2 — a poi must not be mistaken for a track point", len(track.Points))
	}
	if len(track.Pois) != 1 {
		t.Fatalf("pois = %+v, want exactly 1", track.Pois)
	}
	got := track.Pois[0]
	if got.Lat != 50.905 || got.Lon != 4.405 || got.Name != "Café stop" || got.Type != "food" {
		t.Errorf("poi = %+v, want {50.905, 4.405, \"Café stop\", \"food\"}", got)
	}
}

// TestUpdateRoutePointsRejectsUnknownPoiType proves the fixed marker-type
// list is enforced server-side, not just offered as a frontend dropdown.
func TestUpdateRoutePointsRejectsUnknownPoiType(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+route.Slug+"/points", `{
		"points": [{"lat": 50.90, "lon": 4.40}, {"lat": 50.91, "lon": 4.41}],
		"pois": [{"lat": 50.905, "lon": 4.405, "name": "Mystery", "type": "made-up"}]
	}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown poi type", resp.StatusCode)
	}
}

// TestUpdateRoutePointsRejectsTooManyPois proves maxRouteBuilderPois is
// actually enforced, not just documented.
func TestUpdateRoutePointsRejectsTooManyPois(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	pois := "["
	for i := 0; i < 21; i++ {
		if i > 0 {
			pois += ","
		}
		pois += `{"lat": 50.90, "lon": 4.40, "name": "x", "type": "other"}`
	}
	pois += "]"

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+route.Slug+"/points", `{
		"points": [{"lat": 50.90, "lon": 4.40}, {"lat": 50.91, "lon": 4.41}],
		"pois": `+pois+`
	}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for 21 pois (max 20)", resp.StatusCode)
	}
}

// TestUpdateRoutePointsRejectsTooFewPoints proves a single-point "route" is
// rejected the same way handleCreateRouteFromPoints already rejects one —
// gpx.Render's own floor, not a check this handler duplicates.
func TestUpdateRoutePointsRejectsTooFewPoints(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+route.Slug+"/points",
		`{"points": [{"lat": 50.90, "lon": 4.40}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a single-point track", resp.StatusCode)
	}
}

// TestUpdateRoutePointsRequiresOwnership mirrors
// TestRecalculateElevationRequiresOwnership: a rider may not rewrite someone
// else's route, only an admin or the route's own owner.
func TestUpdateRoutePointsRequiresOwnership(t *testing.T) {
	h := newAuthHarness(t, nil)
	theirs := h.seedRoute(t, "Friend's route", "friend")
	body := `{"points": [{"lat": 50.90, "lon": 4.40}, {"lat": 50.91, "lon": 4.41}]}`

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/"+theirs.Slug+"/points", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	resp = h.as("boss", "domestique-admins", http.MethodPut, "/api/routes/"+theirs.Slug+"/points", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", resp.StatusCode)
	}
}

// TestUpdateRoutePointsMissingRoute proves an unknown slug 404s.
func TestUpdateRoutePointsMissingRoute(t *testing.T) {
	h := newAuthHarness(t, nil)

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/routes/nope/points",
		`{"points": [{"lat": 50.90, "lon": 4.40}, {"lat": 50.91, "lon": 4.41}]}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
