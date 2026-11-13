package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshclient/sshtest"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// ─── Fakes ────────────────────────────────────────────────────────────────────

// fakeRepo is an in-memory Repository. PinHostKey is a real compare-and-set.
type fakeRepo struct {
	mu       sync.Mutex
	servers  map[string]*Server
	statuses []string // every UpdateStatus, in order
	replaced []string // keys passed to ReplaceHostKey
	pinErr   error
	// beforePin runs inside PinHostKey before the compare-and-set, to stage
	// a concurrent pin.
	beforePin   func()
	dns         map[string]*DNSConfig         // server ID → DNS provider
	backups     map[string]*BackupDestination // server ID ("" panel host) → destination
	shares      map[string]map[string]Share
	orgServices map[string]map[string]int64
	runs        map[string]*ProvisionRun // server ID → last provisioning run
}

func (r *fakeRepo) GetDNSProvider(_ context.Context, id string) (*DNSConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.dns[id]; ok {
		return c, nil
	}
	return nil, ErrNoDNSProvider
}

func (r *fakeRepo) SetDNSProvider(_ context.Context, id, provider string, creds map[string]string) (*DNSConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dns == nil {
		r.dns = map[string]*DNSConfig{}
	}
	r.dns[id] = &DNSConfig{Provider: provider, Credentials: creds}
	return r.dns[id], nil
}

func (r *fakeRepo) DeleteDNSProvider(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dns, id)
	return nil
}

// Shares: shares[serverID][orgID]; orgServices[serverID][orgID] counts
// services.
func (r *fakeRepo) ListShares(_ context.Context, serverID string) ([]Share, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Share{}
	for _, sh := range r.shares[serverID] {
		out = append(out, sh)
	}
	return out, nil
}
func (r *fakeRepo) GetShare(_ context.Context, serverID, orgID string) (*Share, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sh, ok := r.shares[serverID][orgID]
	if !ok {
		return nil, ErrNoShare
	}
	return &sh, nil
}
func (r *fakeRepo) UpsertShare(_ context.Context, serverID, orgID string, cpu float64, mem int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shares == nil {
		r.shares = map[string]map[string]Share{}
	}
	if r.shares[serverID] == nil {
		r.shares[serverID] = map[string]Share{}
	}
	r.shares[serverID][orgID] = Share{OrgID: orgID, CPUCores: cpu, MemoryMB: mem}
	return nil
}
func (r *fakeRepo) DeleteShare(_ context.Context, serverID, orgID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.shares[serverID][orgID]
	delete(r.shares[serverID], orgID)
	return ok, nil
}
func (r *fakeRepo) ListSharedWithOrg(_ context.Context, orgID string) ([]SharedServer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []SharedServer{}
	for sid, byOrg := range r.shares {
		if sh, ok := byOrg[orgID]; ok {
			out = append(out, SharedServer{Server: *r.servers[sid], OwnerOrgName: "owner", Share: sh})
		}
	}
	return out, nil
}
func (r *fakeRepo) CountOrgServicesOnServer(_ context.Context, serverID, orgID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.orgServices[serverID][orgID], nil
}
func (r *fakeRepo) CountServicesOnServer(_ context.Context, serverID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, c := range r.orgServices[serverID] {
		n += c
	}
	return n, nil
}
func (r *fakeRepo) CountSharedServicesOnServer(_ context.Context, serverID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for org := range r.shares[serverID] {
		n += r.orgServices[serverID][org]
	}
	return n, nil
}

func newFakeRepo(servers ...*Server) *fakeRepo {
	r := &fakeRepo{servers: map[string]*Server{}}
	for _, s := range servers {
		r.servers[s.ID] = s
	}
	return r
}

