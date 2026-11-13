package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch is matched by errors.Is for every *HostKeyMismatchError.
var ErrHostKeyMismatch = errors.New("ssh: host key mismatch")

// HostKeyMismatchError reports that a server presented a host key other than
// the pinned one. The connection is refused before any credential is sent.
type HostKeyMismatchError struct {
	// Addr is the host:port that was dialled.
	Addr string
	// ExpectedType is the pinned key's type, e.g. "ssh-ed25519".
	ExpectedType string
	// Expected is the SHA256 fingerprint of the pinned key.
	Expected string
	// Presented is the SHA256 fingerprint of the key the server presented.
	// It is empty when the server no longer offers a key of the pinned type
	// at all, so no key could be compared.
	Presented string
}

func (e *HostKeyMismatchError) Error() string {
	if e.Presented == "" {
		return fmt.Sprintf("SSH host key for %s changed: expected %s %s, but the server no longer offers a %s host key",
			e.Addr, e.ExpectedType, e.Expected, e.ExpectedType)
	}
	return fmt.Sprintf("SSH host key for %s changed: expected %s, server presented %s",
		e.Addr, e.Expected, e.Presented)
}

// Is makes errors.Is(err, ErrHostKeyMismatch) true for a mismatch.
func (e *HostKeyMismatchError) Is(target error) bool { return target == ErrHostKeyMismatch }

// MarshalHostKey returns key in authorized_keys format without a comment or
// trailing newline, e.g. "ssh-ed25519 AAAA...". A certificate is reduced to
// the key it certifies. This is the form pins are stored and compared in.
func MarshalHostKey(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	key = plainKey(key)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// ParseHostKey parses a host key in authorized_keys format (as written by
// MarshalHostKey, or a line of a server's /etc/ssh/ssh_host_*_key.pub). A
// certificate is reduced to the key it certifies.
func ParseHostKey(authorizedKey string) (ssh.PublicKey, error) {
	s := strings.TrimSpace(authorizedKey)
	if s == "" {
		return nil, errors.New("empty host key")
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	return plainKey(key), nil
}

// Fingerprint returns the SHA256 fingerprint ("SHA256:...") and the key type
// of a host key in authorized_keys format.
func Fingerprint(authorizedKey string) (fingerprint, keyType string, err error) {
	key, err := ParseHostKey(authorizedKey)
	if err != nil {
		return "", "", err
	}
	return ssh.FingerprintSHA256(key), key.Type(), nil
}

// sameKey reports whether a and b are the same public key.
func sameKey(a, b ssh.PublicKey) bool {
	return bytes.Equal(a.Marshal(), b.Marshal())
}

// plainKey reduces a certificate to the key it certifies. Pins are always
// plain keys, so a certificate presented anyway is compared by its key.
func plainKey(key ssh.PublicKey) ssh.PublicKey {
	if cert, ok := key.(*ssh.Certificate); ok {
		return cert.Key
	}
	return key
}

// tofuHostKeyAlgorithms is what a connection without a pin offers: plain
// (non-certificate) keys only, ed25519 first. A certificate algorithm would
// let the server present a certificate, and the plain key is what is pinned.
var tofuHostKeyAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256,
	ssh.KeyAlgoECDSA384,
	ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512,
	ssh.KeyAlgoRSASHA256,
}

// hostKeyAlgorithmsFor returns the host key algorithms a connection offers.
// With a pin it is only the pinned key's type, so negotiation can never pick
// another of the server's host keys and report a false mismatch. An RSA key
// can sign with three algorithms; the others have exactly one.
func hostKeyAlgorithmsFor(pinned ssh.PublicKey) []string {
	if pinned == nil {
		return slices.Clone(tofuHostKeyAlgorithms)
	}
	if pinned.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{pinned.Type()}
}

// hostKeyCheck is the HostKeyCallback of one handshake. It records the key
// the server presented and, with a pin, the mismatch it refused.
type hostKeyCheck struct {
	addr   string
	pinned ssh.PublicKey // nil: trust on first use (capture only)

	presented ssh.PublicKey
	mismatch  *HostKeyMismatchError
}

// callback runs for the first key exchange and again for every re-key of
// the connection. Without a pin, the first key is accepted and every re-key
// must present that same key.
func (h *hostKeyCheck) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	key = plainKey(key)
	want := h.pinned
	if want == nil {
		want = h.presented
	}
	if want == nil || sameKey(key, want) {
		h.presented = key
		return nil
	}
	h.mismatch = &HostKeyMismatchError{
		Addr:         h.addr,
		ExpectedType: want.Type(),
		Expected:     ssh.FingerprintSHA256(want),
		Presented:    ssh.FingerprintSHA256(key),
	}
	return h.mismatch
}

// explain turns a failed handshake into the host-key error it was, if any:
// the recorded mismatch, or (with a pin) a server that no longer offers a
// key of the pinned type. It returns nil for any other failure.
func (h *hostKeyCheck) explain(err error) error {
	if h.mismatch != nil {
		return h.mismatch
	}
	var neg *ssh.AlgorithmNegotiationError
	if h.pinned != nil && errors.As(err, &neg) && neg.What == "host key" {
		return &HostKeyMismatchError{
			Addr:         h.addr,
			ExpectedType: h.pinned.Type(),
			Expected:     ssh.FingerprintSHA256(h.pinned),
		}
	}
	return nil
}

// ScanConfig is the target of ScanHostKey.
type ScanConfig struct {
	Host string
	Port int
	// Timeout bounds the TCP connect and the key exchange. Zero means
	// DefaultHandshakeTimeout.
	Timeout time.Duration
	// Prefer, when set, is a host key (usually the current pin) whose type
	// is negotiated first, so a server with several host keys presents the
	// one that can be compared with the pin. Other types stay acceptable,
	// so a rebuilt server that dropped that type still shows its new key.
	Prefer string
}

// errScanned aborts a scan handshake once the host key has been seen.
var errScanned = errors.New("host key scanned")

// ScanHostKey runs an SSH key exchange with the server and returns the host
// key it presents, without authenticating: the handshake is aborted right
// after the key is seen, so no credentials are needed or sent.
func ScanHostKey(ctx context.Context, cfg ScanConfig) (ssh.PublicKey, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	algos := slices.Clone(tofuHostKeyAlgorithms)
	if cfg.Prefer != "" {
		if prefer, err := ParseHostKey(cfg.Prefer); err == nil {
			first := hostKeyAlgorithmsFor(prefer)
			rest := slices.DeleteFunc(algos, func(a string) bool { return slices.Contains(first, a) })
			algos = append(first, rest...)
		}
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var presented ssh.PublicKey
	sshCfg := &ssh.ClientConfig{
		User: "host-key-scan",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = plainKey(key)
			return errScanned
		},
		HostKeyAlgorithms: algos,
	}
	c, err := handshake(ctx, addr, nil, sshCfg, timeout)
	if c != nil {
		_ = c.conn.Close()
	}
	if presented != nil {
		return presented, nil
	}
	if err == nil {
		// Unreachable: the callback always aborts the handshake.
		return nil, fmt.Errorf("scan %s: no host key presented", addr)
	}
	return nil, err
}
