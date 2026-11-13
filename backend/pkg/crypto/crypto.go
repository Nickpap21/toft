// Package crypto seals the values Toft stores encrypted with ENCRYPTION_KEY:
// AES-256-GCM, the key SHA-256 of the secret.
//
// Values are "v1.<key id>.<base64url(nonce ‖ ciphertext ‖ tag)>", sealed
// with the value's place (AD) as associated data, so a value copied to
// another row, column or table doesn't open (D-126). Releases before v0.28.0
// wrote base64(nonce ‖ ciphertext ‖ tag), with no associated data and no key
// named: only OpenLegacy reads that format, so an old value from a backup can't
// be planted in another row. "." is not in its alphabet, so the two never
// mix up.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// deriveKey produces a 32-byte AES-256 key from an arbitrary-length secret using SHA-256.
func deriveKey(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

const v1Prefix = "v1."

// Keys seals and opens stored values. current seals; previous, set only while
// a rotation runs, still opens what it sealed.
type Keys struct {
	current, previous *key
}

type key struct {
	id   string
	aead cipher.AEAD
}

func newKey(secret string) *key {
	k := deriveKey(secret)
	block, err := aes.NewCipher(k)
	if err != nil {
		panic(err) // a 32-byte key never fails
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err) // AES's block size never fails
	}
	id := sha256.Sum256(append([]byte("toft key id"), k...))
	return &key{id: hex.EncodeToString(id[:4]), aead: gcm}
}

// NewKeys takes ENCRYPTION_KEY and ENCRYPTION_KEY_PREVIOUS ("" when no
// rotation runs).
func NewKeys(current, previous string) *Keys {
	k := &Keys{current: newKey(current)}
	if previous != "" {
		k.previous = newKey(previous)
	}
	return k
}

// CurrentID names the key that seals.
func (k *Keys) CurrentID() string { return k.current.id }

// AD is a value's place, the associated data it is sealed with: its table and
// column ("env_vars.value"), and its row's identity (an ID, or the parts of
// a unique key). None of the parts contains a "/".
func AD(column string, row ...string) string {
	return "toft/v1/" + column + "/" + strings.Join(row, "/")
}

// Seal encrypts plain for the place ad.
func (k *Keys) Seal(ad, plain string) (string, error) {
	nonce := make([]byte, k.current.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := k.current.aead.Seal(nonce, nonce, []byte(plain), []byte(ad))
	return v1Prefix + k.current.id + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// ErrOldFormat is a value not in the v1 format: sealed before v0.28.0,
// which only OpenLegacy reads, or not sealed at all.
var ErrOldFormat = errors.New("sealed in the format before v0.28.0; the start-up pass re-seals it")

// Open decrypts a value stored at the place ad.
func (k *Keys) Open(ad, stored string) (string, error) {
	rest, ok := strings.CutPrefix(stored, v1Prefix)
	if !ok {
		return "", ErrOldFormat
	}
	id, body, ok := strings.Cut(rest, ".")
	if !ok {
		return "", errors.New("malformed sealed value")
	}
	var kk *key
	switch {
	case id == k.current.id:
		kk = k.current
	case k.previous != nil && id == k.previous.id:
		kk = k.previous
	default:
		return "", fmt.Errorf("sealed with key %s, which is neither ENCRYPTION_KEY nor ENCRYPTION_KEY_PREVIOUS", id)
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	return open(kk, data, []byte(ad))
}

// OpenLegacy opens a value stored at the place ad in either format: an old
// one is tried with the current key, then the previous one (GCM tells a
// wrong key apart). Only the start-up passes use it, to seal old values
// again; everything else opens with Open.
func (k *Keys) OpenLegacy(ad, stored string) (string, error) {
	plain, err := k.Open(ad, stored)
	if !errors.Is(err, ErrOldFormat) {
		return plain, err
	}
	data, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	plain, err = open(k.current, data, nil)
	if err != nil && k.previous != nil {
		if p, perr := open(k.previous, data, nil); perr == nil {
			return p, nil
		}
	}
	return plain, err
}

func open(k *key, data, ad []byte) (string, error) {
	n := k.aead.NonceSize()
	if len(data) < n {
		return "", errors.New("ciphertext too short")
	}
	plain, err := k.aead.Open(nil, data[:n], data[n:], ad)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}

// Encrypt seals plaintext in the old format with secret: the format of
// every release before v0.28.0, kept for tests that store such a value.
func Encrypt(secret, plaintext string) (string, error) {
	k := newKey(secret)
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(k.aead.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}
