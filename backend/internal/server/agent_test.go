package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshclient/sshtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// fakeAgents is an in-memory AgentStore.
type fakeAgents struct {
	mu     sync.Mutex
	recs   map[string]*AgentRecord
	tokens map[string]*fakeToken
	// spec and observed are every server's generations; drift is the last
	// answer recorded.
	spec, observed int64
	drift          []byte
	// moved, when set, sees SetConnection (the servers row it changes).
	moved func(serverID, connection string)
	// strict is each server's strict services (D-115).
	strict            map[string][]NamedService
	sandboxedPreviews []string
}

type fakeToken struct {
	server   string
	replaces bool
	by       string
	expires  time.Time
	used     bool
}

func newFakeAgents() *fakeAgents {
	return &fakeAgents{recs: map[string]*AgentRecord{}, tokens: map[string]*fakeToken{}}
}

func (f *fakeAgents) rec(id string) *AgentRecord {
	if f.recs[id] == nil {
		f.recs[id] = &AgentRecord{Connection: ConnectionAgent}
	}
	return f.recs[id]
}

func (f *fakeAgents) CreateAgentServer(context.Context, string, string, string, string, bool) (string, error) {
	return "", nil
}
func (f *fakeAgents) NextServerNumber(context.Context, string) (int, error) { return 1, nil }
func (f *fakeAgents) FindWaitingServer(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeAgents) LastJoinTokenCreator(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tokens {
		if t.server == id && t.used {
			return t.by, nil
		}
	}
	return "", nil
}
func (f *fakeAgents) WaitingSSHKey(context.Context, string) (string, error) { return "", nil }
func (f *fakeAgents) SetWaitingSSHKey(context.Context, string, string) (bool, error) {
	return true, nil
}
func (f *fakeAgents) UseSSH(context.Context, string, string, int, string) error      { return nil }
func (f *fakeAgents) DeleteUnjoinedServers(context.Context) ([]RemovedServer, error) { return nil, nil }
func (f *fakeAgents) CreateJoinToken(_ context.Context, id string, h []byte, rep bool, by string, exp time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tokens {
		if t.server == id {
			t.expires = time.Now()
		}
	}
	f.tokens[string(h)] = &fakeToken{server: id, replaces: rep, by: by, expires: exp}
	return nil
}
func (f *fakeAgents) JoinWithToken(_ context.Context, h []byte, key string) (string, bool, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tokens[string(h)]
	if t == nil || t.used || !time.Now().Before(t.expires) {
		return "", false, "", false, ErrUnknownToken
	}
	r := f.rec(t.server)
	if r.Key != "" && !t.replaces {
		return t.server, t.replaces, t.by, false, nil
	}
	t.used = true
	r.Key, r.NextKey, r.RevokedAt = key, "", nil
	return t.server, t.replaces, t.by, true, nil
}
func (f *fakeAgents) PeekJoinToken(_ context.Context, h []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tokens[string(h)]
	if t == nil || t.used || !time.Now().Before(t.expires) {
		return "", ErrUnknownToken
	}
	return t.server, nil
}
func (f *fakeAgents) PinAgentKey(_ context.Context, id, key string, replace bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rec(id)
	if r.Key != "" && !replace {
		return false, nil
	}
	r.Key, r.NextKey, r.RevokedAt = key, "", nil
	return true, nil
}
func (f *fakeAgents) SetAgentNextKey(_ context.Context, id, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec(id).NextKey = key
	return nil
}
func (f *fakeAgents) PromoteAgentNextKey(_ context.Context, id, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rec(id)
	if r.NextKey == "" || r.NextKey != key {
		return false, nil
	}
	r.Key, r.NextKey = r.NextKey, ""
	return true, nil
}
func (f *fakeAgents) RevokeAgent(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rec(id)
	now := time.Now()
	r.Key, r.NextKey, r.RevokedAt = "", "", &now
	return true, nil
}
func (f *fakeAgents) AgentKey(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rec(id).Key, nil
}
func (f *fakeAgents) RecordAgentConnected(context.Context, string, string, string, string, string) error {
	return nil
}
func (f *fakeAgents) TouchAgentSeen(context.Context, string) error { return nil }
func (f *fakeAgents) GetAgent(_ context.Context, id string) (*AgentRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recs[id] == nil {
		return nil, ErrServerNotFound
	}
	r := *f.recs[id]
	return &r, nil
}
func (f *fakeAgents) ListAgents(context.Context, string) (map[string]AgentRecord, error) {
	return nil, nil
}
func (f *fakeAgents) SetConnection(_ context.Context, serverID, connection string) error {
	if f.moved != nil {
		f.moved(serverID, connection)
	}
	return nil
}
func (f *fakeAgents) StrictServices(_ context.Context, serverID string) ([]NamedService, error) {
	return f.strict[serverID], nil
}
func (f *fakeAgents) SandboxStrictPreviews(_ context.Context, serverID string) error {
	f.sandboxedPreviews = append(f.sandboxedPreviews, serverID)
	return nil
}
func (f *fakeAgents) SetSSHCredentials(context.Context, string, string, int, string) error {
	return nil
}
func (f *fakeAgents) SetAddressIfEmpty(context.Context, string, string) error { return nil }
func (f *fakeAgents) ListAgentServerIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.recs))
	for id, r := range f.recs {
		if r.Key != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (f *fakeAgents) PanelAgentKey(context.Context) (string, error) { return "", nil }
func (f *fakeAgents) PanelEpoch(context.Context) (string, error)    { return "epoch-1", nil }
func (f *fakeAgents) AgentGeneration(context.Context, string) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spec, f.observed, nil
}
func (f *fakeAgents) RecordAgentObserved(_ context.Context, id string, gen int64, drift []byte, routes *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed, f.drift = min(gen, f.spec), drift
	r := f.rec(id)
	r.Routes, r.FrozenAt, r.Frozen = routes, nil, nil
	return nil
}
func (f *fakeAgents) RecordAgentFrozen(_ context.Context, id string, running []byte, routes *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rec(id)
	if r.FrozenAt == nil {
		now := time.Now()
		r.FrozenAt = &now
	}
	r.Routes, r.Frozen = routes, running
	return nil
}
func (f *fakeAgents) AgentsFrozen(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for id, r := range f.recs {
		if r.Key != "" {
			out[id] = r.FrozenAt != nil
		}
	}
	return out, nil
}

