package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	dockerclient "github.com/docker/docker/client"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// SSH to managed servers (D-19): every caller reaches a server through
// Service.SSH, which hands out the server's pooled connection with its
// pinned host key. The first connection to a server pins the key it presents
// (trust on first use, pinHostKey); after that only an instance operator can
// replace the pin (ResetHostKey), after comparing its fingerprint.

// SSHConnector is the part of *sshpool.Pool the server service uses.
type SSHConnector interface {
	Handle(t sshpool.Target) sshpool.RemoteHost
	Invalidate(serverID string)
}

// hostKeyScanner runs a credential-free SSH handshake and returns the host
// key the server presents (sshclient.ScanHostKey).
type hostKeyScanner func(ctx context.Context, cfg sshclient.ScanConfig) (ssh.PublicKey, error)

// scanTimeout bounds a host-key scan (TCP connect plus key exchange).
const scanTimeout = 10 * time.Second

var (
	// ErrServerNotFound is returned when the server row does not exist.
	ErrServerNotFound = errors.New("server not found")

	// ErrNoProvisionRun: no provisioning run is recorded for the server.
	ErrNoProvisionRun = errors.New("no provisioning run recorded")

	// ErrServerHoldsVolumes: shared volumes (D-38) are on the server; they
	// must be deleted first, knowingly, since deleting them deletes their data.
	ErrServerHoldsVolumes = errors.New("the server holds shared volumes: delete them under Settings → Storage first")

	// ErrSSHNotConfigured is returned by every SSH call when the service has
	// no SSH connector (WithSSH was not called).
	ErrSSHNotConfigured = errors.New("SSH to managed servers is not configured")

	// ErrSSHFailed is matched (errors.Is) by the errors of a host-key scan or
	// reset that could not reach the server over SSH.
	ErrSSHFailed = errors.New("SSH failed")

	// ErrHostKeyResetOperatorOnly is returned when someone who is not an
	// instance operator tries to pin or replace a server's SSH host key.
	ErrHostKeyResetOperatorOnly = errors.New("only instance operators can pin or replace a server's SSH host key")

	// ErrInvalidFingerprint is returned when a confirmed fingerprint is not
	// a SHA256 fingerprint.
	ErrInvalidFingerprint = errors.New(`the fingerprint must be a SHA256 fingerprint, like "SHA256:" followed by 43 characters`)
)

// HostKeyChangedError is returned by ResetHostKey when the server presents a
// key other than the one the operator confirmed (it changed since the scan,
// or the confirmation was for another key). Nothing is pinned.
type HostKeyChangedError struct {
	Confirmed string
	Presented string
	KeyType   string
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("the server now presents %s %s, not the confirmed %s; nothing was pinned. Scan again and compare the new fingerprint on the server's console",
		e.KeyType, e.Presented, e.Confirmed)
}

// sshFailure marks an error as ErrSSHFailed while keeping its message and
// chain (errors.As still finds a *sshclient.HostKeyMismatchError).
type sshFailure struct{ err error }

func (e *sshFailure) Error() string        { return e.err.Error() }
func (e *sshFailure) Unwrap() error        { return e.err }
func (e *sshFailure) Is(target error) bool { return target == ErrSSHFailed }

// WithSSH gives the service its SSH connections (the pool) and a logger for
// host-key events. Called from main.go.
func (s *Service) WithSSH(conn SSHConnector, log *zap.Logger) {
	s.conn = conn
	if log != nil {
		s.log = log
	}
}

