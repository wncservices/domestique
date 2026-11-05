package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/testdb"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

// Wahoo rotates refresh tokens, so two callers refreshing the same expired
// session with the same refresh token cannot both succeed: the second is
// refused, and a refresh whose result is not saved loses the rotated token.
// Concurrent callers for one rider must share one refresh.
func TestConcurrentCallersShareOneWahooRefresh(t *testing.T) {
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

	// A token endpoint that rotates: only the current refresh token works,
	// and each success hands back the next one. It is slow enough that a
	// second caller without the lock would certainly overlap the first.
	var mu sync.Mutex
	attempts, current := 0, "rt-1"
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if r.FormValue("refresh_token") != current {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		current = fmt.Sprintf("rt-%d", attempts+1)
		_, _ = fmt.Fprintf(w, `{"access_token":"at-%d","refresh_token":%q,"expires_in":3600}`, attempts, current)
	}))
	t.Cleanup(token.Close)

	client := wahoo.New(wahoo.Config{ClientID: "test-client", ClientSecret: "test-secret", RedirectURL: token.URL + "/cb"})
	client.APIBase = token.URL

	sealed, err := json.Marshal(wahoo.Session{AccessToken: "stale", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := links.Save(wahooProvider, "wilant", providerlink.Connection{Email: "rider@example.test", Secret: string(sealed)}); err != nil {
		t.Fatal(err)
	}

	s := &Server{Wahoo: client, Links: links}
	var wg sync.WaitGroup
	start := make(chan struct{})
	tokens := make([]string, 2)
	errs := make([]error, 2)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = s.wahooAccessToken(context.Background(), "wilant")
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	mu.Lock()
	gotAttempts := attempts
	mu.Unlock()
	if gotAttempts != 1 {
		t.Errorf("token endpoint saw %d refreshes, want exactly 1", gotAttempts)
	}
	for i, tok := range tokens {
		if tok != "at-1" {
			t.Errorf("caller %d got %q, want the one refreshed token", i, tok)
		}
	}
	_, stored, err := links.Secret(wahooProvider, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var session wahoo.Session
	if err := json.Unmarshal([]byte(stored), &session); err != nil {
		t.Fatal(err)
	}
	if session.RefreshToken != "rt-2" {
		t.Errorf("stored refresh token = %q, want the rotated rt-2", session.RefreshToken)
	}
}
