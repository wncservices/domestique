package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestRoundTrip(t *testing.T) {
	box := testBox(t)

	const secret = "komoot-session-token-1234"
	sealed, err := box.Seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(secret)) {
		t.Fatal("the plaintext is present in the ciphertext")
	}

	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if opened != secret {
		t.Errorf("opened = %q, want %q", opened, secret)
	}
}

// Sealing the same value twice must not produce the same bytes, or the
// database reveals which riders hold the same credential.
func TestSealIsNotDeterministic(t *testing.T) {
	box := testBox(t)

	first, err := box.Seal("same")
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.Seal("same")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Error("two seals of the same value are identical; the nonce is not random")
	}
}

func TestOpenRejectsAnotherKey(t *testing.T) {
	sealed, err := testBox(t).Seal("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testBox(t).Open(sealed); err == nil {
		t.Error("a different key opened the value")
	}
}

// GCM is authenticated, so a flipped bit must fail rather than decrypt to
// something else.
func TestOpenRejectsTampering(t *testing.T) {
	box := testBox(t)
	sealed, err := box.Seal("secret")
	if err != nil {
		t.Fatal(err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := box.Open(tampered); err == nil {
		t.Error("a tampered ciphertext opened")
	}
}

func TestOpenRejectsTruncation(t *testing.T) {
	box := testBox(t)
	if _, err := box.Open([]byte{1, 2, 3}); err == nil {
		t.Error("a value shorter than the nonce opened")
	}
}

func TestNilBoxRefusesRatherThanPanics(t *testing.T) {
	var box *Box
	if _, err := box.Seal("x"); err != ErrNoKey {
		t.Errorf("Seal error = %v, want ErrNoKey", err)
	}
	if _, err := box.Open([]byte("x")); err != ErrNoKey {
		t.Errorf("Open error = %v, want ErrNoKey", err)
	}
}

func TestNewRejectsWrongLengthKey(t *testing.T) {
	// Valid base64, wrong number of bytes — the likely mistake is a key
	// generated with the wrong tool, so the error has to say the size.
	_, err := New("c2hvcnQ=")
	if err == nil {
		t.Fatal("a short key was accepted")
	}
	if !strings.Contains(err.Error(), "want 32") {
		t.Errorf("error = %q, want it to name the expected size", err)
	}
}

func TestNewRejectsNonBase64(t *testing.T) {
	if _, err := New("not base64 at all!"); err == nil {
		t.Error("a non-base64 key was accepted")
	}
}

func TestSealForBindsABlobToItsContext(t *testing.T) {
	box := testBox(t)
	sealed, err := box.SealFor("session/abc", "identity")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := box.OpenFor("session/abc", sealed); err != nil || got != "identity" {
		t.Fatalf("OpenFor same context = %q, %v", got, err)
	}
	if _, err := box.OpenFor("session/other", sealed); err == nil {
		t.Error("a bound blob opened under another context")
	}
	if _, err := box.Open(sealed); err == nil {
		t.Error("a bound blob opened with no context")
	}
}

func TestOpenForStillOpensALegacyBlob(t *testing.T) {
	box := testBox(t)
	legacy, err := box.Seal("old value")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := box.OpenFor("anything", legacy); err != nil || got != "old value" {
		t.Fatalf("legacy blob = %q, %v", got, err)
	}
	// A legacy blob whose random nonce happens to start with the prefix.
	nonce := append([]byte(boundPrefix), make([]byte, box.aead.NonceSize()-len(boundPrefix))...)
	unlucky := box.aead.Seal(nonce, nonce, []byte("unlucky"), nil)
	if got, err := box.OpenFor("anything", unlucky); err != nil || got != "unlucky" {
		t.Fatalf("prefix-colliding legacy blob = %q, %v", got, err)
	}
}
