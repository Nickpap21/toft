package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// The node agent (D-106): a server whose connection is "agent" is reached
// through the WebSocket its toft-agent opened to the panel, with the same
// SSH as any server inside it. These are the service's parts: who may
// connect (AdmitAgent), joining by token, moving between SSH and the agent,
// rotating, revoking, and upgrading agents to the panel's version.

// AgentHub is the part of *agenthub.Hub the service uses.
type AgentHub interface {
	Conn(ctx context.Context, serverID string) (net.Conn, error)
	State(serverID string) (agenthub.State, bool)
	Started() time.Time
	MarkUpgrading(serverID string)
	Drop(serverID string)
}

// AgentAuditor writes the workspace audit log (org.Service).
type AgentAuditor interface {
	WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any)
}

// JoinTokenTTL is how long a join token works (D-106).
const JoinTokenTTL = time.Hour

// agentInstallTimeout bounds installing the agent over SSH and waiting for
// it to connect.
const agentInstallTimeout = 5 * time.Minute

var (
	// ErrAgentsNotConfigured: the backend runs without the agent hub.
	ErrAgentsNotConfigured = errors.New("the node agent is not configured on this panel")
	// ErrNoAgent: the server has no agent joined.
	ErrNoAgent = errors.New("this server has no agent")
	// ErrAgentOffline: the action needs the agent connected.
	ErrAgentOffline = errors.New("the server's agent is offline")
	// ErrServerFrozen refuses deploys, moves and removals on a server whose
	// agent kept its own state when the panel was restored from a backup
	// (D-113), until someone chooses which state to keep.
	ErrServerFrozen = errors.New("this server kept its own state when the panel was restored from a backup: " +
		"choose which state to keep on the server's page first")
	// ErrAgentBinaries: this build carries no agent binaries.
	ErrAgentBinaries = errors.New("this panel's build has no agent binaries")
	// ErrNotSSH / ErrNotAgent: the move asked for is already the case.
	ErrAlreadyAgent = errors.New("the server already uses the agent")
	ErrAlreadySSH   = errors.New("the server already uses SSH")
	// ErrStrictInUse: the server's strict services need the agent's DNS
	// relay (D-115); StrictInUseError names them.
	ErrStrictInUse = errors.New("services here run Sandboxed with a strict network, and only the agent answers their name lookups: " +
		"change their isolation first")
	// ErrNoSSHKey: moving to SSH needs a key and the server has none stored.
	ErrNoSSHKey = errors.New("this server has no SSH key stored: paste one to move it to SSH")
	// ErrAgentOwnerOnly / ErrAgentOperatorOrOwner: who may do what (D-106).
	ErrAgentOwnerOnly       = errors.New("only workspace owners can rotate an agent's key")
	ErrAgentOperatorOrOwner = errors.New("only workspace owners and instance operators can do this")
)

// agentDeps is set by WithAgents.
type agentDeps struct {
	store AgentStore
	hub   AgentHub
	audit AgentAuditor
	// binDir holds toft-agent-linux-<arch> and the release's signatures of
	// it: .sig over the binary (agents before v0.24.0 check it) and
	// .manifest.sig over its manifest (agentproto.UpgradeHeader).
	binDir string

	keyMu  sync.Mutex
	keyPEM string

	// sums: "<path> <size> <mtime>" → the agent binary's SHA-256, so the
	// install script doesn't hash the release on every request.
	sums sync.Map

	// offered: server ID → the offer (an upgradeOffer) its agent last
	// answered, yes or no, so an agent back at the old version (refused, or
	// its trial went back) isn't sent the same one again until the panel's
	// changes.
	offered sync.Map

	// desired is set by WithDesired, adopter by WithAdopter.
	desired DesiredServices
	adopter DeploymentAdopter
}

// WithAgents enables the node agent. binDir is where the release's agent
// binaries are (TOFT_AGENT_DIR).
func (s *Service) WithAgents(store AgentStore, hub AgentHub, audit AgentAuditor, binDir string) {
	s.agents = &agentDeps{store: store, hub: hub, audit: audit, binDir: binDir}
}