func (r *fakeRepo) get(id string) (*Server, error) {
	s, ok := r.servers[id]
	if !ok {
		return nil, ErrServerNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *fakeRepo) EncryptLegacySSHKeys(context.Context) (int64, error) { return 0, nil }

func (r *fakeRepo) Create(context.Context, string, string, CreateServerInput) (*Server, error) {
	return nil, errors.New("not implemented")
}

func (r *fakeRepo) GetByID(_ context.Context, id string) (*Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.get(id)
}

func (r *fakeRepo) ListByOrg(_ context.Context, orgID string) ([]Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Server, 0, len(r.servers))
	for _, s := range r.servers {
		if s.OrgID == orgID {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *fakeRepo) ListAll(context.Context) ([]Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Server, 0, len(r.servers))
	for _, s := range r.servers {
		out = append(out, *s)
	}
	return out, nil
}

func (r *fakeRepo) UpdateStatus(_ context.Context, id, status string) (*Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	if !ok {
		return nil, ErrServerNotFound
	}
	s.Status = status
	r.statuses = append(r.statuses, status)
	return r.get(id)
}

func (r *fakeRepo) MarkProvisioned(_ context.Context, id string) (*Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	if !ok {
		return nil, ErrServerNotFound
	}
	now := time.Now()
	s.ProvisionedAt = &now
	return r.get(id)
}

func (r *fakeRepo) UpdateCapacity(_ context.Context, id string, _ UpdateCapacityInput) (*Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.get(id)
}

func (r *fakeRepo) UpdateDiscoveredCapacity(_ context.Context, id string, _ float64, _ int64) (*Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.get(id)
}

func (r *fakeRepo) Rename(_ context.Context, id, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	if !ok {
		return ErrServerNotFound
	}
	s.Name = name
	return nil
}

// NameFromHostname renames a server still named server-<n> (the fake's
// automatic name); names are unique in the workspace.
func (r *fakeRepo) NameFromHostname(_ context.Context, id, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	if !ok || !regexp.MustCompile(`^server-[0-9]+$`).MatchString(s.Name) {
		return false, nil
	}
	for _, o := range r.servers {
		if o.ID != id && o.OrgID == s.OrgID && o.Name == name {
			return false, ErrServerNameTaken
		}
	}
	s.Name = name
	return true, nil
}

func (r *fakeRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.servers, id)
	return nil
}

func (r *fakeRepo) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	return ok && s.OrgID == orgID, nil
}

func (r *fakeRepo) ListServicesByServer(context.Context, string, string) ([]ServiceOnServer, error) {
	return nil, nil
}

func (r *fakeRepo) StartProvisionRun(_ context.Context, id, by string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]*ProvisionRun{}
	}
	r.runs[id] = &ProvisionRun{StartedBy: by}
	return nil
}
func (r *fakeRepo) AppendProvisionLog(_ context.Context, id, line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[id].Log += line + "\n"
	return nil
}
func (r *fakeRepo) FinishProvisionRun(_ context.Context, id string, ok bool, msg string, warnings []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.runs[id].FinishedAt, r.runs[id].OK, r.runs[id].Error, r.runs[id].Warnings = &now, &ok, msg, warnings
	return nil
}
func (r *fakeRepo) ListProvisionWarnings(context.Context, string) (map[string][]string, error) {
	return nil, nil
}
func (r *fakeRepo) CloseUnfinishedProvisionRuns(context.Context) (int64, error) { return 0, nil }
func (r *fakeRepo) GetProvisionRun(_ context.Context, id string) (*ProvisionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[id]; ok {
		return run, nil
	}
	return nil, ErrNoProvisionRun
}

func (r *fakeRepo) PinHostKey(_ context.Context, id, key string) (string, bool, error) {
	if r.beforePin != nil {
		r.beforePin()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pinErr != nil {
		return "", false, r.pinErr
	}
	s, ok := r.servers[id]
	if !ok {
		return "", false, ErrServerNotFound
	}
	if s.HostKey != "" {
		return s.HostKey, false, nil
	}
	now := time.Now()
	s.HostKey, s.HostKeyPinnedAt = key, &now
	return key, true, nil
}

func (r *fakeRepo) HostKey(_ context.Context, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	if !ok {
		return "", ErrServerNotFound
	}
	return s.HostKey, nil
}

func (r *fakeRepo) ReplaceHostKey(_ context.Context, id, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.servers[id]
	if !ok {
		return ErrServerNotFound
	}
	now := time.Now()
	s.HostKey, s.HostKeyPinnedAt = key, &now
	r.replaced = append(r.replaced, key)
	return nil
}

func (r *fakeRepo) setHostKey(id, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers[id].HostKey = key
}

func (r *fakeRepo) server(id string) Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.servers[id]
}

func (r *fakeRepo) lastStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.statuses) == 0 {
		return ""
	}
	return r.statuses[len(r.statuses)-1]
}

// recordingConn is the real pool, recording Invalidate calls.
type recordingConn struct {
	*sshpool.Pool
	mu          sync.Mutex
	invalidated []string
}

func (c *recordingConn) Invalidate(id string) {
	c.mu.Lock()
	c.invalidated = append(c.invalidated, id)
	c.mu.Unlock()
	c.Pool.Invalidate(id)
}

func (c *recordingConn) invalidations() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.invalidated...)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

const testServerID = "srv-1"

func serverFor(sshd *sshtest.Server) *Server {
	return &Server{
		ID: testServerID, OrgID: "org-a", Name: "web-1",
		IPAddress: sshd.Host, Port: sshd.Port, User: "root", SSHKey: sshd.ClientKeyPEM,
		Status: StatusActive,
	}
}

