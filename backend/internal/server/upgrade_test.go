package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// upgradeHost is an agent that accepts or refuses each upgrade offered, or
// a link that drops before it answers.
type upgradeHost struct {
	sshpool.RemoteHost
	refuse  bool
	words   string // what it answers when it refuses
	drop    bool
	offered []agentproto.UpgradeHeader
}

func (h *upgradeHost) Subsystem(_ context.Context, name string, stdin io.Reader) (string, error) {
	if name != agentproto.SubsystemUpgrade {
		return "", io.EOF
	}
	var hdr agentproto.UpgradeHeader
	if err := json.NewDecoder(stdin).Decode(&hdr); err != nil {
		return "", err
	}
	h.offered = append(h.offered, hdr)
	if h.drop {
		return "", io.EOF
	}
	if h.refuse {
		return h.words, &ssh.ExitError{}
	}
	return "ok", nil
}

type upgradeConn struct{ h sshpool.RemoteHost }

func (c upgradeConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (upgradeConn) Invalidate(string)                          {}

// An agent back at the version it was upgraded from (its trial went back,
// D-112) isn't offered the same upgrade again, accepted or refused, until
// the panel's version changes.
func TestAnUpgradeIsOfferedOncePerPanelVersion(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		svc, _, _ := agentService(t)
		host := &upgradeHost{refuse: refuse}
		svc.WithSSH(upgradeConn{host}, nil)
		bin := filepath.Join(svc.agents.binDir, "toft-agent-linux-amd64")
		require.NoError(t, os.WriteFile(bin, []byte("agent"), 0o600))
		require.NoError(t, os.WriteFile(bin+".sig", []byte("c2ln\n"), 0o600))
		require.NoError(t, os.WriteFile(bin+".manifest.sig", []byte("bWFu\n"), 0o600))
		saved := buildinfo.Version
		t.Cleanup(func() { buildinfo.Version = saved })
		hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"}
		ctx := context.Background()

		buildinfo.Version = "v0.24.0"
		svc.upgradeAgent(ctx, "s1", hello)
		svc.upgradeAgent(ctx, "s1", hello)
		require.Len(t, host.offered, 1, "refused %v: offered once", refuse)
		sum := sha256.Sum256([]byte("agent"))
		assert.Equal(t, agentproto.UpgradeHeader{Version: "v0.24.0", Size: 5, SHA256: hex.EncodeToString(sum[:]), Signature: "c2ln", ManifestSignature: "bWFu"}, host.offered[0])

		buildinfo.Version = "v0.24.1"
		svc.upgradeAgent(ctx, "s1", hello)
		assert.Len(t, host.offered, 2, "a new panel version offers again")
	}
}

// An offer the agent never answered, its link dropped, is made again.
func TestAnUnansweredUpgradeIsOfferedAgain(t *testing.T) {
	svc, _, _ := agentService(t)
	host := &upgradeHost{drop: true}
	svc.WithSSH(upgradeConn{host}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(svc.agents.binDir, "toft-agent-linux-amd64"), []byte("agent"), 0o600))
	saved := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = saved })
	buildinfo.Version = "v0.24.0"
	hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"}

	svc.upgradeAgent(context.Background(), "s1", hello)
	svc.upgradeAgent(context.Background(), "s1", hello)
	assert.Len(t, host.offered, 2)
}

// behindAgent is s1 with a pinned agent at v0.23.1 under a panel at
// v0.24.0, the agent's binary in place, and the host that answers offers.
func behindAgent(t *testing.T, host *upgradeHost) (*Service, *fakeHub, *fakeAudit) {
	t.Helper()
	svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1", Connection: ConnectionAgent, AgentKey: "key"}), "")
	store, hub, audit := newFakeAgents(), &fakeHub{started: time.Now(), known: true}, &fakeAudit{}
	hub.state = agenthub.State{Connected: true, Version: "v0.23.1", Caps: []string{agentproto.CapRollout}}
	svc.WithAgents(store, hub, audit, t.TempDir())
	store.rec("s1").Key, store.rec("s1").Version, store.rec("s1").Arch = "key", "v0.23.1", "amd64"
	svc.WithSSH(upgradeConn{host}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(svc.agents.binDir, "toft-agent-linux-amd64"), []byte("agent"), 0o600))
	saved := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = saved })
	buildinfo.Version = "v0.24.0"
	return svc, hub, audit
}

