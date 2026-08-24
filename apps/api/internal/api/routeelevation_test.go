package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/elevation"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// allZeroElevationGPX mirrors the fixture in
// internal/source/elevation_backfill_test.go — same placeholder-zero shape
// (afstandmeten.nl-style), redeclared here rather than exported across
// packages for one test fixture.
const allZeroElevationGPX = `<?xml version="1.0"?>
<gpx version="1.1"><trk><trkseg>
<trkpt lat="50.8000" lon="4.7000"><ele>0.00000</ele></trkpt>
<trkpt lat="50.8010" lon="4.7000"><ele>0.00000</ele></trkpt>
<trkpt lat="50.8020" lon="4.7000"><ele>0.00000</ele></trkpt>
<trkpt lat="50.8030" lon="4.7000"><ele>0.00000</ele></trkpt>
</trkseg></trk></gpx>`

// fakeElevationServer returns a climbing profile — elevation rises with
// latitude — so a test can assert a specific, nonzero ascent came from the
// backfill rather than merely "some number, who knows." Same shape as the
// source package's own copy; not shared across packages for one fixture.
func fakeElevationServer(t *testing.T) *elevation.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Locations []struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"locations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		results := make([]struct {
			Elevation float64 `json:"elevation"`
		}, len(body.Locations))
		for i, loc := range body.Locations {
			results[i].Elevation = (loc.Latitude - 50.800) * 10000
		}
		_ = json.NewEncoder(w).Encode(struct {
			Results []struct {
				Elevation float64 `json:"elevation"`
			} `json:"results"`
		}{Results: results})
	}))
	t.Cleanup(srv.Close)
	return elevation.New(srv.URL)
}

// TestRecalculateElevationRequiresConfiguration proves the endpoint tells
// the caller plainly when this deployment has no elevation lookup wired up
// at all, rather than returning 200 having silently done nothing — the
// harness's *source.DB never gets SetElevationClient called on it, the
// same as any deployment with elevation.enabled left off.
func TestRecalculateElevationRequiresConfiguration(t *testing.T) {
	h := newAuthHarness(t, nil)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+route.Slug+"/recalculate-elevation", "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
}

// TestRecalculateElevationFixesAPlaceholderRoute proves the actual point of
// the feature: a route uploaded with placeholder-zero elevation (before
// this deployment had elevation lookup configured, or while it was
// temporarily down) gets a real ascent once the caller asks for it again,
// without needing to re-upload the GPX.
func TestRecalculateElevationFixesAPlaceholderRoute(t *testing.T) {
	h := newAuthHarness(t, nil)

	// Created with no elevation client configured yet — the same state a
	// route uploaded before this deployment turned elevation lookup on
	// would be in. If the client were wired up before Create, Create's own
	// backfill would already fix it, defeating this test's premise.
	route, err := h.src.Create(t.Context(), source.CreateRequest{
		Name: "Night Run", GPX: []byte(allZeroElevationGPX), UploadedBy: "wilant",
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.Stats.AscentM != 0 {
		t.Fatalf("ascent before = %.1f, want 0 — this test's premise is that it starts unbackfilled", route.Stats.AscentM)
	}

	h.src.SetElevationClient(fakeElevationServer(t))
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+route.Slug+"/recalculate-elevation", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out routeDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.AscentM < 29 || out.AscentM > 31 {
		t.Errorf("ascent after = %.2f, want ~30 m from the backfilled elevation", out.AscentM)
	}
}

// TestRecalculateElevationOnAnAlreadyRealRouteIsANoOp proves it's safe to
// call on a route that already has real elevation — the point of this
// action is "fix the ones that need it," not "always re-derive elevation
// from a terrain API even for a device's own genuine reading."
func TestRecalculateElevationOnAnAlreadyRealRouteIsANoOp(t *testing.T) {
	h := newAuthHarness(t, nil)
	h.src.SetElevationClient(fakeElevationServer(t))
	route := h.seedRoute(t, "Hill Loop", "wilant") // seedGPX has real, non-zero elevation

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+route.Slug+"/recalculate-elevation", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out routeDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.AscentM != route.Stats.AscentM {
		t.Errorf("ascent = %.2f, want unchanged from %.2f — the file's own real elevation should never be overwritten", out.AscentM, route.Stats.AscentM)
	}
}

// TestRecalculateElevationRequiresOwnership mirrors TestRouteOwnership: a
// rider may not trigger this on someone else's route, only an admin or the
// route's own owner.
func TestRecalculateElevationRequiresOwnership(t *testing.T) {
	h := newAuthHarness(t, nil)
	h.src.SetElevationClient(fakeElevationServer(t))
	theirs := h.seedRoute(t, "Friend's route", "friend")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+theirs.Slug+"/recalculate-elevation", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	resp = h.as("boss", "domestique-admins", http.MethodPost, "/api/routes/"+theirs.Slug+"/recalculate-elevation", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", resp.StatusCode)
	}
}

// TestRecalculateElevationMissingRoute proves an unknown slug 404s rather
// than the 412 a missing elevation client would otherwise mask it behind —
// existence is checked regardless of configuration.
func TestRecalculateElevationMissingRoute(t *testing.T) {
	h := newAuthHarness(t, nil)
	h.src.SetElevationClient(fakeElevationServer(t))

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/nope/recalculate-elevation", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
