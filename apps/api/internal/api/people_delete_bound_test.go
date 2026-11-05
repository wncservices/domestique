package api_test

import (
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/calendarfeed"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// An account bound to a rider whose name the People page never guessed
// ("wilant-2" for "auth0|wilant") must still have that rider's data purged
// when the admin deletes the person without naming a rider. The binding is
// the only link between the two, and it is removed at the end of the delete,
// so it has to be read first.
func TestDeletingAPersonPurgesTheRiderTheirBindingNames(t *testing.T) {
	h := newBindingHarness(t, person("auth0|wilant", "Wilant", "wilant@example.com", true))
	bindTo(t, h.ssoHarness, "auth0|wilant", "wilant-2", "wilant@example.com")

	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Name: "Bound Rider's Loop", UploadedBy: "wilant-2",
		GPX: []byte(`<gpx version="1.1"><trk><trkseg><trkpt lat="50" lon="3"/><trkpt lat="50.001" lon="3.001"/></trkseg></trk></gpx>`),
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	// The SSO harness leaves Source unset, which would skip the route purge.
	h.srv.Source = h.db
	feeds, err := calendarfeed.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.CalendarFeeds = feeds
	token, err := feeds.Regenerate(t.Context(), "wilant-2")
	if err != nil {
		t.Fatal(err)
	}
	if rider, err := feeds.Lookup(t.Context(), token); err != nil || rider != "wilant-2" {
		t.Fatalf("setup: feed token resolves to %q, %v", rider, err)
	}

	resp := h.admin.do(http.MethodDelete, peoplePath("auth0|wilant", ""), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE person = %d: %v", resp.StatusCode, jsonBody(t, resp))
	}

	routes, _, err := h.db.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Name == "Bound Rider's Loop" {
			t.Errorf("the bound rider's route survived deleting their person")
		}
	}
	if _, err := feeds.Lookup(t.Context(), token); err == nil {
		t.Errorf("the bound rider's calendar feed still resolves after deleting their person")
	}
	if _, ok := h.binding("auth0|wilant"); ok {
		t.Errorf("the binding of the deleted person was not removed")
	}
}

// A person with no binding has only a name guess ("Robin" reads as robin),
// and two people can share it. Deleting that person without naming a rider
// must not purge robin, who may well be somebody else.
func TestDeletingAnUnboundPersonNeverPurgesTheirGuessedRider(t *testing.T) {
	h := newBindingHarness(t, person("auth0|robin-two", "Robin", "robin2@example.com", true))
	if _, err := h.db.Create(t.Context(), source.CreateRequest{
		Name: "Robin's Loop", UploadedBy: "robin",
		GPX: []byte(`<gpx version="1.1"><trk><trkseg><trkpt lat="50" lon="3"/><trkpt lat="50.001" lon="3.001"/></trkseg></trk></gpx>`),
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	h.srv.Source = h.db

	resp := h.admin.do(http.MethodDelete, peoplePath("auth0|robin-two", ""), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE person = %d: %v", resp.StatusCode, jsonBody(t, resp))
	}
	routes, _, err := h.db.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Name == "Robin's Loop" {
			return
		}
	}
	t.Error("deleting an unbound person purged the rider their name only suggested")
}