// An agent that refused the panel's version isn't offered it when it
// connects again; the view says why in the agent's own words, bounded; the
// action offers it again, and is in the audit log.
func TestARefusedUpgradeIsOfferedAgainWhenAsked(t *testing.T) {
	host := &upgradeHost{refuse: true, words: "the release key didn't sign\nthis\x1b[31m binary " + strings.Repeat("x", 400)}
	svc, hub, audit := behindAgent(t, host)
	ctx := context.Background()
	hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"}

	svc.upgradeAgent(ctx, "s1", hello)
	svc.upgradeAgent(ctx, "s1", hello) // it connects again
	require.Len(t, host.offered, 1, "held back")
	up := svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade
	require.NotNil(t, up)
	assert.Equal(t, "v0.24.0", up.PanelVersion)
	assert.Equal(t, UpgradeRefused, *up.Reason)
	assert.True(t, strings.HasPrefix(*up.Detail, "the release key didn't sign this [31m binary xxx"), *up.Detail)
	assert.Len(t, []rune(*up.Detail), 301, "300 characters and the ellipsis")
	assert.NotNil(t, up.At)
	assert.True(t, up.ComposeRefused, "it runs rollouts, and not on the builder")
	outdated, err := svc.AgentsOutdated(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"s1": true}, outdated)

	host.refuse = false
	require.NoError(t, svc.OfferAgentUpgrade(ctx, "u1", "s1"))
	offerEnded(t, svc)
	require.Len(t, host.offered, 2, "offered again")
	assert.Equal(t, "v0.24.0", host.offered[1].Version)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, auditEntry{"o1", "u1", "server.agent_upgrade_offered", "server:s1"}, audit.entries[0])
	assert.Equal(t, map[string]any{"name": "fsn1", "from": "v0.23.1", "to": "v0.24.0", "connected": true}, audit.details[0])
	assert.Nil(t, svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade.Reason, "its answer isn't known yet")
	outdated, err = svc.AgentsOutdated(ctx)
	require.NoError(t, err)
	assert.Empty(t, outdated, "a retry under way says nothing: the alert stays as it was")

	hub.set(func(st *agenthub.State) { *st = agenthub.State{} }) // restarting into it
	outdated, _ = svc.AgentsOutdated(ctx)
	assert.Empty(t, outdated)
	hub.state = agenthub.State{Connected: true, Version: "v0.23.1", Caps: []string{agentproto.CapRollout}}
	svc.upgradeAgent(ctx, "s1", hello) // and back at the version it left
	outdated, _ = svc.AgentsOutdated(ctx)
	assert.Equal(t, map[string]bool{"s1": true}, outdated, "the retry failed")
	hub.state = agenthub.State{Connected: true, Version: "v0.24.0", Caps: []string{agentproto.CapRollout, agentproto.CapBuilder}}
	outdated, _ = svc.AgentsOutdated(ctx)
	assert.Equal(t, map[string]bool{"s1": false}, outdated, "connected with the builder")
}

// An agent's words keep its letters and lose what could reorder or hide
// them on the card: a right-to-left override and its pop, a zero-width
// joiner, a NUL, an ANSI escape, the line and paragraph separators.
func TestAgentWordsAreOnePlainLine(t *testing.T) {
	for in, want := range map[string]string{
		"signed by \xe2\x80\xaeliam\xe2\x80\xac": "signed by liam",
		"zero\xe2\x80\x8dwidth":                  "zerowidth",
		"nul\x00byte":                            "nul byte",
		"\x1b[31mred\x1b[0m":                     "[31mred [0m",
		"one\xe2\x80\xa8two\xe2\x80\xa9three":    "one two three",
		"η υπογραφή δεν ταιριάζει\r\n":           "η υπογραφή δεν ταιριάζει",
	} {
		assert.Equal(t, want, agentWords(in), "%q", in)
	}
}

// An agent that took the panel's version and connects at the one it left
// came back from it (D-112): the view says so, and the action offers again.
func TestAnUpgradeTheAgentCameBackFromSaysSo(t *testing.T) {
	host := &upgradeHost{}
	svc, _, _ := behindAgent(t, host)
	ctx := context.Background()
	hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"}
	rec := *svc.agents.store.(*fakeAgents).rec("s1")

	svc.upgradeAgent(ctx, "s1", hello)
	assert.Nil(t, svc.agentView("s1", rec).Upgrade.Reason)
	svc.upgradeAgent(ctx, "s1", hello) // back at v0.23.1
	up := svc.agentView("s1", rec).Upgrade
	assert.Equal(t, UpgradeReturned, *up.Reason)
	assert.Nil(t, up.Detail)
	require.Len(t, host.offered, 1)

	require.NoError(t, svc.OfferAgentUpgrade(ctx, "u1", "s1"))
	offerEnded(t, svc)
	assert.Len(t, host.offered, 2)
}