// fakeHub reports a fixed state.
type fakeHub struct {
	state   agenthub.State
	known   bool
	started time.Time
	dropped []string
}

func (h *fakeHub) Conn(context.Context, string) (net.Conn, error) { return nil, agenthub.ErrOffline }
func (h *fakeHub) State(string) (agenthub.State, bool)            { return h.state, h.known }
func (h *fakeHub) Started() time.Time                             { return h.started }
func (h *fakeHub) MarkUpgrading(string)                           {}
func (h *fakeHub) Drop(id string)                                 { h.dropped = append(h.dropped, id) }

func agentKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	k, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return sshclient.MarshalHostKey(k)
}

func agentService(t *testing.T) (*Service, *fakeAgents, *fakeHub) {
	t.Helper()
	svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1"}), "")
	store, hub := newFakeAgents(), &fakeHub{started: time.Now().Add(-time.Hour)}
	svc.WithAgents(store, hub, nil, t.TempDir())
	store.rec("s1")
	return svc, store, hub
}

// A server whose services run with a strict network keeps its agent: only
// the agent answers their name lookups (D-115). Refused before SSH is tried.
func TestMoveToSSHRefusedWhileStrictServicesRun(t *testing.T) {
	svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1", Connection: ConnectionAgent, IPAddress: "192.0.2.1", SSHKey: "key"}), "")
	store := newFakeAgents()
	store.strict = map[string][]NamedService{"s1": {{ID: "svc-1", Name: "api"}, {ID: "svc-2", Name: "pgadmin"}}}
	store.moved = func(string, string) { t.Fatal("the connection changed") }
	svc.WithAgents(store, &fakeHub{started: time.Now()}, nil, t.TempDir())

	err := svc.MoveToSSH(context.Background(), "u1", "s1", "", 0, "")
	var strict *StrictInUseError
	require.ErrorAs(t, err, &strict)
	require.ErrorIs(t, err, ErrStrictInUse)
	assert.Equal(t, []NamedService{{ID: "svc-1", Name: "api"}, {ID: "svc-2", Name: "pgadmin"}}, strict.Services)
}

// Moved to SSH, a server's untrusted preview copies go to Sandboxed: no
// agent answers a strict container's names there (D-115).
func TestMoveToSSHSandboxesStrictPreviews(t *testing.T) {
	sshd := sshtest.New(t)
	svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1", Connection: ConnectionAgent, IPAddress: "127.0.0.1", Port: sshd.Port, User: "root", SSHKey: sshd.ClientKeyPEM}), "")
	store := newFakeAgents()
	var moved string
	store.moved = func(_, to string) { moved = to }
	svc.WithAgents(store, &fakeHub{started: time.Now()}, nil, t.TempDir())

	require.NoError(t, svc.MoveToSSH(context.Background(), "u1", "s1", "", 0, ""))
	assert.Equal(t, ConnectionSSH, moved)
	assert.Equal(t, []string{"s1"}, store.sandboxedPreviews)
}