// newSSHService returns a service over a real pool to sshd, with operator "op".
func newSSHService(t *testing.T, sshd *sshtest.Server) (*Service, *fakeRepo, *recordingConn) {
	t.Helper()
	repo := newFakeRepo(serverFor(sshd))
	pool := sshpool.New(sshpool.Options{BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
	t.Cleanup(func() { _ = pool.Close() })
	conn := &recordingConn{Pool: pool}
	s := NewService(repo, "ops@example.com")
	s.WithOperatorChecker(fakeOperators{"op": true})
	s.WithSSH(conn, nil)
	return s, repo, conn
}

func authorized(k ssh.PublicKey) string { return sshclient.MarshalHostKey(k) }

func fingerprint(t *testing.T, key string) string {
	t.Helper()
	fp, _, err := sshclient.Fingerprint(key)
	require.NoError(t, err)
	return fp
}

// ─── pinHostKey ───────────────────────────────────────────────────────────────

func TestPinHostKey(t *testing.T) {
	keyA := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	keyB := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	srv := &Server{ID: testServerID, Name: "web-1", IPAddress: "203.0.113.7", Port: 22}

	t.Run("pins when nothing is pinned", func(t *testing.T) {
		repo := newFakeRepo(&Server{ID: testServerID})
		s := NewService(repo, "")
		pinned, err := s.pinHostKey(srv)(context.Background(), keyA)
		require.NoError(t, err)
		assert.True(t, pinned)
		assert.Equal(t, keyA, repo.server(testServerID).HostKey)
	})

	t.Run("accepts a concurrent winner with the same key", func(t *testing.T) {
		repo := newFakeRepo(&Server{ID: testServerID})
		repo.beforePin = func() { repo.setHostKey(testServerID, keyA+" root@web-1") }
		s := NewService(repo, "")
		pinned, err := s.pinHostKey(srv)(context.Background(), keyA)
		require.NoError(t, err)
		assert.False(t, pinned, "the other connection pinned it")
	})

	t.Run("refuses when a different key won", func(t *testing.T) {
		repo := newFakeRepo(&Server{ID: testServerID, HostKey: keyB})
		s := NewService(repo, "")
		pinned, err := s.pinHostKey(srv)(context.Background(), keyA)
		assert.False(t, pinned)
		var mismatch *sshclient.HostKeyMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, "203.0.113.7:22", mismatch.Addr)
		assert.Equal(t, fingerprint(t, keyB), mismatch.Expected)
		assert.Equal(t, fingerprint(t, keyA), mismatch.Presented)
		assert.Equal(t, keyB, repo.server(testServerID).HostKey, "the pin is untouched")
	})

	t.Run("repository errors pass through", func(t *testing.T) {
		repo := newFakeRepo(&Server{ID: testServerID})
		repo.pinErr = errors.New("db down")
		s := NewService(repo, "")
		_, err := s.pinHostKey(srv)(context.Background(), keyA)
		assert.ErrorContains(t, err, "db down")
	})
}

// ─── TestConnection, Provision and the pooled connection ─────────────────────

func TestTestConnectionPinsOnFirstUseAndReusesTheConnection(t *testing.T) {
	sshd := sshtest.New(t)
	s, repo, _ := newSSHService(t, sshd)
	want := authorized(sshd.HostKey())

	res, err := s.TestConnection(context.Background(), testServerID)
	require.NoError(t, err)
	assert.True(t, res.HostKeyNewlyPinned)
	assert.Equal(t, fingerprint(t, want), res.HostKeyFingerprint)
	assert.Equal(t, ssh.KeyAlgoED25519, res.HostKeyType)
	assert.Equal(t, want, repo.server(testServerID).HostKey)
	assert.NotNil(t, repo.server(testServerID).HostKeyPinnedAt)
	assert.Equal(t, StatusActive, repo.lastStatus())

	res, err = s.TestConnection(context.Background(), testServerID)
	require.NoError(t, err)
	assert.False(t, res.HostKeyNewlyPinned, "already pinned")
	assert.Equal(t, fingerprint(t, want), res.HostKeyFingerprint)
	assert.Equal(t, 1, sshd.Accepts(), "the second test reuses the pooled connection")
}

func TestTestConnectionHostKeyMismatchMarksOffline(t *testing.T) {
	sshd := sshtest.New(t)
	s, repo, _ := newSSHService(t, sshd)
	_, err := s.TestConnection(context.Background(), testServerID)
	require.NoError(t, err)
	oldFP := fingerprint(t, repo.server(testServerID).HostKey)

	// The server is rebuilt: new host key, connections dropped.
	rebuilt := sshtest.NewEd25519Signer(t)
	sshd.SetHostKey(rebuilt)
	sshd.DropAll()
	attempts := sshd.AuthAttempts()

	_, err = s.TestConnection(context.Background(), testServerID)
	require.Error(t, err)
	var mismatch *sshclient.HostKeyMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Contains(t, err.Error(), oldFP)
	assert.Contains(t, err.Error(), fingerprint(t, authorized(rebuilt.PublicKey())))
	assert.Contains(t, err.Error(), "Servers → web-1 → SSH host key", "says what to do")
	assert.Contains(t, err.Error(), "intercepting")
	assert.Equal(t, StatusOffline, repo.lastStatus())
	assert.Equal(t, attempts, sshd.AuthAttempts(), "no credentials sent to the changed server")
	assert.Equal(t, oldFP, fingerprint(t, repo.server(testServerID).HostKey), "the pin is untouched")
}

// provisionedHost answers the provisioner's checks like a server that has
// been provisioned before, and records every command.
type provisionedHost struct {
	mu   sync.Mutex
	cmds []string
}

func (p *provisionedHost) answer(cmd string) string {
	p.mu.Lock()
	p.cmds = append(p.cmds, cmd)
	p.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, requirementsMarker):
		return strings.Replace(goodHost, "docker:\n", "docker:29.1.0\n", 1)
	case strings.HasPrefix(cmd, "docker --version"):
		return "Docker version 29.1.0, build 1234567"
	case strings.HasPrefix(cmd, "docker plugin ls"):
		return "loki:latest"
	case strings.HasPrefix(cmd, "docker network ls"):
		return "paas-network"
	case strings.HasPrefix(cmd, "docker ps --filter name=paas-traefik"):
		return "Up 3 days"
	case strings.HasPrefix(cmd, "docker container inspect"):
		_, hash, _ := traefikConfig("ops@example.com", nil, false) // started with this configuration
		return hash + "|" + traefikAcmeVolume + ","
	case strings.HasPrefix(cmd, "command -v ufw"), strings.HasPrefix(cmd, "command -v unattended-upgrade"),
		strings.HasPrefix(cmd, "command -v nft"):
		return "yes"
	case strings.HasPrefix(cmd, "sha256sum "+hostFirewallRulesPath):
		return sha256Hex(hostFirewallRules)
	case strings.HasPrefix(cmd, "sha256sum "+hostFirewallUnitPath):
		return sha256Hex(hostFirewallUnit)
	case strings.HasPrefix(cmd, "sha256sum "+backupScriptPath):
		return sha256Hex(backupScript)
	case strings.HasPrefix(cmd, "sha256sum "+backupServicePath):
		return sha256Hex(backupServiceUnit)
	case strings.HasPrefix(cmd, "sha256sum "+backupTimerPath):
		return sha256Hex(backupTimerUnit)
	case strings.HasPrefix(cmd, "systemctl is-enabled toft-backup.timer"):
		return "enabled"
	case strings.HasPrefix(cmd, "systemctl is-enabled toft-firewall"):
		return "enabled"
	case strings.HasPrefix(cmd, "nft list table ip toft"):
		return "loaded"
	case strings.HasPrefix(cmd, "ufw status"):
		return "Status: active"
	case strings.HasPrefix(cmd, "systemctl is-active fail2ban"):
		return "active"
	}
	return ""
}