// SSH returns the pooled connection to srv, built from the current server
// row: its address, credentials and pinned host key. A new connection is
// made with the key pinned at that moment (read again, since srv may have
// been loaded before a pin was set or replaced), and one made while no key
// is pinned pins the key it saw. Errors from a changed host key say what to
// do about it. It satisfies docker.SSHProvider and configfile.SSHProvider.
func (s *Service) SSH(srv *Server) sshpool.RemoteHost {
	var h sshpool.RemoteHost = unconfiguredSSH{}
	if s.conn != nil && srv.Connection == ConnectionAgent {
		h = s.conn.Handle(s.agentTarget(srv))
	} else if s.conn != nil {
		h = s.conn.Handle(sshpool.Target{
			ServerID:      srv.ID,
			Name:          srv.Name,
			Host:          srv.IPAddress,
			Port:          srv.Port,
			User:          srv.User,
			PrivateKeyPEM: srv.SSHKey,
			PinnedHostKey: srv.HostKey,
			LoadPin:       s.loadHostKey(srv.ID),
			OnFirstUse:    s.pinHostKey(srv),
		})
	}
	return hintedHost{h: h, name: srv.Name}
}

// loadHostKey returns the pool's LoadPin for server id: the key pinned now.
func (s *Service) loadHostKey(id string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		return s.repo.HostKey(ctx, id)
	}
}

// pinHostKey returns the pool's OnFirstUse for srv: it pins the key a
// connection without a pin saw, unless another key was pinned first, in
// which case the connection fails with a mismatch.
func (s *Service) pinHostKey(srv *Server) func(ctx context.Context, key string) (bool, error) {
	id, name := srv.ID, srv.Name
	addr := net.JoinHostPort(srv.IPAddress, strconv.Itoa(srv.Port))
	return func(ctx context.Context, key string) (bool, error) {
		stored, wrote, err := s.repo.PinHostKey(ctx, id, key)
		if err != nil {
			return false, err
		}
		if wrote {
			return true, nil
		}
		if stored == "" {
			// The pin was cleared between the compare-and-set and the read.
			return false, fmt.Errorf("the SSH host key of %s was cleared while it was being pinned; try again", name)
		}
		if sameHostKey(stored, key) {
			return false, nil // pinned by a concurrent connection
		}
		mismatch := mismatchError(addr, stored, key)
		s.log.Warn("a different SSH host key was pinned first; refusing the connection",
			zap.String("server_id", id), zap.String("server", name),
			zap.String("pinned", mismatch.Expected), zap.String("presented", mismatch.Presented))
		return false, mismatch
	}
}

// AuthorizeHostKeyReset returns ErrHostKeyResetOperatorOnly unless actorID
// is an instance operator. Replacing a pin decides which machine the panel
// trusts with its SSH keys, which no workspace role should grant.
func (s *Service) AuthorizeHostKeyReset(ctx context.Context, actorID string) error {
	return s.requireOperator(ctx, actorID, ErrHostKeyResetOperatorOnly)
}

// ScanHostKey runs a handshake with the server, without credentials, and
// reports the key it presents next to the pinned one. It is the first step
// of verifying or replacing a pin.
func (s *Service) ScanHostKey(ctx context.Context, id string) (*HostKeyScanResponse, error) {
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	presented, err := s.scanKey(ctx, srv)
	if err != nil {
		return nil, err
	}
	return scanResponse(srv.HostKey, presented), nil
}

// ResetHostKey pins the key the server presents now, provided its
// fingerprint is the one the operator confirmed (otherwise a
// *HostKeyChangedError and nothing changes). It replaces any earlier pin and
// closes the server's pooled connections when the key changed. Instance
// operators only. It returns the updated server.
func (s *Service) ResetHostKey(ctx context.Context, actorID, id, fingerprint string) (*Server, error) {
	if err := s.AuthorizeHostKeyReset(ctx, actorID); err != nil {
		return nil, err
	}
	confirmed := strings.TrimSpace(fingerprint)
	if !validFingerprint(confirmed) {
		return nil, ErrInvalidFingerprint
	}
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	presented, err := s.scanKey(ctx, srv)
	if err != nil {
		return nil, err
	}
	fp, keyType, err := sshclient.Fingerprint(presented)
	if err != nil {
		return nil, fmt.Errorf("read the presented host key: %w", err)
	}
	if fp != confirmed {
		return nil, &HostKeyChangedError{Confirmed: confirmed, Presented: fp, KeyType: keyType}
	}

	unchanged := srv.HostKey != "" && sameHostKey(srv.HostKey, presented)
	if err := s.repo.ReplaceHostKey(ctx, id, presented); err != nil {
		return nil, fmt.Errorf("replace the SSH host key: %w", err)
	}
	if !unchanged && s.conn != nil {
		// Connections made with the old key (or before any pin) must not
		// carry on: close them now, failing whatever runs on them.
		s.conn.Invalidate(id)
	}
	previous := ""
	if srv.HostKey != "" {
		previous, _, _ = sshclient.Fingerprint(srv.HostKey)
	}
	s.log.Info("SSH host key pinned by an instance operator",
		zap.String("server_id", id), zap.String("server", srv.Name), zap.String("actor_id", actorID),
		zap.String("key_type", keyType), zap.String("fingerprint", fp),
		zap.String("previous_fingerprint", previous), zap.Bool("changed", !unchanged))
	return s.repo.GetByID(ctx, id)
}