func TestJoinTokenPinsTheAgentOnce(t *testing.T) {
	svc, store, _ := agentService(t)
	ctx := context.Background()
	tok, rejoin, err := svc.CreateJoinToken(ctx, "s1", "u1")
	require.NoError(t, err)
	assert.False(t, rejoin)
	key := agentKey(t)

	id, err := svc.AdmitAgent(ctx, agenthub.Hello{Token: tok.Token, Key: key})
	require.NoError(t, err)
	assert.Equal(t, "s1", id)
	assert.Equal(t, key, store.rec("s1").Key)

	// Single use: another key with the used token is refused, while the
	// joined agent, still sending it, gets in by its pinned key.
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Token: tok.Token, Key: agentKey(t)})
	assert.ErrorIs(t, err, agenthub.ErrRevoked)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{Token: tok.Token, Key: agentKey(t)})
	assert.ErrorIs(t, err, agenthub.ErrUnknown)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Token: tok.Token, Key: key})
	assert.NoError(t, err)
	// From now on the pinned key gets in without a token; another doesn't.
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: key})
	assert.NoError(t, err)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: agentKey(t)})
	assert.ErrorIs(t, err, agenthub.ErrRevoked)
	// A first-join token can't replace a pinned key.
	tok2, err := svc.issueJoinToken(ctx, "s1", false, "u1")
	require.NoError(t, err)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Token: tok2.Token, Key: agentKey(t)})
	assert.Error(t, err)
	assert.Equal(t, key, store.rec("s1").Key)
}

func TestRejoinReplacesTheKeyAndShutsOutTheOldAgent(t *testing.T) {
	svc, store, _ := agentService(t)
	ctx := context.Background()
	old := agentKey(t)
	_, _ = store.PinAgentKey(ctx, "s1", old, false)
	tok, rejoin, err := svc.CreateJoinToken(ctx, "s1", "u1")
	require.NoError(t, err)
	assert.True(t, rejoin, "a token for a server with a pinned key is a rejoin")
	fresh := agentKey(t)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{Token: tok.Token, Key: fresh})
	require.NoError(t, err)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: old})
	assert.ErrorIs(t, err, agenthub.ErrRevoked)
}

func TestRevokedAgentIsRefused(t *testing.T) {
	svc, store, _ := agentService(t)
	ctx := context.Background()
	key := agentKey(t)
	_, _ = store.PinAgentKey(ctx, "s1", key, false)
	_, _ = store.RevokeAgent(ctx, "s1")
	_, err := svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: key})
	assert.ErrorIs(t, err, agenthub.ErrRevoked)
	// An unknown server, or a key that isn't ed25519, is just unknown.
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "nope", Key: key})
	assert.ErrorIs(t, err, agenthub.ErrUnknown)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: "ssh-rsa AAAA"})
	assert.ErrorIs(t, err, agenthub.ErrUnknown)
}

// A rotation's answer can be lost: until the agent connects with one of
// its keys both get in, and the new one, once presented, replaces the old.
func TestRotationAdmitsEitherKeyUntilTheAgentPicks(t *testing.T) {
	svc, store, _ := agentService(t)
	ctx := context.Background()
	old, next := agentKey(t), agentKey(t)
	_, _ = store.PinAgentKey(ctx, "s1", old, false)
	require.NoError(t, store.SetAgentNextKey(ctx, "s1", next))

	_, err := svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: old})
	require.NoError(t, err, "the agent never switched")
	assert.Equal(t, old, store.rec("s1").Key)

	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: next})
	require.NoError(t, err, "the agent switched though the panel didn't hear it")
	assert.Equal(t, next, store.rec("s1").Key)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{ServerID: "s1", Key: old})
	assert.ErrorIs(t, err, agenthub.ErrRevoked)
}

// The offline alert samples an agent as away only once it has been gone 5
// minutes, both after it left and after the panel started.
func TestAgentsAwayWaitsFiveMinutes(t *testing.T) {
	svc, store, hub := agentService(t)
	ctx := context.Background()
	_, _ = store.PinAgentKey(ctx, "s1", agentKey(t), false)

	hub.known, hub.state = true, agenthub.State{Since: time.Now().Add(-time.Minute)}
	away, err := svc.AgentsAway(ctx)
	require.NoError(t, err)
	assert.False(t, away["s1"], "left a minute ago: reconnecting")

	hub.state.Since = time.Now().Add(-agenthub.ReconnectGrace - time.Minute)
	away, _ = svc.AgentsAway(ctx)
	assert.False(t, away["s1"], "offline for a minute: not yet 5 minutes")

	hub.state.Since = time.Now().Add(-agentOfflineAfter - time.Second)
	away, _ = svc.AgentsAway(ctx)
	assert.True(t, away["s1"])

	hub.known, hub.started = false, time.Now()
	away, _ = svc.AgentsAway(ctx)
	assert.False(t, away["s1"], "the panel just started")
	up, err := svc.AgentsConnected(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"s1": false}, up, "an agent server, not connected")

	hub.known, hub.state = true, agenthub.State{Connected: true, Since: time.Now()}
	up, _ = svc.AgentsConnected(ctx)
	assert.True(t, up["s1"])
}

