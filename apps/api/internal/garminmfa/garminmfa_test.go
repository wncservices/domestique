package garminmfa

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

// A fixed instant with an explicit zone, so no assertion depends on the
// machine's clock or timezone.
var t0 = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

type harness struct {
	store *Store
	now   time.Time
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func openStore(t *testing.T, dsn string, box *secrets.Box) (*harness, *source.DB) {
	t.Helper()
	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { src.Close() })

	store, err := UseDB(src.Conn(), src.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	if _, err := src.Conn().Exec(`DELETE FROM garmin_mfa_challenges`); err != nil {
		t.Fatal(err)
	}
	h := &harness{store: store, now: t0}
	store.Now = func() time.Time { return h.now }
	return h, src
}

func openSQLite(t *testing.T) (*harness, *source.DB) {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "test.db"), newBox(t))
}

func openPostgres(t *testing.T) (*harness, *source.DB) {
	t.Helper()
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn, newBox(t))
}

func challenge(email string) garmin.MFAChallenge {
	return garmin.MFAChallenge{
		Cookies: map[string][]*http.Cookie{
			"https://sso.garmin.com/sso": {{Name: "GARMIN-SSO", Value: "cookie-secret-value"}},
		},
		CSRF:   "csrf-secret-value",
		Email:  email,
		Method: "email",
	}
}