// offerEnded waits for the offer to s1 running in the background to end.
func offerEnded(t *testing.T, svc *Service) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, busy := svc.agents.offering.Load("s1")
		return !busy
	}, 5*time.Second, time.Millisecond)
}

// slowHost is an agent whose answer to an upgrade waits until the test
// lets it go: it says on uploaded when the upload arrived.
type slowHost struct {
	upgradeHost
	uploaded chan struct{}
	answer   chan struct{}
}

func (h *slowHost) Subsystem(ctx context.Context, name string, stdin io.Reader) (string, error) {
	out, err := h.upgradeHost.Subsystem(ctx, name, stdin)
	h.uploaded <- struct{}{}
	<-h.answer
	return out, err
}

// POST …/agent/upgrade answers 202 with the server as the offer starts,
// and 409 agent_upgrade_running while it runs.
func TestOfferingAnUpgradeAnswers202ThenConflict(t *testing.T) {
	host := &slowHost{uploaded: make(chan struct{}), answer: make(chan struct{})}
	svc, _, audit := behindAgent(t, &host.upgradeHost)
	svc.WithSSH(upgradeConn{host}, nil)
	r := roleRouterAs(svc, audit, "owner", "o1", "u1")
	const path = "/api/v1/servers/s1/agent/upgrade"

	code, body := callJSON(t, r, http.MethodPost, path, nil)
	require.Equal(t, http.StatusAccepted, code, body)
	apispec.Check(t, http.MethodPost, path, code, body)
	assert.Equal(t, true, body["data"].(map[string]any)["agent"].(map[string]any)["upgrade"].(map[string]any)["offering"])
	<-host.uploaded

	code, body = callJSON(t, r, http.MethodPost, path, nil)
	require.Equal(t, http.StatusConflict, code, body)
	apispec.Check(t, http.MethodPost, path, code, body)
	assert.Equal(t, "agent_upgrade_running", body["data"].(map[string]any)["code"])
	assert.Equal(t, ErrAgentUpgradeRunning.Error(), body["error"])

	close(host.answer)
	offerEnded(t, svc)
	assert.Len(t, host.offered, 1)
}

// An offer runs in the background, one at a time per server: a second one
// asked for while it runs, or while the agent restarts into a version, is
// refused and changes nothing, the record of the first one's answer, the
// audit log and the uploads included. Its answer lands on the view.
func TestOneUpgradeOfferRunsAtATime(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		host := &slowHost{upgradeHost: upgradeHost{refuse: refuse, words: "busy"}, uploaded: make(chan struct{}), answer: make(chan struct{})}
		svc, hub, audit := behindAgent(t, &host.upgradeHost)
		svc.WithSSH(upgradeConn{host}, nil)
		ctx := context.Background()
		hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"}

		require.NoError(t, svc.OfferAgentUpgrade(ctx, "u1", "s1"))
		<-host.uploaded
		up := svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade
		require.NotNil(t, up)
		assert.True(t, up.Offering, "the view says an offer runs")
		assert.Nil(t, up.Reason)

		require.ErrorIs(t, svc.OfferAgentUpgrade(ctx, "u2", "s1"), ErrAgentUpgradeRunning)
		// A connect offers nothing beside it (as a server moving to the
		// agent: AgentConnected doesn't sign in to it first).
		svc.agents.store.(*fakeAgents).rec("s1").Connection = ConnectionSSH
		svc.AgentConnected("s1", hello)
		svc.agents.store.(*fakeAgents).rec("s1").Connection = ConnectionAgent
		assert.Len(t, host.offered, 1, "no second upload")
		assert.Len(t, audit.entries, 1, "a refusal isn't an offer")

		close(host.answer)
		offerEnded(t, svc)
		up = svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade
		require.NotNil(t, up)
		assert.False(t, up.Offering)
		if refuse {
			assert.Equal(t, UpgradeRefused, *up.Reason)
			assert.Equal(t, "busy", *up.Detail)
		} else {
			assert.Nil(t, up.Reason, "accepted: the agent restarts into it")
			v, ok := svc.agents.offered.Load("s1")
			require.True(t, ok)
			assert.Equal(t, upgradeOffer{attempt: "v0.23.1 v0.24.0"}, v.(upgradeOffer), "the first one's answer stands")

			hub.set(func(st *agenthub.State) { st.Upgrading = true }) // restarting into it
			require.ErrorIs(t, svc.OfferAgentUpgrade(ctx, "u2", "s1"), ErrAgentUpgradeRunning)
			v, _ = svc.agents.offered.Load("s1")
			assert.Equal(t, upgradeOffer{attempt: "v0.23.1 v0.24.0"}, v.(upgradeOffer), "a refused second offer leaves it alone")
			hub.set(func(st *agenthub.State) { st.Upgrading = false })
		}
		assert.Len(t, audit.entries, 1)

		svc.WithSSH(upgradeConn{&host.upgradeHost}, nil) // answers at once
		require.NoError(t, svc.OfferAgentUpgrade(ctx, "u1", "s1"), "after it ended, a new offer starts")
		offerEnded(t, svc)
		assert.Len(t, host.offered, 2)
		assert.Len(t, audit.entries, 2)
	}
}