func (p *provisionedHost) commands() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.cmds...)
}

// exec answers an sshtest session. It reads the rules nft checks, as nft
// does: exiting first fails the client's write of them (EOF) at random,
// which now shows as a host firewall warning.
func (p *provisionedHost) exec(sess *sshtest.Session) int {
	if strings.HasPrefix(sess.Command, "nft -c -f -") {
		_, _ = io.Copy(io.Discard, sess.Stdin)
	}
	if out := p.answer(sess.Command); out != "" {
		_, _ = sess.Stdout.Write([]byte(out + "\n"))
	}
	return 0
}

func TestProvisionPinsTheHostKeyAndProvisions(t *testing.T) {
	host := &provisionedHost{}
	sshd := sshtest.New(t, sshtest.WithExec(host.exec))
	s, repo, _ := newSSHService(t, sshd)

	var lines []string
	err := s.Provision(context.Background(), testServerID, "user-1", func(l string) { lines = append(lines, l) })
	require.NoError(t, err)

	// The run is recorded with every line it streamed (D-48).
	run, err := s.GetProvisionRun(context.Background(), testServerID)
	require.NoError(t, err)
	assert.Equal(t, "user-1", run.StartedBy)
	assert.Equal(t, strings.Join(lines, "\n")+"\n", run.Log)
	require.NotNil(t, run.OK)
	assert.True(t, *run.OK)

	fp := fingerprint(t, authorized(sshd.HostKey()))
	assert.Equal(t, "Connecting via SSH...", lines[0])
	assert.Contains(t, lines, "Pinned SSH host key ssh-ed25519 "+fp+" (first connection)")
	assert.Contains(t, lines, "Server provisioned successfully!")
	assert.NotNil(t, repo.server(testServerID).ProvisionedAt)
	assert.Equal(t, StatusActive, repo.lastStatus())
	assert.NotEmpty(t, host.commands())
}