func TestExpiredTokenIsRefused(t *testing.T) {
	svc, store, _ := agentService(t)
	ctx := context.Background()
	tok, _, err := svc.CreateJoinToken(ctx, "s1", "u1")
	require.NoError(t, err)
	store.tokens[string(hashToken(tok.Token))].expires = time.Now().Add(-time.Second)
	_, err = svc.AdmitAgent(ctx, agenthub.Hello{Token: tok.Token, Key: agentKey(t)})
	assert.ErrorIs(t, err, agenthub.ErrUnknown)
}

func TestAgentStatus(t *testing.T) {
	svc, _, hub := agentService(t)
	key := agentKey(t)
	now := time.Now()
	view := func(rec AgentRecord) string { return svc.agentView("s1", rec).Status }

	assert.Equal(t, AgentWaiting, view(AgentRecord{Connection: ConnectionAgent}))
	assert.Equal(t, AgentRevoked, view(AgentRecord{Connection: ConnectionAgent, RevokedAt: &now}))
	assert.Equal(t, AgentWaiting, view(AgentRecord{Connection: ConnectionAgent, RevokedAt: &now, TokenExpiresAt: &now}), "a new token after a revoke")
	assert.Nil(t, svc.agentView("s1", AgentRecord{Connection: ConnectionSSH}), "an SSH server that never had an agent")
	assert.True(t, svc.agentView("s1", AgentRecord{Connection: ConnectionAgent, RevokedAt: &now, TokenExpiresAt: &now}).Rejoin, "waiting, but it replaces a revoked machine")
	assert.False(t, svc.agentView("s1", AgentRecord{Connection: ConnectionAgent}).Rejoin)

	pinned := AgentRecord{Connection: ConnectionAgent, Key: key}
	hub.state, hub.known = agenthub.State{Connected: true, Since: now}, true
	assert.Equal(t, AgentConnected, view(pinned))
	hub.state = agenthub.State{Since: now.Add(-30 * time.Second)}
	assert.Equal(t, AgentReconnecting, view(pinned))
	hub.state = agenthub.State{Since: now.Add(-10 * time.Minute)}
	assert.Equal(t, AgentOffline, view(pinned))
	hub.state = agenthub.State{Upgrading: true}
	assert.Equal(t, AgentUpgrading, view(pinned))
	// Never heard from since the panel started: reconnecting for a while, then offline.
	hub.state, hub.known, hub.started = agenthub.State{}, false, now
	assert.Equal(t, AgentReconnecting, view(pinned))
	hub.started = now.Add(-time.Hour)
	assert.Equal(t, AgentOffline, view(pinned))

	// The desired state shows once the agent has answered it.
	assert.Nil(t, svc.agentView("s1", pinned).Desired)
	pinned.SpecGeneration, pinned.ObservedGeneration, pinned.DriftAt = 4, 3, &now
	pinned.Drift = []byte(`[{"serviceId":"svc-1","kind":"missing"},{"kind":"orphan","container":"paas-x-1"}]`)
	d := svc.agentView("s1", pinned).Desired
	require.NotNil(t, d)
	assert.Equal(t, int64(4), d.SpecGeneration)
	assert.Equal(t, int64(3), d.ObservedGeneration)
	svcID, ctr := "svc-1", "paas-x-1"
	assert.Equal(t, []AgentDrift{{ServiceID: &svcID, Kind: "missing"}, {Kind: "orphan", Container: &ctr}}, d.Drift)
}

// A revoked server's next join replaces a machine it trusted: a rejoin,
// which the handler gives to owners and operators with the password, never a
// first join any developer may start.
func TestJoinAfterRevokeIsARejoin(t *testing.T) {
	svc, store, _ := agentService(t)
	ctx := context.Background()
	_, _ = store.PinAgentKey(ctx, "s1", agentKey(t), false)
	_, _ = store.RevokeAgent(ctx, "s1")

	pinned, err := svc.AgentKeyPinned(ctx, "s1")
	require.NoError(t, err)
	assert.True(t, pinned)
	_, rejoin, err := svc.CreateJoinToken(ctx, "s1", "u1")
	require.NoError(t, err)
	assert.True(t, rejoin)
}