// scanKey returns the host key srv presents, in authorized_keys form. With
// a pin, the pinned key's type is negotiated first so the two compare.
func (s *Service) scanKey(ctx context.Context, srv *Server) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	key, err := s.scan(ctx, sshclient.ScanConfig{
		Host:    srv.IPAddress,
		Port:    srv.Port,
		Timeout: scanTimeout,
		Prefer:  srv.HostKey,
	})
	if err != nil {
		return "", &sshFailure{fmt.Errorf("scan the SSH host key of %s: %w", srv.Name, err)}
	}
	return sshclient.MarshalHostKey(key), nil
}

// scanResponse compares the presented key with the pin ("" when none).
func scanResponse(pinned, presented string) *HostKeyScanResponse {
	fp, keyType, _ := sshclient.Fingerprint(presented)
	r := &HostKeyScanResponse{KeyType: keyType, PresentedFingerprint: fp}
	if pinned == "" {
		return r
	}
	if pfp, ptype, err := sshclient.Fingerprint(pinned); err == nil {
		r.PinnedFingerprint, r.PinnedKeyType = &pfp, &ptype
		r.MatchesPinned = pfp == fp
	}
	return r
}

// sameHostKey reports whether two authorized_keys strings hold the same key.
func sameHostKey(a, b string) bool {
	ka, err := sshclient.ParseHostKey(a)
	if err != nil {
		return false
	}
	kb, err := sshclient.ParseHostKey(b)
	if err != nil {
		return false
	}
	return sshclient.MarshalHostKey(ka) == sshclient.MarshalHostKey(kb)
}

// mismatchError describes a presented key that is not the pinned one.
func mismatchError(addr, pinned, presented string) *sshclient.HostKeyMismatchError {
	e := &sshclient.HostKeyMismatchError{Addr: addr, Expected: "an unreadable pinned key"}
	if fp, keyType, err := sshclient.Fingerprint(pinned); err == nil {
		e.Expected, e.ExpectedType = fp, keyType
	}
	e.Presented, _, _ = sshclient.Fingerprint(presented)
	return e
}

// validFingerprint reports whether fp looks like "SHA256:<base64 of 32 bytes>",
// with or without base64 padding.
func validFingerprint(fp string) bool {
	b64, ok := strings.CutPrefix(fp, "SHA256:")
	if !ok {
		return false
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	return err == nil && len(raw) == 32
}

// hostKeyHint adds what to do to a host-key mismatch; any other error is
// returned unchanged.
func hostKeyHint(name string, err error) error {
	if err == nil || !errors.Is(err, sshclient.ErrHostKeyMismatch) {
		return err
	}
	return fmt.Errorf("%w. If you rebuilt server %q or regenerated its SSH host keys, an instance operator can accept the new key under Servers → %s → SSH host key; otherwise someone may be intercepting the connection",
		err, name, name)
}

// hintedHost is a RemoteHost whose errors explain a host-key mismatch, so a
// deploy log, a provision stream or a test connection says what to do.
type hintedHost struct {
	h    sshpool.RemoteHost
	name string
}

func (h hintedHost) Exec(ctx context.Context, cmd string) (string, error) {
	out, err := h.h.Exec(ctx, cmd)
	return out, hostKeyHint(h.name, err)
}

func (h hintedHost) ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	out, err := h.h.ExecInput(ctx, cmd, stdin)
	return out, hostKeyHint(h.name, err)
}