// panelKey returns the panel's agent key (PEM), loaded once it loads: a
// failure isn't kept, and a caller's cancelled request doesn't fail it.
func (s *Service) panelKey(ctx context.Context) (string, error) {
	a := s.agents
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	if a.keyPEM != "" {
		return a.keyPEM, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	key, err := a.store.PanelAgentKey(ctx)
	if err != nil {
		return "", err
	}
	a.keyPEM = key
	return key, nil
}

// PanelKeyFingerprint is the SHA256 fingerprint of the panel's agent key,
// which install scripts give the agent to pin.
func (s *Service) PanelKeyFingerprint(ctx context.Context) (string, error) {
	if s.agents == nil {
		return "", ErrAgentsNotConfigured
	}
	pemKey, err := s.panelKey(ctx)
	if err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey([]byte(pemKey))
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

// agentTarget is the pool target for an agent server: the agent's
// WebSocket, the panel's key, and the agent's pinned key as host key.
func (s *Service) agentTarget(srv *Server) sshpool.Target {
	t := sshpool.Target{ServerID: srv.ID, Name: srv.Name, Host: srv.IPAddress, User: agentproto.SSHUser}
	if s.agents == nil {
		t.Conn = func(context.Context) (net.Conn, error) { return nil, ErrAgentsNotConfigured }
		return t
	}
	key, err := s.panelKey(context.Background())
	if err != nil {
		t.Conn = func(context.Context) (net.Conn, error) { return nil, fmt.Errorf("the panel's agent key: %w", err) }
		return t
	}
	hub, id := s.agents.hub, srv.ID
	t.PrivateKeyPEM = key
	t.PinnedHostKey = srv.AgentKey
	t.LoadPin = func(ctx context.Context) (string, error) { return s.agents.store.AgentKey(ctx, id) }
	t.Conn = func(ctx context.Context) (net.Conn, error) {
		c, err := hub.Conn(ctx, id)
		if errors.Is(err, agenthub.ErrOffline) {
			return nil, fmt.Errorf("%s: %w", srv.Name, ErrAgentOffline)
		}
		return c, err
	}
	return t
}

// AgentRunsRollouts reports whether the server's agent is connected and
// runs the rollouts the panel hands it (D-111).
func (s *Service) AgentRunsRollouts(serverID string) bool {
	if s.agents == nil {
		return false
	}
	st, ok := s.agents.hub.State(serverID)
	return ok && st.Connected && slices.Contains(st.Caps, agentproto.CapRollout)
}

// PanelEpoch returns the panel's epoch (D-111).
func (s *Service) PanelEpoch(ctx context.Context) (string, error) {
	if s.agents == nil {
		return "", errors.New("agents are not configured")
	}
	return s.agents.store.PanelEpoch(ctx)
}

// AgentKeepsDeadlines reports whether the server's agent is connected and
// ends a rollout at the deploy's time limit (D-125).
func (s *Service) AgentKeepsDeadlines(serverID string) bool {
	if s.agents == nil {
		return false
	}
	st, ok := s.agents.hub.State(serverID)
	return ok && st.Connected && slices.Contains(st.Caps, agentproto.CapDeadline)
}

// AgentAppliesContainerChecks reports whether the server's agent is
// connected and creates slots with the service's own container check.
func (s *Service) AgentAppliesContainerChecks(serverID string) bool {
	if s.agents == nil {
		return false
	}
	st, ok := s.agents.hub.State(serverID)
	return ok && st.Connected && slices.Contains(st.Caps, agentproto.CapContainerCheck)
}

// AgentKeepsMetrics reports whether the server's agent is connected and
// keeps the samples it takes while the panel is away (D-113).
func (s *Service) AgentKeepsMetrics(serverID string) bool {
	if s.agents == nil {
		return false
	}
	st, ok := s.agents.hub.State(serverID)
	return ok && st.Connected && slices.Contains(st.Caps, agentproto.CapMetrics)
}

// agentDNS reports whether srv is reached through its agent, connected now
// and answering names for strict-network containers (D-115).
func (s *Service) agentDNS(srv *Server) bool {
	if s.agents == nil || srv.Connection != ConnectionAgent {
		return false
	}
	st, ok := s.agents.hub.State(srv.ID)
	return ok && st.Connected && slices.Contains(st.Caps, agentproto.CapDNS)
}

// ─── Admission (the hub's Admitter) ──────────────────────────────────────────

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// AdmitAgent decides whether a connecting agent may open its WebSocket.
func (s *Service) AdmitAgent(ctx context.Context, h agenthub.Hello) (string, error) {
	if s.agents == nil {
		return "", agenthub.ErrUnknown
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.Key))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 {
		return "", agenthub.ErrUnknown
	}
	presented := sshclient.MarshalHostKey(key)
	if h.Token != "" {
		id, err := s.admitJoin(ctx, h, presented)
		if err == nil || !errors.Is(err, agenthub.ErrUnknown) {
			return id, err
		}
		// A used token left in the agent's file: its pinned key decides.
	}
	if h.ServerID == "" {
		return "", agenthub.ErrUnknown
	}
	rec, err := s.agents.store.GetAgent(ctx, h.ServerID)
	if errors.Is(err, ErrServerNotFound) {
		return "", agenthub.ErrUnknown
	}
	if err != nil {
		return "", err
	}
	// A rotation's new key: the agent switched, whatever the panel heard.
	if rec.Key != "" && rec.NextKey != "" && sameHostKey(rec.NextKey, presented) {
		if ok, err := s.agents.store.PromoteAgentNextKey(ctx, h.ServerID, rec.NextKey); err != nil {
			return "", err
		} else if !ok {
			// Not ErrUnknown: an agent refused without a token stops for good.
			return "", errors.New("a key rotation raced a rejoin or revoke; the agent tries again")
		}
		if s.conn != nil {
			s.conn.Invalidate(h.ServerID)
		}
		return h.ServerID, nil
	}
	// A key other than the pinned one is revoked or replaced (a rejoin
	// made a new one): that agent stops for good. The SSH handshake that
	// follows proves the agent holds the pinned key.
	if rec.Key == "" || !sameHostKey(rec.Key, presented) {
		if rec.Key == "" && rec.RevokedAt == nil {
			return "", agenthub.ErrUnknown
		}
		return "", agenthub.ErrRevoked
	}
	return h.ServerID, nil
}

// admitJoin consumes a join token and pins the agent's key.
func (s *Service) admitJoin(ctx context.Context, h agenthub.Hello, key string) (string, error) {
	hash := hashToken(h.Token)
	if id, err := s.agents.store.PeekJoinToken(ctx, hash); err != nil || (h.ServerID != "" && id != h.ServerID) {
		return "", agenthub.ErrUnknown
	}
	// One transaction, so a dropped request can't spend the token unpinned.
	serverID, replaces, createdBy, pinned, err := s.agents.store.JoinWithToken(ctx, hash, key)
	if errors.Is(err, ErrUnknownToken) {
		return "", agenthub.ErrUnknown
	}
	if err != nil {
		return "", err
	}
	if !pinned {
		// A first join while another key is pinned: the pinned key decides.
		return "", agenthub.ErrUnknown
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	// A rejoin replaced the key: connections made with the old one must not
	// carry on. Only the agent's: a server being moved to the agent is still
	// reached over SSH, which is running the install right now.
	if replaces && s.conn != nil && err == nil && srv.Connection == ConnectionAgent {
		s.conn.Invalidate(serverID)
	}
	if replaces {
		s.agents.hub.Drop(serverID) // the replaced agent, which would otherwise keep its place
	}
	if err == nil {
		action := "server.agent_joined"
		if replaces {
			action = "server.agent_rejoined"
		}
		fp, _, _ := sshclient.Fingerprint(key)
		s.auditAgent(ctx, srv.OrgID, createdBy, action, serverID, map[string]any{"name": srv.Name, "fingerprint": fp, "from": h.FromIP, "version": h.Version})
	}
	s.log.Info("agent joined", zap.String("server_id", serverID), zap.Bool("rejoin", replaces), zap.String("from", h.FromIP))
	return serverID, nil
}

func (s *Service) auditAgent(ctx context.Context, orgID, actorID, action, serverID string, detail map[string]any) {
	if s.agents.audit == nil || orgID == "" || actorID == "" {
		return
	}
	s.agents.audit.WriteAuditLogDetail(ctx, orgID, actorID, action, "server:"+serverID, detail)
}

// AgentConnected records the agent, fills in the server's address when it
// had none, retries what failed while it was away, probes the server after
// a reboot, and upgrades an agent older than the panel.
func (s *Service) AgentConnected(serverID string, h agenthub.Hello) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	prev, _ := s.agents.store.GetAgent(ctx, serverID)
	if err := s.agents.store.RecordAgentConnected(ctx, serverID, h.Version, h.Arch, h.BootID, h.FromIP); err != nil {
		s.log.Warn("could not record the agent's connection", zap.Error(err), zap.String("server_id", serverID))
	}
	if ip := net.ParseIP(h.FromIP); ip != nil {
		_ = s.agents.store.SetAddressIfEmpty(ctx, serverID, h.FromIP)
	}
	if r, ok := s.conn.(interface{ Retry(string) }); ok {
		r.Retry(serverID)
	}
	if prev != nil && prev.Connection == ConnectionAgent {
		// Sign in now, so a key that doesn't match fails at once and the
		// pool is ready for the next call.
		srv, err := s.repo.GetByID(ctx, serverID)
		if err == nil {
			if _, err := s.SSH(srv).Ensure(ctx, sshpool.EnsureOpts{Force: true}); err != nil {
				s.log.Warn("could not sign in to the agent", zap.Error(err), zap.String("server_id", serverID))
				return
			}
		}
		if _, err := s.repo.UpdateStatus(ctx, serverID, StatusActive); err != nil {
			s.log.Warn("could not mark the server online", zap.Error(err))
		}
		s.followPanel(ctx, serverID)
		if srv != nil {
			s.provisionOnFirstJoin(ctx, srv)
		}
		if prev.BootID != "" && h.BootID != "" && prev.BootID != h.BootID {
			go func() {
				pctx, pcancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer pcancel()
				if _, err := s.ProbeCapabilities(pctx, serverID); err != nil {
					s.log.Warn("capability probe after the server rebooted failed", zap.Error(err), zap.String("server_id", serverID))
				}
			}()
		}
	}
	if buildinfo.Newer(buildinfo.Version, h.Version) {
		s.upgradeAgent(ctx, serverID, h)
	} else if buildinfo.Newer(h.Version, buildinfo.Version) {
		s.log.Warn("this server's agent is newer than the panel (a rolled-back panel?)", zap.String("server_id", serverID), zap.String("agent", h.Version), zap.String("panel", buildinfo.Version))
	}
}

// AgentSeen records that the agent was heard from.
func (s *Service) AgentSeen(serverID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.agents.store.TouchAgentSeen(ctx, serverID)
}

// agentBinary returns the release's agent for arch.
func (s *Service) agentBinary(arch string) ([]byte, error) {
	if arch != "amd64" && arch != "arm64" {
		return nil, fmt.Errorf("no agent for %q", arch)
	}
	bin, err := os.ReadFile(s.agentPath(arch))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAgentBinaries, err)
	}
	return bin, nil
}

