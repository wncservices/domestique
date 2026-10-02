// Package secrets encrypts the few values that have to be kept rather than
// hashed.
//
// A password can be hashed because it is only ever compared. A session token
// has to be replayed to somebody else's API, so it has to come back out — and
// that means encryption, with a key that lives outside the database. If the
// database leaks, the tokens in it are inert without the key.
//
// AES-256-GCM: authenticated, so a tampered ciphertext fails to open rather
// than decrypting to something attacker-chosen.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// EnvKey holds the base64 of a 32-byte key. Deliberately not a config file
// field: it is a credential, and credentials come from the environment.
const EnvKey = "DOMESTIQUE_ENCRYPTION_KEY"

// ErrNoKey means nothing can be stored. Callers turn this into "the UI cannot
// offer to save a credential", never into "save it in the clear".
var ErrNoKey = errors.New("no encryption key: set " + EnvKey)

// Box seals and opens values. The zero Box is unusable; use FromEnv.
type Box struct {
	aead cipher.AEAD
	key  []byte
}

// MAC returns a keyed, one-way stand-in for value: hex HMAC-SHA256 under a
// subkey derived from the encryption key and purpose. It exists so a table can
// find the rows that belong to a value (one rider's sessions) with an index,
// without storing the value and without decrypting every row.
//
// Keyed rather than a plain hash on purpose: a rider name is low-entropy, so
// an unkeyed hash of it is reversible by trying names. The purpose keeps two
// tables from being joined on each other's stand-ins. Rotating the key changes
// every MAC, which is harmless for its one user because the same rotation
// already makes every sealed session unreadable.
//
// A nil Box returns "", which no caller stores (nothing can be stored
// without a key at all).
func (b *Box) MAC(purpose, value string) string {
	if b == nil {
		return ""
	}
	sub := hmac.New(sha256.New, b.key)
	sub.Write([]byte("domestique/mac/" + purpose))
	m := hmac.New(sha256.New, sub.Sum(nil))
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}

// GenerateKey returns a fresh key, encoded the way EnvKey expects.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// FromEnv builds a Box from EnvKey. A missing key returns ErrNoKey, which is
// an ordinary state and not a startup failure: everything except storing
// credentials still works without one.
func FromEnv() (*Box, error) {
	raw := os.Getenv(EnvKey)
	if raw == "" {
		return nil, ErrNoKey
	}
	return New(raw)
}

// New builds a Box from a base64-encoded 32-byte key.
func New(encoded string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", EnvKey, err)
	}
	// Length is checked here rather than left to aes.NewCipher so the error
	// names the variable and the fix, at startup, once.
	if len(key) != 32 {
		return nil, fmt.Errorf("%s decodes to %d bytes, want 32 — generate one with `domestique keygen`",
			EnvKey, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, key: key}, nil
}

// Seal encrypts plaintext. The nonce is random per call and prepended, so
// sealing the same value twice produces different ciphertext — otherwise the
// database would reveal which riders share a token.
func (b *Box) Seal(plaintext string) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}

	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open decrypts what Seal produced. It fails on a wrong key or a tampered
// value; both mean the stored credential is unusable and should be replaced,
// not worked around.
func (b *Box) Open(ciphertext []byte) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}

	size := b.aead.NonceSize()
	if len(ciphertext) < size {
		return "", errors.New("ciphertext is too short to contain a nonce")
	}

	plaintext, err := b.aead.Open(nil, ciphertext[:size], ciphertext[size:], nil)
	if err != nil {
		// Deliberately vague: the caller cannot tell a wrong key from a
		// corrupt value, and neither can an attacker probing with either.
		return "", errors.New("could not decrypt: wrong key, or the value was altered")
	}
	return string(plaintext), nil
}