func (h hintedHost) ExecStream(ctx context.Context, cmd string, logFn func(string)) error {
	return hostKeyHint(h.name, h.h.ExecStream(ctx, cmd, logFn))
}

func (h hintedHost) Upload(ctx context.Context, remotePath string, content []byte) error {
	return hostKeyHint(h.name, h.h.Upload(ctx, remotePath, content))
}

func (h hintedHost) NewPTY(ctx context.Context, cols, rows int) (*sshclient.PTYSession, error) {
	pty, err := h.h.NewPTY(ctx, cols, rows)
	return pty, hostKeyHint(h.name, err)
}

func (h hintedHost) DialUnix(ctx context.Context, path string) (net.Conn, error) {
	nc, err := h.h.DialUnix(ctx, path)
	return nc, hostKeyHint(h.name, err)
}

func (h hintedHost) DockerClient() (*dockerclient.Client, error) {
	return sshclient.NewDockerClient(h.DialUnix)
}

func (h hintedHost) Ensure(ctx context.Context, opts sshpool.EnsureOpts) (sshpool.ConnInfo, error) {
	info, err := h.h.Ensure(ctx, opts)
	return info, hostKeyHint(h.name, err)
}

func (h hintedHost) Ping(ctx context.Context) (time.Duration, error) {
	rtt, err := h.h.Ping(ctx)
	return rtt, hostKeyHint(h.name, err)
}

func (h hintedHost) Subsystem(ctx context.Context, name string, stdin io.Reader) (string, error) {
	out, err := h.h.Subsystem(ctx, name, stdin)
	return out, hostKeyHint(h.name, err)
}

func (h hintedHost) SubsystemStream(ctx context.Context, name string, stdin io.Reader, lineFn func(string)) error {
	return hostKeyHint(h.name, h.h.SubsystemStream(ctx, name, stdin, lineFn))
}

// unconfiguredSSH fails every call: the service has no SSH connector.
type unconfiguredSSH struct{}

func (unconfiguredSSH) Exec(context.Context, string) (string, error) { return "", ErrSSHNotConfigured }
func (unconfiguredSSH) ExecInput(context.Context, string, io.Reader) (string, error) {
	return "", ErrSSHNotConfigured
}
func (unconfiguredSSH) ExecStream(context.Context, string, func(string)) error {
	return ErrSSHNotConfigured
}
func (unconfiguredSSH) Upload(context.Context, string, []byte) error { return ErrSSHNotConfigured }
func (unconfiguredSSH) NewPTY(context.Context, int, int) (*sshclient.PTYSession, error) {
	return nil, ErrSSHNotConfigured
}
func (unconfiguredSSH) DialUnix(context.Context, string) (net.Conn, error) {
	return nil, ErrSSHNotConfigured
}
func (unconfiguredSSH) DockerClient() (*dockerclient.Client, error) { return nil, ErrSSHNotConfigured }
func (unconfiguredSSH) Ensure(context.Context, sshpool.EnsureOpts) (sshpool.ConnInfo, error) {
	return sshpool.ConnInfo{}, ErrSSHNotConfigured
}
func (unconfiguredSSH) Ping(context.Context) (time.Duration, error) { return 0, ErrSSHNotConfigured }
func (unconfiguredSSH) Subsystem(context.Context, string, io.Reader) (string, error) {
	return "", ErrSSHNotConfigured
}
func (unconfiguredSSH) SubsystemStream(context.Context, string, io.Reader, func(string)) error {
	return ErrSSHNotConfigured
}