// A server whose ufw refuses (the drill's non-root user) is provisioned all
// the same (D-37), and its run is recorded ok with the warning naming what
// is missing, not as a clean success.
func TestProvisionRecordsWarningsWhenTheFirewallFails(t *testing.T) {
	host := &provisionedHost{}
	sshd := sshtest.New(t, sshtest.WithExec(func(sess *sshtest.Session) int {
		switch {
		case strings.HasPrefix(sess.Command, "ufw status"):
			_, _ = sess.Stdout.Write([]byte("Status: inactive\n"))
			return 0
		case strings.HasPrefix(sess.Command, "ufw allow"):
			_, _ = sess.Stderr.Write([]byte("ERROR: You need to be root to run this script\n"))
			return 1
		}
		return host.exec(sess)
	}))
	s, repo, _ := newSSHService(t, sshd)

	var lines []string
	require.NoError(t, s.Provision(context.Background(), testServerID, "user-1", func(l string) { lines = append(lines, l) }))

	run, err := s.GetProvisionRun(context.Background(), testServerID)
	require.NoError(t, err)
	require.NotNil(t, run.OK)
	assert.True(t, *run.OK)
	assert.Equal(t, []string{"the firewall (ufw, fail2ban)"}, run.Warnings)
	assert.NotContains(t, lines, "Server provisioned successfully!")
	assert.Equal(t, "Server provisioned with warnings, missing the firewall (ufw, fail2ban): fix what the warnings above say, then Re-provision", lines[len(lines)-1])
	assert.NotNil(t, repo.server(testServerID).ProvisionedAt, "the server stays usable")
	assert.Equal(t, StatusActive, repo.lastStatus())
}

func TestProvisionKeepsRunningWhenTheCallerGoesAway(t *testing.T) {
	host := &provisionedHost{}
	sshd := sshtest.New(t, sshtest.WithExec(func(sess *sshtest.Session) int {
		if out := host.answer(sess.Command); out != "" {
			_, _ = sess.Stdout.Write([]byte(out + "\n"))
		}
		return 0
	}))
	s, repo, _ := newSSHService(t, sshd)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the browser stopped watching before the install started

	require.NoError(t, s.Provision(ctx, testServerID, "", func(string) {}))
	assert.NotNil(t, repo.server(testServerID).ProvisionedAt)
	assert.Equal(t, StatusActive, repo.lastStatus())
}

func TestProvisionHostKeyMismatchIsReported(t *testing.T) {
	sshd := sshtest.New(t)
	s, repo, _ := newSSHService(t, sshd)
	repo.setHostKey(testServerID, authorized(sshtest.NewEd25519Signer(t).PublicKey()))

	err := s.Provision(context.Background(), testServerID, "", func(string) {})
	require.ErrorIs(t, err, sshclient.ErrHostKeyMismatch)
	assert.Contains(t, err.Error(), "SSH host key")
	assert.Equal(t, StatusOffline, repo.lastStatus())

	// The failure is the recorded run's outcome.
	run, rerr := s.GetProvisionRun(context.Background(), testServerID)
	require.NoError(t, rerr)
	require.NotNil(t, run.OK)
	assert.False(t, *run.OK)
	assert.Equal(t, err.Error(), run.Error)
}

func TestSSHWithARowReadBeforeThePinStillEnforcesIt(t *testing.T) {
	// A deploy loaded the server before its first pin; metrics then pinned
	// the real key. When the deploy dials again and an impostor answers, the
	// stored pin is enforced before any credential is sent.
	impostor := sshtest.New(t)
	s, repo, _ := newSSHService(t, impostor)
	stale := repo.server(testServerID) // HostKey ""
	repo.setHostKey(testServerID, authorized(sshtest.NewEd25519Signer(t).PublicKey()))

	_, err := s.SSH(&stale).Exec(context.Background(), "echo hi")
	require.ErrorIs(t, err, sshclient.ErrHostKeyMismatch)
	assert.Zero(t, impostor.AuthAttempts(), "refused before authentication")
	assert.NotEqual(t, authorized(impostor.HostKey()), repo.server(testServerID).HostKey, "the pin is untouched")
}

