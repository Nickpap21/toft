package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// Add a server (D-153): opening the page makes a waiting server, server-<n>,
// and its one-time command; the person's own waiting server is reused. Its
// first join provisions it, requirements first, and its hostname replaces
// the automatic name. A server that can't reach Toft is reached over SSH
// instead, as root, with a key Toft makes. Waiting servers nobody joined are
// removed 24 hours after their last command expired.

// serverNameRe is the rule for a new name or a rename: a hostname label.
var serverNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var (
	// ErrInvalidServerName: a new name breaks the rule.
	ErrInvalidServerName = errors.New("a server's name is lowercase letters, digits and dashes, starting and ending with a letter or digit, up to 63 characters")
	// ErrServerNameTaken: another server of the workspace has the name.
	ErrServerNameTaken = errors.New("another server in this workspace has that name")
	// ErrProvisionRunning: Provision is already running on the server.
	ErrProvisionRunning = errors.New("this server is being provisioned already: follow it on its page")
	// ErrServerJoined: the server isn't waiting any more (an agent joined,
	// it was provisioned, or it moved to SSH).
	ErrServerJoined = errors.New("this server isn't waiting for its first agent any more")
	// ErrServerNotReady: the server is still waiting for its first agent
	// (Server.WaitingForAgent), so nothing is placed on it: 409 server_not_ready.
	ErrServerNotReady = errors.New("this server is still waiting for its agent: finish adding it before using it")
)

// ValidServerName reports whether name follows the rule (D-153).
func ValidServerName(name string) bool { return serverNameRe.MatchString(name) }

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// nameFromHostname turns a hostname into a name that follows the rule, or
// "" when nothing is left: "Hel1_Prod.03.example" is "hel1-prod".
func nameFromHostname(host string) string {
	host, _, _ = strings.Cut(strings.TrimSpace(host), ".")
	n := strings.Trim(nonName.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if len(n) > 63 {
		n = strings.TrimRight(n[:63], "-")
	}
	return n
}

// CheckReady refuses (ErrServerNotReady) a server still waiting for its
// first agent. Every use case that puts something on a server asks it.
func (s *Service) CheckReady(ctx context.Context, serverID string) error {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.WaitingForAgent() {
		return ErrServerNotReady
	}
	return nil
}

// RenameServer gives a server a new name (owners; the handler checks).
func (s *Service) RenameServer(ctx context.Context, id, name string) (*Server, error) {
	if !ValidServerName(name) {
		return nil, ErrInvalidServerName
	}
	if err := s.repo.Rename(ctx, id, name); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

// nameFromHost replaces srv's automatic name with its hostname (the
// provisioning run's first step). Best-effort: a name taken in the
// workspace tries -2 to -5, then the automatic name stays.
func (s *Service) nameFromHost(ctx context.Context, srv *Server, sc shell, logFn func(string)) {
	out, err := sc.Exec("hostname")
	base := nameFromHostname(out)
	if err != nil || base == "" {
		return
	}
	for i := 1; i <= 5; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", strings.TrimRight(base[:min(len(base), 61)], "-"), i)
		}
		named, err := s.repo.NameFromHostname(ctx, srv.ID, name)
		if errors.Is(err, ErrServerNameTaken) {
			continue
		}
		if err != nil {
			s.log.Warn("name the server from its hostname", zap.Error(err), zap.String("server_id", srv.ID))
		} else if named {
			logFn(fmt.Sprintf("Named %s, from its hostname", name))
			srv.Name = name
		}
		return
	}
}