func rowCount(t *testing.T, src *source.DB) int {
	t.Helper()
	var n int
	if err := src.Conn().QueryRow(`SELECT COUNT(*) FROM garmin_mfa_challenges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) (*harness, *source.DB){
		"sqlite":   openSQLite,
		"postgres": openPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("create then resolve round-trips the challenge", func(t *testing.T) {
				h, _ := open(t)
				token, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if err != nil {
					t.Fatal(err)
				}
				if token == "" {
					t.Fatal("empty token")
				}
				got, err := h.store.Resolve(t.Context(), "wilant", token)
				if err != nil {
					t.Fatal(err)
				}
				if got.CSRF != "csrf-secret-value" || got.Email != "w@example.com" || got.Method != "email" {
					t.Errorf("challenge = %+v", got)
				}
				if c := got.Cookies["https://sso.garmin.com/sso"]; len(c) != 1 || c[0].Value != "cookie-secret-value" {
					t.Errorf("cookies = %+v", got.Cookies)
				}
			})

			t.Run("only a hash of the token and a sealed state are stored", func(t *testing.T) {
				h, src := open(t)
				token, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if err != nil {
					t.Fatal(err)
				}
				var stored string
				var state []byte
				if err := src.Conn().QueryRow(`SELECT token, state FROM garmin_mfa_challenges`).Scan(&stored, &state); err != nil {
					t.Fatal(err)
				}
				if stored == token {
					t.Error("the bearer token itself is in the database")
				}
				for _, secret := range []string{"cookie-secret-value", "csrf-secret-value"} {
					if bytes.Contains(state, []byte(secret)) {
						t.Errorf("state holds %q in clear", secret)
					}
				}
			})

			t.Run("unknown token and another rider are both not found", func(t *testing.T) {
				h, _ := open(t)
				token, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.Resolve(t.Context(), "wilant", "nope"); !errors.Is(err, ErrNotFound) {
					t.Errorf("unknown token: %v, want ErrNotFound", err)
				}
				if _, err := h.store.Resolve(t.Context(), "friend", token); !errors.Is(err, ErrNotFound) {
					t.Errorf("another rider: %v, want ErrNotFound", err)
				}
				// The stranger's probe must not have burned the owner's challenge.
				if _, err := h.store.Resolve(t.Context(), "wilant", token); err != nil {
					t.Errorf("the owner lost the challenge to a stranger's probe: %v", err)
				}
			})

			t.Run("resolve does not delete, consume does", func(t *testing.T) {
				h, src := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				for range 3 {
					if _, err := h.store.Resolve(t.Context(), "wilant", token); err != nil {
						t.Fatal(err)
					}
				}
				if rowCount(t, src) != 1 {
					t.Fatal("Resolve deleted the row")
				}
				if err := h.store.Consume(t.Context(), "wilant", token); err != nil {
					t.Fatal(err)
				}
				if rowCount(t, src) != 0 {
					t.Error("Consume left the row behind")
				}
				if _, err := h.store.Resolve(t.Context(), "wilant", token); !errors.Is(err, ErrNotFound) {
					t.Errorf("a consumed challenge resolved again: %v", err)
				}
			})

			t.Run("an expired challenge is expired, and gone", func(t *testing.T) {
				h, src := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)

				h.advance(DefaultTTL - time.Second)
				if _, err := h.store.Resolve(t.Context(), "wilant", token); err != nil {
					t.Fatalf("just before expiry: %v", err)
				}
				h.advance(2 * time.Second)
				if _, err := h.store.Resolve(t.Context(), "wilant", token); !errors.Is(err, ErrExpired) {
					t.Fatalf("after expiry: %v, want ErrExpired", err)
				}
				if rowCount(t, src) != 0 {
					t.Error("the expired row is still there and could be replayed")
				}
			})

			t.Run("expired is not disclosed to another rider", func(t *testing.T) {
				h, _ := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				h.advance(time.Hour)
				if _, err := h.store.Resolve(t.Context(), "friend", token); !errors.Is(err, ErrNotFound) {
					t.Errorf("error = %v, want ErrNotFound for a stranger", err)
				}
			})

			t.Run("reserve counts atomically up to the cap, then refuses", func(t *testing.T) {
				h, src := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				for want := 1; want <= MaxAttempts; want++ {
					n, err := h.store.Reserve(t.Context(), "wilant", token)
					if err != nil || n != want {
						t.Fatalf("reserve %d: n=%d err=%v", want, n, err)
					}
				}
				if _, err := h.store.Reserve(t.Context(), "wilant", token); !errors.Is(err, ErrAttemptsExhausted) {
					t.Fatalf("sixth reserve: %v, want ErrAttemptsExhausted", err)
				}
				// Refusing does not delete: the attempts in flight may yet be
				// refunded. Finishing the challenge is Consume's job.
				if rowCount(t, src) != 1 {
					t.Error("a refused reserve deleted the row")
				}
				if err := h.store.Consume(t.Context(), "wilant", token); err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.Reserve(t.Context(), "wilant", token); !errors.Is(err, ErrNotFound) {
					t.Errorf("reserve after consume: %v, want ErrNotFound", err)
				}
			})

			t.Run("reserve is the owners only and a stranger costs nothing", func(t *testing.T) {
				h, _ := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if _, err := h.store.Reserve(t.Context(), "friend", token); !errors.Is(err, ErrNotFound) {
					t.Errorf("stranger reserve: %v, want ErrNotFound", err)
				}
				if n, err := h.store.Reserve(t.Context(), "wilant", token); err != nil || n != 1 {
					t.Errorf("owner's first reserve: n=%d err=%v, want 1", n, err)
				}
			})

			t.Run("refund gives an attempt back and never goes below zero", func(t *testing.T) {
				h, _ := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				for range MaxAttempts {
					if _, err := h.store.Reserve(t.Context(), "wilant", token); err != nil {
						t.Fatal(err)
					}
				}
				if err := h.store.Refund(t.Context(), "wilant", token); err != nil {
					t.Fatal(err)
				}
				if n, err := h.store.Reserve(t.Context(), "wilant", token); err != nil || n != MaxAttempts {
					t.Errorf("reserve after refund: n=%d err=%v, want %d", n, err, MaxAttempts)
				}

				fresh, _ := h.store.Create(t.Context(), "friend", challenge("f@example.com"), DefaultTTL)
				if err := h.store.Refund(t.Context(), "friend", fresh); err != nil {
					t.Fatal(err)
				}
				if n, _ := h.store.Reserve(t.Context(), "friend", fresh); n != 1 {
					t.Errorf("a refund on zero attempts went negative: next reserve = %d, want 1", n)
				}
				// A stranger cannot refund the owner's attempts back.
				if err := h.store.Refund(t.Context(), "friend", token); err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.Reserve(t.Context(), "wilant", token); !errors.Is(err, ErrAttemptsExhausted) {
					t.Errorf("a stranger's refund freed an attempt: %v", err)
				}
			})

			t.Run("concurrent reserves never exceed the cap", func(t *testing.T) {
				h, _ := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)

				const workers = 20
				results := make(chan error, workers)
				for range workers {
					go func() {
						_, err := h.store.Reserve(t.Context(), "wilant", token)
						results <- err
					}()
				}
				granted := 0
				for range workers {
					switch err := <-results; {
					case err == nil:
						granted++
					case errors.Is(err, ErrAttemptsExhausted):
					default:
						t.Errorf("unexpected error: %v", err)
					}
				}
				if granted != MaxAttempts {
					t.Errorf("%d reserves granted, want exactly %d", granted, MaxAttempts)
				}
			})

			t.Run("update re-seals the state in place", func(t *testing.T) {
				h, src := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if _, err := h.store.Reserve(t.Context(), "wilant", token); err != nil {
					t.Fatal(err)
				}

				next := challenge("w@example.com")
				next.CSRF = "csrf-rotated"
				next.Cookies["https://sso.garmin.com/sso"][0].Value = "cookie-rotated"
				if err := h.store.Update(t.Context(), "wilant", token, next); err != nil {
					t.Fatal(err)
				}

				got, err := h.store.Resolve(t.Context(), "wilant", token)
				if err != nil {
					t.Fatal(err)
				}
				if got.CSRF != "csrf-rotated" || got.Cookies["https://sso.garmin.com/sso"][0].Value != "cookie-rotated" {
					t.Errorf("resolved %+v, want the updated state", got)
				}
				if rowCount(t, src) != 1 {
					t.Error("update created a second row")
				}
				// Neither the attempts spent nor the expiry moved.
				if n, _ := h.store.Reserve(t.Context(), "wilant", token); n != 2 {
					t.Errorf("attempts after update = %d, want 2 kept", n)
				}
				h.advance(DefaultTTL + time.Second)
				if _, err := h.store.Resolve(t.Context(), "wilant", token); !errors.Is(err, ErrExpired) {
					t.Errorf("update extended the life of a challenge: %v", err)
				}
			})

			t.Run("update refuses a stranger, the unknown, and the oversized", func(t *testing.T) {
				h, _ := open(t)
				token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)

				if err := h.store.Update(t.Context(), "friend", token, challenge("x@example.com")); !errors.Is(err, ErrNotFound) {
					t.Errorf("stranger update: %v, want ErrNotFound", err)
				}
				if err := h.store.Update(t.Context(), "wilant", "nope", challenge("x@example.com")); !errors.Is(err, ErrNotFound) {
					t.Errorf("unknown update: %v, want ErrNotFound", err)
				}
				big := challenge("w@example.com")
				big.Cookies["https://sso.garmin.com/sso"][0].Value = strings.Repeat("x", MaxChallengeBytes)
				if err := h.store.Update(t.Context(), "wilant", token, big); !errors.Is(err, ErrTooLarge) {
					t.Errorf("oversized update: %v, want ErrTooLarge", err)
				}
				got, err := h.store.Resolve(t.Context(), "wilant", token)
				if err != nil || got.Email != "w@example.com" {
					t.Errorf("a refused update disturbed the row: %+v %v", got, err)
				}
			})

			t.Run("a fourth live challenge evicts the riders oldest", func(t *testing.T) {
				h, _ := open(t)
				var tokens []string
				for range 3 {
					tok, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
					if err != nil {
						t.Fatal(err)
					}
					tokens = append(tokens, tok)
					h.advance(time.Second)
				}
				other, _ := h.store.Create(t.Context(), "friend", challenge("f@example.com"), DefaultTTL)
				h.advance(time.Second)

				fourth, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.Resolve(t.Context(), "wilant", tokens[0]); !errors.Is(err, ErrNotFound) {
					t.Errorf("the oldest survived: %v", err)
				}
				for _, tok := range append(tokens[1:], fourth) {
					if _, err := h.store.Resolve(t.Context(), "wilant", tok); err != nil {
						t.Errorf("a newer challenge was evicted: %v", err)
					}
				}
				if _, err := h.store.Resolve(t.Context(), "friend", other); err != nil {
					t.Errorf("another rider's challenge was evicted: %v", err)
				}
			})

			t.Run("expired rows are pruned on create and do not count as live", func(t *testing.T) {
				h, src := open(t)
				for range 3 {
					if _, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), time.Minute); err != nil {
						t.Fatal(err)
					}
					h.advance(time.Second)
				}
				h.advance(time.Hour)
				fresh, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
				if err != nil {
					t.Fatal(err)
				}
				if rowCount(t, src) != 1 {
					t.Errorf("rows = %d, want only the fresh one", rowCount(t, src))
				}
				if _, err := h.store.Resolve(t.Context(), "wilant", fresh); err != nil {
					t.Error(err)
				}
			})

			t.Run("an oversized challenge is refused before sealing", func(t *testing.T) {
				h, src := open(t)
				big := challenge("w@example.com")
				big.Cookies["https://sso.garmin.com/sso"][0].Value = strings.Repeat("x", MaxChallengeBytes)
				if _, err := h.store.Create(t.Context(), "wilant", big, DefaultTTL); !errors.Is(err, ErrTooLarge) {
					t.Errorf("error = %v, want ErrTooLarge", err)
				}
				if rowCount(t, src) != 0 {
					t.Error("an oversized challenge was stored")
				}
			})

			t.Run("UseDB is idempotent", func(t *testing.T) {
				h, src := open(t)
				if _, err := UseDB(src.Conn(), src.DSN(), newBox(t)); err != nil {
					t.Errorf("second UseDB: %v", err)
				}
				_ = h
			})
		})
	}
}

func TestWithoutAKeyNothingIsStored(t *testing.T) {
	h, _ := openStore(t, filepath.Join(t.TempDir(), "test.db"), nil)
	if h.store.CanStore() {
		t.Error("CanStore is true without a key")
	}
	if _, err := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL); !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("error = %v, want secrets.ErrNoKey", err)
	}
}

func TestCanStoreIsNilSafe(t *testing.T) {
	var s *Store
	if s.CanStore() {
		t.Error("a nil store claims it can store")
	}
}

func TestCreateRejectsNoRider(t *testing.T) {
	h, _ := openSQLite(t)
	if _, err := h.store.Create(t.Context(), " ", challenge("w@example.com"), DefaultTTL); err == nil {
		t.Error("a challenge for nobody was created")
	}
}

func TestATamperedRowIsNotFound(t *testing.T) {
	h, src := openSQLite(t)
	token, _ := h.store.Create(t.Context(), "wilant", challenge("w@example.com"), DefaultTTL)
	if _, err := src.Conn().Exec(`UPDATE garmin_mfa_challenges SET state = ?`, []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Resolve(t.Context(), "wilant", token); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}