func TestDeleteServerInvalidatesThePool(t *testing.T) {
	sshd := sshtest.New(t)
	s, repo, conn := newSSHService(t, sshd)
	// Ensure rather than TestConnection, whose background capacity
	// discovery could dial again after the delete.
	srv := repo.server(testServerID)
	_, err := s.SSH(&srv).Ensure(context.Background(), sshpool.EnsureOpts{})
	require.NoError(t, err)
	require.Equal(t, 1, sshd.Conns())

	require.NoError(t, s.DeleteServer(context.Background(), testServerID, false))
	assert.Equal(t, []string{testServerID}, conn.invalidations())
	assert.Eventually(t, func() bool { return sshd.Conns() == 0 }, 2*time.Second, 10*time.Millisecond,
		"the pooled connection is closed")
}

// blockingExec runs "block ..." (ExecOnServer appends 2>&1) until the
// session closes or a signal arrives; anything else goes to DefaultExec.
func blockingExec(sshd **sshtest.Server) sshtest.Option {
	return sshtest.WithExec(func(sess *sshtest.Session) int {
		if strings.HasPrefix(sess.Command, "block") {
			select {
			case <-sess.Done:
				return 129
			case <-sess.Signals:
				return 143
			}
		}
		return (*sshd).DefaultExec(sess)
	})
}

func TestExecOnServerStopsWhenTheCallerGoesAway(t *testing.T) {
	var sshd *sshtest.Server
	sshd = sshtest.New(t, blockingExec(&sshd))
	s, _, _ := newSSHService(t, sshd)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ExecOnServer(ctx, "op", testServerID, "block", func(string) {}) }()

	require.Eventually(t, func() bool { return sshd.Running() == 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("ExecOnServer did not return after cancel")
	}
	assert.Eventually(t, func() bool { return sshd.Running() == 0 }, 2*time.Second, 10*time.Millisecond,
		"the remote command was stopped")
	assert.Eventually(t, func() bool { return len(sshd.Signals()) == 1 && sshd.Signals()[0] == "TERM" },
		2*time.Second, 10*time.Millisecond, "it was sent SIGTERM")
}

func TestOpenPTYCloseKeepsTheConnection(t *testing.T) {
	sshd := sshtest.New(t)
	s, _, _ := newSSHService(t, sshd)
	pty, err := s.OpenPTY(context.Background(), "op", testServerID, 80, 24)
	require.NoError(t, err)
	pty.Close()

	_, err = s.GetSystemStats(context.Background(), testServerID) // any call on the same connection
	assert.NotErrorIs(t, err, sshpool.ErrPoolClosed)
	assert.Equal(t, 1, sshd.Accepts(), "closing the PTY left the pooled connection open")
}

func TestSSHWithoutConnectorFailsClosed(t *testing.T) {
	s := NewService(newFakeRepo(), "")
	_, err := s.SSH(&Server{ID: "x", Name: "x"}).Exec(context.Background(), "true")
	assert.ErrorIs(t, err, ErrSSHNotConfigured)
	_, err = s.SSH(&Server{ID: "x", Name: "x"}).DockerClient()
	assert.NoError(t, err, "the client is lazy; its dials fail")
}

// ─── Scan and reset ───────────────────────────────────────────────────────────

func TestScanHostKey(t *testing.T) {
	sshd := sshtest.New(t)
	s, repo, _ := newSSHService(t, sshd)
	presented := authorized(sshd.HostKey())

	res, err := s.ScanHostKey(context.Background(), testServerID)
	require.NoError(t, err)
	assert.Equal(t, fingerprint(t, presented), res.PresentedFingerprint)
	assert.Equal(t, ssh.KeyAlgoED25519, res.KeyType)
	assert.Nil(t, res.PinnedFingerprint, "nothing pinned yet")
	assert.False(t, res.MatchesPinned)
	assert.Zero(t, sshd.AuthAttempts(), "a scan sends no credentials")

	repo.setHostKey(testServerID, presented)
	res, err = s.ScanHostKey(context.Background(), testServerID)
	require.NoError(t, err)
	require.NotNil(t, res.PinnedFingerprint)
	assert.Equal(t, res.PresentedFingerprint, *res.PinnedFingerprint)
	assert.True(t, res.MatchesPinned)

	sshd.SetHostKey(sshtest.NewEd25519Signer(t))
	res, err = s.ScanHostKey(context.Background(), testServerID)
	require.NoError(t, err)
	assert.Equal(t, fingerprint(t, presented), *res.PinnedFingerprint)
	assert.NotEqual(t, *res.PinnedFingerprint, res.PresentedFingerprint)
	assert.False(t, res.MatchesPinned)
}

func TestScanHostKeyUnreachableIsAnSSHFailure(t *testing.T) {
	s := NewService(newFakeRepo(&Server{ID: testServerID, Name: "web-1", IPAddress: "127.0.0.1", Port: 1}), "")
	_, err := s.ScanHostKey(context.Background(), testServerID)
	assert.ErrorIs(t, err, ErrSSHFailed)
}

