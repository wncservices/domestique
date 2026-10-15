package api

import (
	"github.com/wncservices/domestique/apps/api/internal/testdb"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// metricValue reads one sample from a scrape: the line starting with prefix.
func metricValue(t *testing.T, out, prefix string) (float64, bool) {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(l, prefix)), 64)
			if err != nil {
				t.Fatalf("unparseable sample %q", l)
			}
			return f, true
		}
	}
	return 0, false
}

// The per-provider error counter and success timestamp cannot say that one
// account is stuck while another keeps succeeding: the second one's successes
// refresh the shared timestamp. A count of accounts whose latest attempt failed
// can, and names nobody.
func TestPushAccountsFailingCountsAccountsNotProviders(t *testing.T) {
	s := &Server{}
	const series = `domestique_push_accounts_failing{provider="wahoo"} `
	base, _ := metricValue(t, scrape(t), series)

	s.recordPushResult(model.PlanItem{Op: model.OpCreate, AccountID: "wahoo:stuck-rider"}, http.ErrBodyNotAllowed)
	s.recordPushResult(model.PlanItem{Op: model.OpCreate, AccountID: "wahoo:fine-rider"}, nil)
	out := scrape(t)
	if got, ok := metricValue(t, out, series); !ok || got != base+1 {
		t.Fatalf("one failing, one fine: got %v (found=%v), want %v\n%s", got, ok, base+1, grepLines(out, "accounts_failing"))
	}
	if strings.Contains(out, "stuck-rider") || strings.Contains(out, "fine-rider") {
		t.Fatal("an account id is in the metrics")
	}

	// The same account failing again is still one account.
	s.recordPushResult(model.PlanItem{Op: model.OpUpdate, AccountID: "wahoo:stuck-rider"}, http.ErrBodyNotAllowed)
	if got, _ := metricValue(t, scrape(t), series); got != base+1 {
		t.Fatalf("a repeat failure counted twice: %v, want %v", got, base+1)
	}

	// A later success clears it, even though the other account's success came first.
	s.recordPushResult(model.PlanItem{Op: model.OpUpdate, AccountID: "wahoo:stuck-rider"}, nil)
	if got, _ := metricValue(t, scrape(t), series); got != base {
		t.Fatalf("after recovery: %v, want %v", got, base)
	}
}

// A noop is not an attempt.
func TestPushAccountsFailingIgnoresNoops(t *testing.T) {
	s := &Server{}
	const series = `domestique_push_accounts_failing{provider="garmin"} `
	base, _ := metricValue(t, scrape(t), series)
	s.recordPushResult(model.PlanItem{Op: model.OpNoop, AccountID: "garmin:noop-rider"}, http.ErrBodyNotAllowed)
	if got, _ := metricValue(t, scrape(t), series); got != base {
		t.Fatalf("a noop changed the count: %v, want %v", got, base)
	}
}

func garminLinks(t *testing.T) *providerlink.Store {
	t.Helper()
	db, err := source.OpenDB(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	linksDB = db
	return links
}

// linksDB lets a test age a session: Save always stamps "now".
var linksDB *source.DB

func ageSession(t *testing.T, rider string, by time.Duration) {
	t.Helper()
	old := time.Now().Add(-by).UTC().Format(time.RFC3339)
	if _, err := linksDB.Conn().Exec(`UPDATE provider_links SET updated_at = '` + old + `' WHERE rider = '` + rider + `'`); err != nil {
		t.Fatal(err)
	}
}

// The expiry gauge is the earliest expiry among sessions still good, so it
// never reads "already past" because of one dead session, and a separate gauge
// counts the dead ones. With nobody connected both read zero rather than
// keeping the last rider's value.
func TestGarminExpiryGaugesSeparateExpiredFromEarliest(t *testing.T) {
	links := garminLinks(t)
	s := &Server{Links: links}
	for _, rider := range []string{"gx-old", "gx-fresh", "gx-fresher"} {
		if _, err := links.Save("garmin", rider, providerlink.Connection{Email: rider + "@example.com", Secret: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	// A Garmin session lasts about a year; age two of them past it and one
	// partway.
	ageSession(t, "gx-old", 400*24*time.Hour)
	ageSession(t, "gx-fresh", 100*24*time.Hour)

	s.checkGarminExpiry(t.Context())
	out := scrape(t)

	if got, ok := metricValue(t, out, "domestique_garmin_sessions_expired "); !ok || got != 1 {
		t.Fatalf("expired sessions = %v (found=%v), want 1\n%s", got, ok, grepLines(out, "garmin_sessions"))
	}
	earliest, ok := metricValue(t, out, "domestique_garmin_session_expiry_timestamp_seconds ")
	if !ok || earliest < float64(time.Now().Unix()) {
		t.Fatalf("earliest expiry = %v (found=%v), want a time in the future", earliest, ok)
	}
	// It is gx-fresh's, the older of the two that still work: roughly 265 days out.
	if days := (earliest - float64(time.Now().Unix())) / 86400; days > 300 || days < 200 {
		t.Fatalf("earliest expiry is %.0f days out, want gx-fresh's (about 265)", days)
	}

	// Everyone leaves.
	for _, rider := range []string{"gx-old", "gx-fresh", "gx-fresher"} {
		if err := links.Delete("garmin", rider); err != nil {
			t.Fatal(err)
		}
	}
	s.checkGarminExpiry(t.Context())
	out = scrape(t)
	if got, _ := metricValue(t, out, "domestique_garmin_sessions_expired "); got != 0 {
		t.Errorf("expired with nobody connected = %v, want 0", got)
	}
	if got, ok := metricValue(t, out, "domestique_garmin_session_expiry_timestamp_seconds "); ok && got != 0 {
		t.Errorf("earliest expiry with nobody connected = %v, want it dropped or 0", got)
	}
}