// An agent that connects again while an offer runs is offered nothing
// beside it. When that offer's link drops without an answer, the new
// connection gets its own; a link that drops with no new connection after
// it gets none until the agent connects.
func TestAnOfferWhoseLinkWasReplacedIsMadeAgain(t *testing.T) {
	for _, reconnects := range []bool{true, false} {
		host := &slowHost{upgradeHost: upgradeHost{drop: true}, uploaded: make(chan struct{}), answer: make(chan struct{})}
		svc, hub, _ := behindAgent(t, &host.upgradeHost)
		svc.WithSSH(upgradeConn{host}, nil)
		hub.state.Since = time.Now().Add(-time.Hour)

		require.NoError(t, svc.OfferAgentUpgrade(context.Background(), "u1", "s1"))
		<-host.uploaded
		if reconnects {
			hub.set(func(st *agenthub.State) { st.Since = time.Now() })
			svc.agents.store.(*fakeAgents).rec("s1").Connection = ConnectionSSH // as above: no sign-in first
			svc.AgentConnected("s1", agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"})
			svc.agents.store.(*fakeAgents).rec("s1").Connection = ConnectionAgent
		}
		require.Len(t, host.offered, 1, "nothing beside the offer that runs")

		close(host.answer) // the first link drops
		if reconnects {
			select {
			case <-host.uploaded:
			case <-time.After(5 * time.Second):
				t.Fatal("the new connection was offered nothing")
			}
		}
		offerEnded(t, svc)
		if reconnects {
			assert.Len(t, host.offered, 2, "the new connection is offered it")
		} else {
			assert.Len(t, host.offered, 1, "the same link isn't tried again")
		}
	}
}

// The agent's state is read with the server's one offer taken: an offer
// that ended a moment ago, the agent restarting into it, is seen, and
// nothing of its record changes.
func TestAnOfferReadsTheAgentsStateOnceItIsTheOnlyOne(t *testing.T) {
	host := &upgradeHost{}
	svc, hub, audit := behindAgent(t, host)
	svc.agents.offered.Store("s1", upgradeOffer{attempt: "v0.23.1 v0.24.0"})
	hub.asked = func() {
		_, taken := svc.agents.offering.Load("s1")
		assert.True(t, taken, "the state is read after the offer is taken")
		hub.state.Upgrading = true // the offer before it ended meanwhile
		hub.asked = nil
	}
	require.ErrorIs(t, svc.OfferAgentUpgrade(context.Background(), "u1", "s1"), ErrAgentUpgradeRunning)
	_, taken := svc.agents.offering.Load("s1")
	assert.False(t, taken, "a refused offer frees the server")
	v, _ := svc.agents.offered.Load("s1")
	assert.Equal(t, upgradeOffer{attempt: "v0.23.1 v0.24.0"}, v.(upgradeOffer))
	assert.Empty(t, audit.entries)
	assert.Empty(t, host.offered)
}

// With no agent binary for its architecture the panel offers nothing and
// says so; every connect looks again.
func TestAnAgentWithNoBuildSaysSo(t *testing.T) {
	host := &upgradeHost{}
	svc, _, _ := behindAgent(t, host)
	ctx := context.Background()
	hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "arm64"}

	svc.upgradeAgent(ctx, "s1", hello)
	up := svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade
	assert.Equal(t, UpgradeNoBuild, *up.Reason)
	assert.Empty(t, host.offered)

	hello.Arch = "amd64"
	svc.upgradeAgent(ctx, "s1", hello)
	assert.Len(t, host.offered, 1, "not held back")
}