func TestResetHostKey(t *testing.T) {
	oldKey := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	newKey := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	newFP := fingerprint(t, newKey)

	setup := func(t *testing.T, pinned, presented string) (*Service, *fakeRepo, *recordingConn) {
		repo := newFakeRepo(&Server{ID: testServerID, Name: "web-1", IPAddress: "203.0.113.7", Port: 22, HostKey: pinned})
		pool := sshpool.New(sshpool.Options{})
		t.Cleanup(func() { _ = pool.Close() })
		conn := &recordingConn{Pool: pool}
		s := NewService(repo, "")
		s.WithOperatorChecker(fakeOperators{"op": true})
		s.WithSSH(conn, nil)
		s.scan = func(_ context.Context, cfg sshclient.ScanConfig) (ssh.PublicKey, error) {
			assert.Equal(t, pinned, cfg.Prefer, "the pinned key's type is negotiated first")
			return sshclient.ParseHostKey(presented)
		}
		return s, repo, conn
	}

	t.Run("replaces a changed key and closes the old connections", func(t *testing.T) {
		s, repo, conn := setup(t, oldKey, newKey)
		srv, err := s.ResetHostKey(context.Background(), "op", testServerID, " "+newFP+" ")
		require.NoError(t, err)
		assert.Equal(t, newKey, srv.HostKey)
		assert.Equal(t, []string{newKey}, repo.replaced)
		assert.Equal(t, []string{testServerID}, conn.invalidations())
	})

	t.Run("pins a server that has no pin yet (verify first)", func(t *testing.T) {
		s, repo, conn := setup(t, "", newKey)
		_, err := s.ResetHostKey(context.Background(), "op", testServerID, newFP)
		require.NoError(t, err)
		assert.Equal(t, []string{newKey}, repo.replaced)
		assert.Equal(t, []string{testServerID}, conn.invalidations())
	})

	t.Run("re-confirming the pinned key keeps the connections", func(t *testing.T) {
		s, repo, conn := setup(t, newKey, newKey)
		_, err := s.ResetHostKey(context.Background(), "op", testServerID, newFP)
		require.NoError(t, err)
		assert.Equal(t, []string{newKey}, repo.replaced)
		assert.Empty(t, conn.invalidations())
	})

	t.Run("a different presented key is a conflict and pins nothing", func(t *testing.T) {
		s, repo, conn := setup(t, oldKey, newKey)
		confirmed := fingerprint(t, authorized(sshtest.NewEd25519Signer(t).PublicKey()))
		_, err := s.ResetHostKey(context.Background(), "op", testServerID, confirmed)
		var changed *HostKeyChangedError
		require.ErrorAs(t, err, &changed)
		assert.Equal(t, newFP, changed.Presented)
		assert.Equal(t, confirmed, changed.Confirmed)
		assert.Equal(t, ssh.KeyAlgoED25519, changed.KeyType)
		assert.Empty(t, repo.replaced)
		assert.Empty(t, conn.invalidations())
	})

	t.Run("operators only", func(t *testing.T) {
		s, repo, _ := setup(t, oldKey, newKey)
		_, err := s.ResetHostKey(context.Background(), "owner", testServerID, newFP)
		assert.ErrorIs(t, err, ErrHostKeyResetOperatorOnly)
		s.WithOperatorChecker(nil)
		_, err = s.ResetHostKey(context.Background(), "op", testServerID, newFP)
		assert.ErrorIs(t, err, ErrHostKeyResetOperatorOnly, "no checker: refused")
		assert.Empty(t, repo.replaced)
	})

	t.Run("a malformed fingerprint is refused before scanning", func(t *testing.T) {
		s, _, _ := setup(t, oldKey, newKey)
		s.scan = func(context.Context, sshclient.ScanConfig) (ssh.PublicKey, error) {
			t.Fatal("scanned")
			return nil, nil
		}
		for _, fp := range []string{"", "MD5:aa:bb", "SHA256:short", strings.TrimPrefix(newFP, "SHA256:")} {
			_, err := s.ResetHostKey(context.Background(), "op", testServerID, fp)
			assert.ErrorIs(t, err, ErrInvalidFingerprint, fp)
		}
	})

	t.Run("an unreachable server is an SSH failure", func(t *testing.T) {
		s, repo, _ := setup(t, oldKey, newKey)
		s.scan = func(context.Context, sshclient.ScanConfig) (ssh.PublicKey, error) {
			return nil, errors.New("connection refused")
		}
		_, err := s.ResetHostKey(context.Background(), "op", testServerID, newFP)
		assert.ErrorIs(t, err, ErrSSHFailed)
		assert.Empty(t, repo.replaced)
	})
}