func (s *Service) agentPath(arch string) string {
	return filepath.Join(s.agents.binDir, "toft-agent-linux-"+arch)
}

// agentSignature reads one of the release's signatures of arch's agent
// (ext ".sig" or ".manifest.sig"), "" for a build without one.
func (s *Service) agentSignature(arch, ext string) string {
	raw, err := os.ReadFile(s.agentPath(arch) + ext)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// agentSum is the hex SHA-256 of the release's agent for arch, "" without
// one.
func (s *Service) agentSum(arch string) (string, error) {
	path := s.agentPath(arch)
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("agent binary: %w", err)
	}
	key := fmt.Sprintf("%s %d %d", path, fi.Size(), fi.ModTime().UnixNano())
	if v, ok := s.agents.sums.Load(key); ok {
		return v.(string), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("agent binary: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash the agent binary: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	s.agents.sums.Store(key, sum)
	return sum, nil
}

// InstallScript is the join script as the panel serves it at o: the panel
// key and the release's agents' checksums written in, so a script whose
// own checksum the join command carries vouches for both. A join token
// (or "") is written in too, so the command needs none on its command line
// (D-123); one that isn't a join token's shape is ErrUnknownToken.
func (s *Service) InstallScript(ctx context.Context, o AgentOrigin, token string) (string, error) {
	if token != "" && !joinTokenRe.MatchString(token) {
		return "", ErrUnknownToken
	}
	fp, err := s.PanelKeyFingerprint(ctx)
	if err != nil {
		return "", err
	}
	amd64, err := s.agentSum("amd64")
	if err != nil {
		return "", err
	}
	arm64, err := s.agentSum("arm64")
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(
		"@PANEL@", o.URL,
		"@PANEL_KEY@", fp,
		"@INSECURE@", map[bool]string{true: "1", false: "0"}[o.InsecureTLS],
		"@CERT_PIN@", o.CertPin,
		"@AGENT_SHA256_AMD64@", amd64,
		"@AGENT_SHA256_ARM64@", arm64,
		"@TOKEN@", token,
	).Replace(agentInstallScript), nil
}

// AgentBinary is the install script's download: the release's agent for
// arch, for a join token that still works. It returns the token's server.
func (s *Service) AgentBinary(ctx context.Context, token, arch string) (serverID string, bin []byte, err error) {
	if s.agents == nil {
		return "", nil, ErrAgentsNotConfigured
	}
	serverID, err = s.agents.store.PeekJoinToken(ctx, hashToken(token))
	if err != nil {
		return "", nil, err
	}
	bin, err = s.agentBinary(arch)
	return serverID, bin, err
}

// upgradeOffer is an upgrade an agent answered: "from to", and whether the
// panel has said it holds it back.
type upgradeOffer struct {
	attempt string
	logged  bool
}

// upgradeAgent sends the panel's agent version to an older agent (D-106).
// The agent verifies the release signature, refuses a downgrade, restarts
// into it and goes back by itself if it can't connect within 2 minutes.
func (s *Service) upgradeAgent(ctx context.Context, serverID string, h agenthub.Hello) {
	attempt := h.Version + " " + buildinfo.Version
	if v, ok := s.agents.offered.Load(serverID); ok && v.(upgradeOffer).attempt == attempt {
		// Once per offer: a flapping link reconnects often.
		if !v.(upgradeOffer).logged && s.agents.offered.CompareAndSwap(serverID, v, upgradeOffer{attempt, true}) {
			s.log.Warn("not offering the agent this upgrade again: it refused it, or came back from it; another panel version or a panel restart offers it again", zap.String("server_id", serverID), zap.String("agent", h.Version), zap.String("panel", buildinfo.Version))
		}
		return
	}
	bin, err := s.agentBinary(h.Arch)
	if err != nil {
		s.log.Warn("cannot upgrade the agent", zap.Error(err), zap.String("server_id", serverID))
		return
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return
	}
	sum := sha256.Sum256(bin)
	hdr, _ := json.Marshal(agentproto.UpgradeHeader{
		Version:           buildinfo.Version,
		Size:              int64(len(bin)),
		SHA256:            hex.EncodeToString(sum[:]),
		Signature:         s.agentSignature(h.Arch, ".sig"),
		ManifestSignature: s.agentSignature(h.Arch, ".manifest.sig"),
	})
	in := bytes.NewReader(append(append(hdr, '\n'), bin...))
	srv.Connection = ConnectionAgent // the upgrade goes through the agent even while the server is moving
	if _, err := s.SSH(srv).Subsystem(ctx, agentproto.SubsystemUpgrade, in); err != nil {
		s.log.Warn("the agent refused the upgrade", zap.Error(err), zap.String("server_id", serverID), zap.String("from", h.Version), zap.String("to", buildinfo.Version))
		if exit := (*ssh.ExitError)(nil); errors.As(err, &exit) {
			s.agents.offered.Store(serverID, upgradeOffer{attempt: attempt}) // it answered no; a dropped link tries again
		}
		return
	}
	s.agents.offered.Store(serverID, upgradeOffer{attempt: attempt})
	s.agents.hub.MarkUpgrading(serverID)
	s.log.Info("agent upgrading", zap.String("server_id", serverID), zap.String("from", h.Version), zap.String("to", buildinfo.Version))
}

// ─── Joining ─────────────────────────────────────────────────────────────────

// JoinToken is a new token, shown once.
type JoinToken struct {
	Token     string
	ExpiresAt time.Time
}

// joinTokenRe is newJoinToken's shape: the install script embeds only that.
var joinTokenRe = regexp.MustCompile(`^toft_join_[A-Za-z0-9_-]{43}$`)

func newJoinToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "toft_join_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateAgentServer adds a server that joins by token (D-106): no SSH key,
// Waiting for its agent until one connects. address may be "": the agent's
// connection fills it in. Without a name (D-153) it reuses the caller's own
// waiting server, with a new command, or names a new one server-<n>; reused
// reports which.
func (s *Service) CreateAgentServer(ctx context.Context, orgID, userID, name, address string) (srv *Server, tok *JoinToken, reused bool, err error) {
	if s.agents == nil {
		return nil, nil, false, ErrAgentsNotConfigured
	}
	if name != "" && !ValidServerName(name) {
		return nil, nil, false, ErrInvalidServerName
	}
	id := ""
	if name == "" {
		if id, err = s.agents.store.FindWaitingServer(ctx, orgID, userID); err != nil {
			return nil, nil, false, err
		}
		reused = id != ""
	}
	// Two creates at once may pick the same server-<n>: the next try takes
	// the next number.
	for try := 0; id == "" && try < 5; try++ {
		n, auto := name, name == ""
		if auto {
			next, err := s.agents.store.NextServerNumber(ctx, orgID)
			if err != nil {
				return nil, nil, false, err
			}
			// A taken number (one beyond the count's 9 digits) moves on.
			n = fmt.Sprintf("server-%d", next+try)
		}
		id, err = s.agents.store.CreateAgentServer(ctx, orgID, userID, n, address, auto)
		if err != nil && (!auto || !errors.Is(err, ErrServerNameTaken)) {
			return nil, nil, false, err
		}
	}
	if id == "" {
		return nil, nil, false, err
	}
	if tok, err = s.issueJoinToken(ctx, id, false, userID); err != nil {
		return nil, nil, false, err
	}
	srv, err = s.repo.GetByID(ctx, id)
	return srv, tok, reused, err
}

// CreateJoinToken makes a new join token for a server, making older unused
// ones useless. With an agent key pinned it is a rejoin: the caller has
// checked the role and the password.
func (s *Service) CreateJoinToken(ctx context.Context, serverID, actorID string) (*JoinToken, bool, error) {
	if s.agents == nil {
		return nil, false, ErrAgentsNotConfigured
	}
	rec, err := s.agents.store.GetAgent(ctx, serverID)
	if err != nil {
		return nil, false, err
	}
	replaces := rejoins(rec)
	tok, err := s.issueJoinToken(ctx, serverID, replaces, actorID)
	return tok, replaces, err
}

// rejoins reports whether a join of the server replaces a machine it
// trusted: one whose key is pinned, or was until it was revoked. Revoking
// clears the key, and a first join, which takes no password, would
// otherwise attach another machine in its place.
func rejoins(rec *AgentRecord) bool {
	return rec.Key != "" || rec.RevokedAt != nil
}

// AgentKeyPinned reports whether a join token for the server would be a
// rejoin (which needs an owner or operator and the password): its key is
// pinned, or its agent was revoked.
func (s *Service) AgentKeyPinned(ctx context.Context, serverID string) (bool, error) {
	if s.agents == nil {
		return false, ErrAgentsNotConfigured
	}
	rec, err := s.agents.store.GetAgent(ctx, serverID)
	if err != nil {
		return false, err
	}
	return rejoins(rec), nil
}

func (s *Service) issueJoinToken(ctx context.Context, serverID string, replaces bool, actorID string) (*JoinToken, error) {
	token, err := newJoinToken()
	if err != nil {
		return nil, err
	}
	exp := time.Now().Add(JoinTokenTTL)
	if err := s.agents.store.CreateJoinToken(ctx, serverID, hashToken(token), replaces, actorID, exp); err != nil {
		return nil, err
	}
	return &JoinToken{Token: token, ExpiresAt: exp}, nil
}

// ─── Who may do what ─────────────────────────────────────────────────────────

// AuthorizeAgentAdmin allows workspace owners and instance operators:
// installing, moving, rejoining and revoking (D-106).
func (s *Service) AuthorizeAgentAdmin(ctx context.Context, actorID, role string) error {
	if role == "owner" {
		return nil
	}
	return s.requireOperator(ctx, actorID, ErrAgentOperatorOrOwner)
}

// ─── Moving ──────────────────────────────────────────────────────────────────

// AgentOrigin is how the agent reaches the panel: its origin, and whether
// TLS can't be verified (the panel has no domain; the pinned SSH keys
// carry the trust then). CertPin is then the SHA-256 of the public key
// (SPKI, base64) of the certificate the panel serves there: everything
// that carries a join token is pinned to it.
type AgentOrigin struct {
	URL         string
	InsecureTLS bool
	CertPin     string
}

// InstallAgent moves an SSH server to the agent (D-106): it installs the
// agent over SSH, waits for it to join and connect, and switches the
// server's connection. Running containers are not touched. Without keepKey
// the SSH key is deleted once the agent is connected.
func (s *Service) InstallAgent(ctx context.Context, actorID, serverID string, origin AgentOrigin, keepKey bool, logFn func(string)) error {
	if s.agents == nil {
		return ErrAgentsNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentInstallTimeout)
	defer cancel()
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.Connection == ConnectionAgent {
		return ErrAlreadyAgent
	}
	fp, err := s.PanelKeyFingerprint(ctx)
	if err != nil {
		return err
	}
	h := s.SSH(srv)
	logFn("Connecting over SSH...")
	arch, err := h.Exec(ctx, "uname -m")
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	goarch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[strings.TrimSpace(arch)]
	bin, err := s.agentBinary(goarch)
	if err != nil {
		return err
	}
	tok, _, err := s.CreateJoinToken(ctx, serverID, actorID)
	if err != nil {
		return err
	}
	dir := "/usr/local/lib/toft-agent/releases/" + buildinfo.Version
	logFn(fmt.Sprintf("Installing toft-agent %s (linux/%s)...", buildinfo.Version, goarch))
	if _, err := h.Exec(ctx, "mkdir -p "+dir); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	if err := h.Upload(ctx, dir+"/toft-agent.tmp", bin); err != nil {
		return fmt.Errorf("copy the agent: %w", err)
	}
	link := "chmod 755 " + dir + "/toft-agent.tmp && mv " + dir + "/toft-agent.tmp " + dir + "/toft-agent && " +
		"ln -sfn releases/" + buildinfo.Version + " /usr/local/lib/toft-agent/current.tmp && mv -T /usr/local/lib/toft-agent/current.tmp /usr/local/lib/toft-agent/current && " +
		"ln -sfn /usr/local/lib/toft-agent/current/toft-agent /usr/local/bin/toft-agent"
	if _, err := h.Exec(ctx, link); err != nil {
		return fmt.Errorf("install the agent: %w", err)
	}
	// The token goes in a root-only file, not on join's command line, where
	// `ps` shows it to the server's other accounts for the whole join (D-123).
	if _, err := h.ExecInput(ctx, "install -d -m 700 /etc/toft-agent && (umask 077 && cat > /etc/toft-agent/join-token)", strings.NewReader(tok.Token)); err != nil {
		return fmt.Errorf("hand the agent its join token: %w", err)
	}
	join := fmt.Sprintf("toft-agent join --panel %s --server %s --token-file /etc/toft-agent/join-token --panel-key %s",
		shellQuote(origin.URL), shellQuote(serverID), shellQuote(fp))
	if origin.InsecureTLS {
		join += " --insecure-tls --panel-cert " + shellQuote(origin.CertPin)
	}
	logFn("Starting toft-agent.service; waiting for it to connect...")
	if err := h.ExecStream(ctx, join, logFn); err != nil {
		return fmt.Errorf("the agent did not join: %w", err)
	}
	if err := s.waitAgent(ctx, serverID); err != nil {
		return err
	}
	if err := s.agents.store.SetConnection(ctx, serverID, ConnectionAgent); err != nil {
		return err
	}
	if !keepKey {
		if err := s.agents.store.SetSSHCredentials(ctx, serverID, srv.User, srv.Port, ""); err != nil {
			s.log.Warn("could not delete the SSH key after moving to the agent", zap.Error(err))
		}
	}
	s.auditAgent(ctx, srv.OrgID, actorID, "server.connection_moved", serverID, map[string]any{"name": srv.Name, "to": ConnectionAgent, "ssh_key_kept": keepKey})
	rec, _ := s.agents.store.GetAgent(ctx, serverID)
	if rec != nil {
		f, _, _ := sshclient.Fingerprint(rec.Key)
		logFn("Agent connected; pinned " + f)
	}
	// The agent connected while the server was still reached over SSH, so
	// AgentConnected left this for now.
	s.followPanel(ctx, serverID)
	return nil
}

// followPanel sends a connected agent the desired state and switches its
// server's Traefik to the agent's routes, unless the agent is frozen: it
// takes no order until the panel's choice.
func (s *Service) followPanel(ctx context.Context, serverID string) {
	ack, err := s.pushDesired(ctx, serverID, true, false)
	if err != nil {
		s.log.Warn("desired state", zap.String("server_id", serverID), zap.Error(err))
	}
	if srv, err := s.repo.GetByID(ctx, serverID); err == nil && (ack == nil || ack.Frozen == nil) {
		s.cutOverRoutes(ctx, srv)
	}
}

// waitAgent waits until the server's agent is connected.
func (s *Service) waitAgent(ctx context.Context, serverID string) error {
	for {
		if st, _ := s.agents.hub.State(serverID); st.Connected {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the agent didn't connect: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// MoveToSSH moves a server back to SSH (D-106): it signs in over SSH,
// checks the pinned host key (pinning on first use for a server that never
// had SSH), and switches. key "" keeps the stored key. The agent stays
// installed and connected, unused until the server moves back, and
// Traefik goes back to the containers' labels.
func (s *Service) MoveToSSH(ctx context.Context, actorID, serverID, user string, port int, key string) error {
	if s.agents == nil {
		return ErrAgentsNotConfigured
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.Connection != ConnectionAgent {
		return ErrAlreadySSH
	}
	strict, err := s.agents.store.StrictServices(ctx, serverID)
	if err != nil {
		return err
	}
	if len(strict) > 0 {
		return &StrictInUseError{Services: strict}
	}
	if user == "" {
		user = srv.User
	}
	if port == 0 {
		port = srv.Port
	}
	if key == "" {
		key = srv.SSHKey
	}
	if key == "" {
		return ErrNoSSHKey
	}
	c, err := sshclient.Dial(ctx, sshclient.Config{Host: srv.IPAddress, Port: port, User: user, PrivateKeyPEM: key, PinnedHostKey: srv.HostKey})
	if err != nil {
		return &sshFailure{hostKeyHint(srv.Name, err)}
	}
	presented := sshclient.MarshalHostKey(c.HostKey())
	_ = c.Close()
	if srv.HostKey == "" {
		if _, err := s.pinHostKey(srv)(ctx, presented); err != nil {
			return err
		}
	}
	if user != srv.User || port != srv.Port || key != srv.SSHKey {
		if err := s.agents.store.SetSSHCredentials(ctx, serverID, user, port, key); err != nil {
			return err
		}
	}
	if err := s.agents.store.SetConnection(ctx, serverID, ConnectionSSH); err != nil {
		return err
	}
	if s.conn != nil {
		s.conn.Invalidate(serverID)
	}
	s.auditAgent(ctx, srv.OrgID, actorID, "server.connection_moved", serverID, map[string]any{"name": srv.Name, "to": ConnectionSSH})
	// Untrusted previews' copies here run Sandboxed from their next deploy:
	// over SSH nothing answers a strict container's names (D-115).
	if err := s.agents.store.SandboxStrictPreviews(ctx, serverID); err != nil {
		s.log.Warn("could not move strict preview copies to Sandboxed", zap.Error(err), zap.String("server_id", serverID))
	}
	// Traefik back to the labels: the agent may be uninstalled next.
	if moved, err := s.repo.GetByID(ctx, serverID); err == nil {
		go s.cutOverRoutes(context.WithoutCancel(ctx), moved)
	}
	return nil
}

// NamedService is a service by ID and name.
type NamedService struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StrictInUseError is ErrStrictInUse with the services.
type StrictInUseError struct{ Services []NamedService }

func (e *StrictInUseError) Error() string { return ErrStrictInUse.Error() }
func (e *StrictInUseError) Unwrap() error { return ErrStrictInUse }

// ─── Rotating and revoking ───────────────────────────────────────────────────

// RotateAgentKey has the agent make a new key, pins it, and has the agent
// switch to it; the connection then restarts with the new key.
func (s *Service) RotateAgentKey(ctx context.Context, actorID, serverID string) (string, error) {
	if s.agents == nil {
		return "", ErrAgentsNotConfigured
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return "", err
	}
	if srv.AgentKey == "" {
		return "", ErrNoAgent
	}
	if st, _ := s.agents.hub.State(serverID); !st.Connected {
		return "", ErrAgentOffline
	}
	// Both steps run to the end even if the request that asked goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	asAgent := *srv
	asAgent.Connection = ConnectionAgent
	h := s.SSH(&asAgent)
	out, err := h.Subsystem(ctx, agentproto.SubsystemRotate, nil)
	if err != nil {
		return "", fmt.Errorf("the agent could not make a new key: %w", err)
	}
	next, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(out)))
	if err != nil || next.Type() != ssh.KeyAlgoED25519 {
		return "", errors.New("the agent answered no key")
	}
	nextKey := sshclient.MarshalHostKey(next)
	// Until the agent connects with one of them, either key is admitted:
	// whether it switched can't be known from a lost answer.
	if err := s.agents.store.SetAgentNextKey(ctx, serverID, nextKey); err != nil {
		return "", err
	}
	if _, err := h.Subsystem(ctx, agentproto.SubsystemRotateCommit, nil); err != nil {
		// Already reconnected with the new key and pinned it: done.
		if pinned, kerr := s.agents.store.AgentKey(ctx, serverID); kerr != nil || !sameHostKey(pinned, nextKey) {
			return "", fmt.Errorf("the agent could not switch keys: %w", err)
		}
	} else if _, err := s.agents.store.PromoteAgentNextKey(ctx, serverID, nextKey); err != nil {
		return "", err
	}
	s.agents.hub.Drop(serverID)
	if s.conn != nil {
		s.conn.Invalidate(serverID)
	}
	fp, _, _ := sshclient.Fingerprint(nextKey)
	old, _, _ := sshclient.Fingerprint(srv.AgentKey)
	s.auditAgent(ctx, srv.OrgID, actorID, "server.agent_key_rotated", serverID, map[string]any{"name": srv.Name, "fingerprint": fp, "previous": old})
	return fp, nil
}

// RevokeAgent forgets the agent's key and closes its connection; the agent
// stops itself. With uninstall it is first asked to remove itself, while
// it is still connected.
func (s *Service) RevokeAgent(ctx context.Context, actorID, serverID string, uninstall bool) error {
	if s.agents == nil {
		return ErrAgentsNotConfigured
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.AgentKey == "" {
		return ErrNoAgent
	}
	removed := uninstall && s.uninstallAgent(ctx, srv)
	if _, err := s.agents.store.RevokeAgent(ctx, serverID); err != nil {
		return err
	}
	s.agents.hub.Drop(serverID)
	if s.conn != nil {
		s.conn.Invalidate(serverID)
	}
	fp, _, _ := sshclient.Fingerprint(srv.AgentKey)
	s.auditAgent(ctx, srv.OrgID, actorID, "server.agent_revoked", serverID, map[string]any{"name": srv.Name, "fingerprint": fp, "uninstalled": removed})
	return nil
}

// uninstallAgent asks the server's agent, when connected, to remove itself,
// and reports whether it did.
func (s *Service) uninstallAgent(ctx context.Context, srv *Server) bool {
	if st, _ := s.agents.hub.State(srv.ID); !st.Connected {
		return false
	}
	asAgent := *srv
	asAgent.Connection = ConnectionAgent
	uctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := s.SSH(&asAgent).Subsystem(uctx, agentproto.SubsystemUninstall, nil)
	return err == nil
}

// AuditJoinToken records a token made for a server.
func (s *Service) AuditJoinToken(ctx context.Context, orgID, actorID, serverID, name string, rejoin bool) {
	if s.agents == nil {
		return
	}
	s.auditAgent(ctx, orgID, actorID, "server.agent_token_created", serverID, map[string]any{"name": name, "rejoin": rejoin})
}

// shellQuote quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