// The action refuses an agent that isn't behind and a server with none; an
// agent that is away is offered the version when it connects.
func TestOfferingAnUpgradeNeedsAnAgentBehindThePanel(t *testing.T) {
	host := &upgradeHost{refuse: true}
	svc, hub, audit := behindAgent(t, host)
	ctx := context.Background()
	hello := agenthub.Hello{ServerID: "s1", Version: "v0.23.1", Arch: "amd64"}
	svc.upgradeAgent(ctx, "s1", hello)

	hub.state = agenthub.State{} // away
	require.NoError(t, svc.OfferAgentUpgrade(ctx, "u1", "s1"))
	require.Len(t, host.offered, 1, "nothing to offer it over")
	up := svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade
	assert.Nil(t, up.Reason)
	assert.False(t, up.ComposeRefused, "what an agent that is away can do isn't known")
	svc.upgradeAgent(ctx, "s1", hello) // it connects
	assert.Len(t, host.offered, 2)

	hub.state = agenthub.State{Connected: true, Version: "v0.24.0"}
	require.ErrorIs(t, svc.OfferAgentUpgrade(ctx, "u1", "s1"), ErrAgentNotBehind)
	assert.Nil(t, svc.agentView("s1", *svc.agents.store.(*fakeAgents).rec("s1")).Upgrade)
	assert.Len(t, audit.entries, 1, "a refusal isn't an offer")

	bare := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1"}), "")
	bare.WithAgents(newFakeAgents(), &fakeHub{}, nil, t.TempDir())
	require.ErrorIs(t, bare.OfferAgentUpgrade(ctx, "u1", "s1"), ErrNoAgent)
}

// A compose deploy is refused for a connected agent that runs rollouts and
// not on Toft's builder, and for no other.
func TestAgentOutdatedRefusesAnAgentWithoutTheBuilder(t *testing.T) {
	svc, hub, _ := behindAgent(t, &upgradeHost{})
	srv := &Server{ID: "s1", Name: "fsn1", Connection: ConnectionAgent}

	var outdated *AgentOutdatedError
	require.ErrorAs(t, svc.AgentOutdated(srv), &outdated)
	assert.Equal(t, "fsn1's agent (v0.23.1) is older than the panel and can't run this deploy safely. Upgrade the agent, then deploy again: /servers/s1?tab=settings#connection", outdated.Error())

	hub.state.Caps = []string{agentproto.CapRollout, agentproto.CapBuilder}
	assert.NoError(t, svc.AgentOutdated(srv))
	hub.state.Caps = nil // the panel runs its rollouts, on the builder
	assert.NoError(t, svc.AgentOutdated(srv))
	hub.state = agenthub.State{Caps: []string{agentproto.CapRollout}} // away
	assert.NoError(t, svc.AgentOutdated(srv))
	assert.NoError(t, svc.AgentOutdated(&Server{ID: "s1", Connection: ConnectionSSH}))
	assert.NoError(t, svc.AgentOutdated(nil))
}

// uninstallHost is an agent that records the requests it is sent.
type uninstallHost struct {
	sshpool.RemoteHost
	asked []string
}

func (h *uninstallHost) Subsystem(_ context.Context, name string, _ io.Reader) (string, error) {
	h.asked = append(h.asked, name)
	return "", nil
}

type uninstallConn struct{ h *uninstallHost }

func (c uninstallConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (uninstallConn) Invalidate(string)                          {}

// Deleting an agent server asks its agent, while connected, to remove
// itself, and drops its connection: one left running would knock forever.
func TestDeletingAnAgentServerUninstallsItsAgent(t *testing.T) {
	for _, connected := range []bool{true, false} {
		svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "fsn1", OrgID: "o1", Connection: ConnectionAgent, AgentKey: "key"}), "")
		hub := &fakeHub{started: time.Now(), known: true, state: agenthub.State{Connected: connected}}
		svc.WithAgents(newFakeAgents(), hub, nil, t.TempDir())
		host := &uninstallHost{}
		svc.WithSSH(uninstallConn{host}, nil)

		require.NoError(t, svc.DeleteServer(context.Background(), "s1", false))
		if connected {
			assert.Equal(t, []string{agentproto.SubsystemUninstall}, host.asked)
		} else {
			assert.Empty(t, host.asked, "an offline agent is refused when it comes back")
		}
		assert.Equal(t, []string{"s1"}, hub.dropped)
	}
}