func TestResetHostKeyThenConnectUsesTheNewKey(t *testing.T) {
	sshd := sshtest.New(t)
	s, repo, _ := newSSHService(t, sshd)
	_, err := s.TestConnection(context.Background(), testServerID)
	require.NoError(t, err)

	rebuilt := sshtest.NewEd25519Signer(t)
	sshd.SetHostKey(rebuilt)
	sshd.DropAll()
	_, err = s.TestConnection(context.Background(), testServerID)
	require.ErrorIs(t, err, sshclient.ErrHostKeyMismatch)

	scan, err := s.ScanHostKey(context.Background(), testServerID)
	require.NoError(t, err)
	require.False(t, scan.MatchesPinned)
	_, err = s.ResetHostKey(context.Background(), "op", testServerID, scan.PresentedFingerprint)
	require.NoError(t, err)

	res, err := s.TestConnection(context.Background(), testServerID)
	require.NoError(t, err, "the reset cleared the backoff and the new pin connects")
	assert.Equal(t, scan.PresentedFingerprint, res.HostKeyFingerprint)
	assert.False(t, res.HostKeyNewlyPinned, "pinned by the operator, not by trust on first use")
	assert.Equal(t, authorized(rebuilt.PublicKey()), repo.server(testServerID).HostKey)
}

// ─── DTOs and hints ───────────────────────────────────────────────────────────

func TestToResponseShowsTheFingerprintNotTheKey(t *testing.T) {
	key := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	pinnedAt := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	r := toResponse(&Server{ID: "s", HostKey: key, HostKeyPinnedAt: &pinnedAt, SSHKey: "PRIVATE"})
	require.NotNil(t, r.HostKeyFingerprint)
	assert.Equal(t, fingerprint(t, key), *r.HostKeyFingerprint)
	assert.Equal(t, ssh.KeyAlgoED25519, *r.HostKeyType)
	assert.Equal(t, &pinnedAt, r.HostKeyPinnedAt)

	body, err := json.Marshal(r)
	require.NoError(t, err)
	keyBlob := strings.Fields(key)[1]
	assert.NotContains(t, string(body), keyBlob, "the raw key is never sent")
	assert.NotContains(t, string(body), "PRIVATE")
	assert.Contains(t, string(body), `"host_key_fingerprint":"SHA256:`)

	unpinned, err := json.Marshal(toResponse(&Server{ID: "s"}))
	require.NoError(t, err)
	assert.Contains(t, string(unpinned), `"host_key_type":null`)
	assert.Contains(t, string(unpinned), `"host_key_fingerprint":null`)
	assert.Contains(t, string(unpinned), `"host_key_pinned_at":null`)

	// The model itself never serialises the key either.
	model, err := json.Marshal(Server{HostKey: key})
	require.NoError(t, err)
	assert.NotContains(t, string(model), keyBlob)
}

func TestHostKeyHint(t *testing.T) {
	assert.NoError(t, hostKeyHint("web-1", nil))
	other := errors.New("connection refused")
	assert.Equal(t, other, hostKeyHint("web-1", other))

	mismatch := &sshclient.HostKeyMismatchError{Addr: "203.0.113.7:22", Expected: "SHA256:a", Presented: "SHA256:b"}
	err := hostKeyHint("web-1", &sshpool.UnreachableError{ServerID: "s", Name: "web-1", LastErr: mismatch})
	var got *sshclient.HostKeyMismatchError
	require.ErrorAs(t, err, &got)
	assert.ErrorIs(t, err, sshpool.ErrUnreachable)
	assert.Contains(t, err.Error(), "Servers → web-1 → SSH host key")
}

func TestValidFingerprint(t *testing.T) {
	key := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	fp := fingerprint(t, key)
	assert.True(t, validFingerprint(fp))
	assert.True(t, validFingerprint(fp+"="), "padding tolerated")
	assert.False(t, validFingerprint(""))
	assert.False(t, validFingerprint("SHA256:"))
	assert.False(t, validFingerprint("sha256:"+strings.TrimPrefix(fp, "SHA256:")))
	assert.False(t, validFingerprint("SHA256:not base64!"))
}

func TestParseSystemStatsReadsUptimeAndTraefik(t *testing.T) {
	stats, err := parseSystemStats("cpu:3.5\nmem:900 1900\ndocker:29.1.3\nuptime_s:1814400\ntraefik:running traefik:v3.6.10\n")
	require.NoError(t, err)
	assert.Equal(t, int64(1814400), stats.UptimeSeconds)
	assert.Equal(t, "running", stats.TraefikStatus)
	assert.Equal(t, "traefik:v3.6.10", stats.TraefikImage)

	// No paas-traefik container: nothing is claimed.
	stats, err = parseSystemStats("cpu:1\ntraefik:\n")
	require.NoError(t, err)
	assert.Empty(t, stats.TraefikStatus)
}
