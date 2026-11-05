package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The route's name, description and tags are free text a rider types, and
// they reach every head unit and the library grid. They are capped on upload
// and on edit alike, so an over-long value is refused with a 400 and stores
// nothing. The caps are source.ValidateText's: 200 characters for the name,
// 5000 for the description, 30 tags of at most 50 characters each. Names are
// counted in characters, not bytes, so a multi-byte name reaches the same
// cap as an ASCII one.

func repeatRune(r string, n int) string { return strings.Repeat(r, n) }

func tagList(n int, each string) string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = fmt.Sprintf("%s%d", each, i)
	}
	return strings.Join(tags, ",")
}

func errorOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body["error"]
}

func TestUploadRefusesOverLongRouteText(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name   string
		fields map[string]string
	}{
		{"name over 200", map[string]string{"name": repeatRune("a", 201)}},
		{"name over 200 characters, not bytes", map[string]string{"name": repeatRune("é", 201)}},
		{"description over 5000", map[string]string{"name": "Long desc", "description": repeatRune("d", 5001)}},
		{"more than 30 tags", map[string]string{"name": "Many tags", "tags": tagList(31, "t")}},
		{"tag over 50", map[string]string{"name": "Long tag", "tags": repeatRune("x", 51)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := h.upload(c.fields, exampleGPX(t), "cap.gpx")
			h.expectStatus(resp, http.StatusBadRequest)
			if msg := errorOf(t, resp); !strings.Contains(msg, "max ") {
				t.Errorf("error = %q, want the cap named", msg)
			}
		})
	}
}

func TestUploadAcceptsRouteTextAtTheLimits(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name   string
		fields map[string]string
	}{
		{"name of exactly 200 characters", map[string]string{"name": repeatRune("é", 100) + repeatRune("a", 100)}},
		{"description of exactly 5000", map[string]string{"name": "Limit desc", "description": repeatRune("d", 5000)}},
		{"exactly 30 tags", map[string]string{"name": "Limit tags", "tags": tagList(30, "t")}},
		{"tag of exactly 50", map[string]string{"name": "Limit tag", "tags": repeatRune("x", 50)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.expectStatus(h.upload(c.fields, exampleGPX(t), "limit.gpx"), http.StatusCreated)
		})
	}
}

func TestUpdateRefusesOverLongRouteText(t *testing.T) {
	h := newHarness(t)
	route := h.uploadExample("Editable Route")
	path := "/api/routes/" + route.Slug

	cases := []struct {
		name string
		body string
	}{
		{"name over 200", fmt.Sprintf(`{"name":%q}`, repeatRune("a", 201))},
		{"description over 5000", fmt.Sprintf(`{"description":%q}`, repeatRune("d", 5001))},
		{"more than 30 tags", fmt.Sprintf(`{"tags":[%s]}`, quotedTags(31, "t"))},
		{"tag over 50", fmt.Sprintf(`{"tags":[%q]}`, repeatRune("x", 51))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := h.do(http.MethodPatch, path, strings.NewReader(c.body), "application/json")
			h.expectStatus(resp, http.StatusBadRequest)
		})
	}

	// The refused edits stored nothing: the name is still the one uploaded.
	routes, _, err := h.source.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range routes {
		if got.Slug == route.Slug && (got.Name != "Editable Route" || got.Description != "" || len(got.Tags) != 0) {
			t.Errorf("route = %q / %d-char description / %d tags after refused edits, want untouched",
				got.Name, len(got.Description), len(got.Tags))
		}
	}
}

func TestUpdateAcceptsRouteTextAtTheLimits(t *testing.T) {
	h := newHarness(t)
	route := h.uploadExample("Editable Limits")
	path := "/api/routes/" + route.Slug

	cases := []struct {
		name string
		body string
	}{
		{"name of exactly 200", fmt.Sprintf(`{"name":%q}`, repeatRune("a", 200))},
		{"description of exactly 5000", fmt.Sprintf(`{"description":%q}`, repeatRune("d", 5000))},
		{"exactly 30 tags", fmt.Sprintf(`{"tags":[%s]}`, quotedTags(30, "t"))},
		{"tag of exactly 50", fmt.Sprintf(`{"tags":[%q]}`, repeatRune("x", 50))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.expectStatus(h.do(http.MethodPatch, path, strings.NewReader(c.body), "application/json"), http.StatusOK)
		})
	}
}

func quotedTags(n int, each string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("%q", fmt.Sprintf("%s%d", each, i))
	}
	return strings.Join(parts, ",")
}

// A route saved from the route builder is rider text too: an over-long name is
// a 400 with nothing stored. Create clips imported text, so this handler must
// refuse on its own.
func TestRouteBuilderSaveRefusesOverLongRouteText(t *testing.T) {
	client, base := newRouteBuilderHarness(t, nil)

	resp := doJSON(t, client, http.MethodPost, base+"/api/routes/from-points", map[string]any{
		"name": repeatRune("a", 201),
		"points": []map[string]float64{
			{"lat": 50.85, "lon": 4.35},
			{"lat": 50.86, "lon": 4.36},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a 201-character name", resp.StatusCode)
	}
}
