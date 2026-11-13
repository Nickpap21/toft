package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
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
		return "", &ssh.ExitError{}
	}
	return "ok", nil
}

type upgradeConn struct{ h *upgradeHost }

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
