package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

const rideStartBody = `{"place":"12 Kerkstraat, Ghent, East Flanders, Belgium","lat":51.054321,"lon":3.719876}`

func TestRideStartIsStoredRoundedAndNeverReturned(t *testing.T) {
	h := newWRHarness(t)

	resp := h.as("wilant", http.MethodPut, "/api/training/ride-start", rideStartBody)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204: %s", resp.StatusCode, h.body(resp))
	}

	p, ok, err := h.starts.Get(context.Background(), "wilant")
	if err != nil || !ok {
		t.Fatalf("stored start: ok=%v err=%v", ok, err)
	}
	if p.Lat != 51.054 || p.Lon != 3.72 {
		t.Errorf("stored %v, %v, want the three-decimal values 51.054, 3.72", p.Lat, p.Lon)
	}

	get := h.as("wilant", http.MethodGet, "/api/training/ride-start", "")
	raw := h.body(get)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d: %s", get.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out["set"] != true || out["place"] != "Ghent, Belgium" {
		t.Errorf("GET = %s, want set and the reduced town", raw)
	}
	for k := range out {
		if k != "set" && k != "place" {
			t.Errorf("GET carries %q: the response is {set, place} only", k)
		}
	}
	for _, leak := range []string{"51.05", "3.71", "3.72", "lat", "lon", "Kerkstraat"} {
		if strings.Contains(raw, leak) {
			t.Errorf("GET body %s leaks %q", raw, leak)
		}
	}
}

func TestRideStartPutAnswersWithNoBody(t *testing.T) {
	h := newWRHarness(t)
	resp := h.as("wilant", http.MethodPut, "/api/training/ride-start", rideStartBody)
	if raw := h.body(resp); raw != "" {
		t.Errorf("PUT body = %q, want empty", raw)
	}
}

func TestRideStartWhenNotSetSaysSo(t *testing.T) {
	h := newWRHarness(t)
	resp := h.as("wilant", http.MethodGet, "/api/training/ride-start", "")
	var out struct {
		Set   bool   `json:"set"`
		Place string `json:"place"`
	}
	if err := json.Unmarshal([]byte(h.body(resp)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Set || out.Place != "" {
		t.Errorf("GET before any PUT = %+v, want not set", out)
	}
}

func TestRideStartDeleteIsTheOptOut(t *testing.T) {
	h := newWRHarness(t)
	h.as("wilant", http.MethodPut, "/api/training/ride-start", rideStartBody)
	if resp := h.as("wilant", http.MethodDelete, "/api/training/ride-start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}
	if _, ok, _ := h.starts.Get(context.Background(), "wilant"); ok {
		t.Error("start survived DELETE")
	}
	if resp := h.as("wilant", http.MethodDelete, "/api/training/ride-start", ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204", resp.StatusCode)
	}
}

// The rider is the session, never the body: a body naming someone else is not
// read, and one rider's start never answers for another.
func TestRideStartBelongsToTheSessionRider(t *testing.T) {
	h := newWRHarness(t)
	h.as("wilant", http.MethodPut, "/api/training/ride-start",
		`{"rider":"someone-else","place":"Ghent, Belgium","lat":51.05,"lon":3.72}`)
	if _, ok, _ := h.starts.Get(context.Background(), "someone-else"); ok {
		t.Error("the body's rider was used")
	}
	if _, ok, _ := h.starts.Get(context.Background(), "wilant"); !ok {
		t.Error("the session rider has no start")
	}
	resp := h.as("marie", http.MethodGet, "/api/training/ride-start", "")
	var out struct{ Set bool }
	_ = json.Unmarshal([]byte(h.body(resp)), &out)
	if out.Set {
		t.Error("another rider's start was visible")
	}
}

func TestRideStartRejectsBadInput(t *testing.T) {
	h := newWRHarness(t)
	for name, body := range map[string]string{
		"not json":       `nope`,
		"no coordinates": `{"place":"Ghent, Belgium"}`,
		"out of range":   `{"place":"Ghent, Belgium","lat":95,"lon":3}`,
		"only a street":  `{"place":"12 Kerkstraat","lat":51,"lon":3}`,
	} {
		if resp := h.as("wilant", http.MethodPut, "/api/training/ride-start", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if _, ok, _ := h.starts.Get(context.Background(), "wilant"); ok {
		t.Error("a rejected PUT left a start")
	}
}

func TestRideStartIsForRidersNotViewers(t *testing.T) {
	h := newWRHarness(t)
	if resp := h.asGroup("guest", "guests", http.MethodPut, "/api/training/ride-start", rideStartBody); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer PUT status = %d, want 403", resp.StatusCode)
	}
}

func TestRideStartWithoutAStoreIs412AndLogsAWarning(t *testing.T) {
	h := newWRHarness(t, func(_ *wrHarness, srv *api.Server) { srv.RideStarts = nil })
	resp := h.as("wilant", http.MethodGet, "/api/training/ride-start", "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "ride start") {
		t.Errorf("no Warn for the missing store; logs:\n%s", logs)
	}
}

func TestRideStartLogsCarryNoPlaceOrCoordinate(t *testing.T) {
	h := newWRHarness(t)
	h.as("wilant", http.MethodPut, "/api/training/ride-start", rideStartBody)
	h.as("wilant", http.MethodDelete, "/api/training/ride-start", "")
	logs := h.logs.String()
	for _, leak := range []string{"Ghent", "Kerkstraat", "51.05", "3.71", "3.72"} {
		if strings.Contains(logs, leak) {
			t.Errorf("logs contain %q:\n%s", leak, logs)
		}
	}
}