// provisionOnFirstJoin provisions a server its agent joined from Add a
// server: never provisioned and never tried, so a failed run waits for a
// person's Check again or Try again instead of repeating at every
// reconnect. It runs as the person who made the command. A server moved to
// the agent over SSH has no used command and is left alone.
func (s *Service) provisionOnFirstJoin(ctx context.Context, srv *Server) {
	if srv.ProvisionedAt != nil || isLocalAddress(srv.IPAddress) {
		return
	}
	if _, err := s.repo.GetProvisionRun(ctx, srv.ID); !errors.Is(err, ErrNoProvisionRun) {
		return
	}
	actor, err := s.agents.store.LastJoinTokenCreator(ctx, srv.ID)
	if err != nil || actor == "" {
		return
	}
	if _, busy := s.firstJoins.LoadOrStore(srv.ID, true); busy {
		return
	}
	// Detached: the connection's handler returns at once, and Provision is
	// bounded by provisionTimeout.
	go func() {
		defer s.firstJoins.Delete(srv.ID)
		pctx := context.Background()
		if err := s.Provision(pctx, srv.ID, actor, func(string) {}); err != nil {
			s.log.Info("provisioning on the first join stopped", zap.Error(err), zap.String("server_id", srv.ID))
			return
		}
		s.auditAgent(pctx, srv.OrgID, actor, "server.provisioned", srv.ID, map[string]any{"on_join": true})
	}()
}

// WaitingServerSSHKey makes, once, the key a waiting server is reached with
// over SSH, and returns its public line for root's authorized_keys.
func (s *Service) WaitingServerSSHKey(ctx context.Context, id string) (string, error) {
	if s.agents == nil {
		return "", ErrAgentsNotConfigured
	}
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	priv, err := s.agents.store.WaitingSSHKey(ctx, id)
	if err != nil {
		return "", err
	}
	if priv == "" {
		made, err := newSSHKey(srv.Name)
		if err != nil {
			return "", err
		}
		set, err := s.agents.store.SetWaitingSSHKey(ctx, id, made)
		if err != nil {
			return "", err
		}
		if priv = made; !set {
			// Another request made it first.
			if priv, err = s.agents.store.WaitingSSHKey(ctx, id); err != nil {
				return "", err
			}
		}
	}
	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		return "", fmt.Errorf("read the server's key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " toft-" + srv.Name, nil
}

// newSSHKey is a new ed25519 private key in OpenSSH's PEM.
func newSSHKey(name string) (string, error) {
	_, pk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(pk, "toft-"+name)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(block)), nil
}

// UseSSH reaches a waiting server over SSH instead, as root, with key or
// the one WaitingServerSSHKey made. It signs in first, so a wrong address
// or key leaves the server waiting (ErrSSHFailed), and pins the host key
// it presented (trust on first use; Add a server's check shows it to
// compare on the server's console). Its join commands stop working.
func (s *Service) UseSSH(ctx context.Context, id, address string, port int, key string) (*Server, error) {
	if s.agents == nil {
		return nil, ErrAgentsNotConfigured
	}
	srv, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if key == "" {
		made, err := s.agents.store.WaitingSSHKey(ctx, id)
		if err != nil {
			return nil, err
		}
		if made == "" {
			return nil, ErrNoSSHKey
		}
		key = made
	}
	c, err := sshclient.Dial(ctx, sshclient.Config{Host: address, Port: port, User: "root", PrivateKeyPEM: key})
	if err != nil {
		return nil, &sshFailure{hostKeyHint(srv.Name, err)}
	}
	presented := sshclient.MarshalHostKey(c.HostKey())
	_ = c.Close()
	if err := s.agents.store.UseSSH(ctx, id, address, port, key); err != nil {
		return nil, err
	}
	moved, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.pinHostKey(moved)(ctx, presented); err != nil {
		return nil, err
	}
	if s.conn != nil {
		s.conn.Invalidate(id)
	}
	return s.repo.GetByID(ctx, id)
}

// RemoveUnjoinedServers removes waiting servers whose last command expired
// over a day ago and that hold nothing, audited as Toft.
func (s *Service) RemoveUnjoinedServers(ctx context.Context) {
	if s.agents == nil {
		return
	}
	gone, err := s.agents.store.DeleteUnjoinedServers(ctx)
	if err != nil {
		s.log.Warn("remove servers nobody joined", zap.Error(err))
		return
	}
	for _, g := range gone {
		// "toft" is org.ToftActor: the entry lists as Toft.
		s.auditAgent(ctx, g.OrgID, "toft", "server.removed_unused", g.ID, map[string]any{"name": g.Name})
	}
}

// RunUnjoinedSweep runs RemoveUnjoinedServers every hour until ctx ends.
func (s *Service) RunUnjoinedSweep(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RemoveUnjoinedServers(ctx)
		}
	}
}
