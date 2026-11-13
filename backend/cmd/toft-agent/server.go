package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"golang.org/x/crypto/ssh"
)

// errRestart ends the connection after an upgrade: the service exits and
// systemd starts the new version.
var errRestart = errors.New("restart into the new version")

// agentServer serves SSH to the panel over one connection. It accepts only
// the panel's pinned key and only the channels phase 1 needs (D-106):
// sessions (exec, shell, the typed subsystems) and the Docker socket.
type agentServer struct {
	cfg    *config
	signer *ssh.Signer
	// rollouts runs the panel's rollouts (nil without Docker); restartNow
	// ends the service once an upgrade waited for them.
	rollouts   *rollouts
	restartNow func()

	mu      sync.Mutex
	restart bool
}

func (a *agentServer) serve(ctx context.Context, nc net.Conn, onAuth func()) error {
	sc := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, a.checkPanelKey(key)
		},
		ServerVersion: "SSH-2.0-toft-agent",
	}
	sc.AddHostKey(*a.signer)

	// Before the handshake too: a panel that never signs in (the server
	// moved to SSH) must not hold up a stop.
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	sconn, chans, reqs, err := ssh.NewServerConn(nc, sc)
	if err != nil {
		return fmt.Errorf("SSH handshake with the panel: %w", err)
	}
	defer sconn.Close()
	onAuth()
	go func() {
		for r := range reqs {
			// keepalive@openssh.com and anything else: answer, refuse.
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	var wg sync.WaitGroup
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			ch, creqs, err := nch.Accept()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.session(ctx, sconn, ch, creqs)
			}()
		case "direct-streamlocal@openssh.com":
			wg.Add(1)
			go func() {
				defer wg.Done()
				streamLocal(nch)
			}()
		default:
			_ = nch.Reject(ssh.UnknownChannelType, "not served by toft-agent")
		}
	}
	wg.Wait()
	a.mu.Lock()
	restart := a.restart
	a.mu.Unlock()
	if restart {
		return errRestart
	}
	return errors.New("the panel closed the connection")
}

// checkPanelKey accepts the pinned panel key. Before one is pinned (the
// first connection after joining) it accepts the key whose fingerprint the
// install script gave, and pins it.
func (a *agentServer) checkPanelKey(key ssh.PublicKey) error {
	presented := authorizedKey(key)
	if raw, err := os.ReadFile(panelKeyFile()); err == nil {
		pinned, _, _, _, err := ssh.ParseAuthorizedKey(raw)
		if err == nil && bytes.Equal(pinned.Marshal(), key.Marshal()) {
			return nil
		}
		slog.Warn("refused a connection signed by another panel key", "presented", ssh.FingerprintSHA256(key))
		return errors.New("not this agent's panel")
	}
	if a.cfg.PanelKeyFP == "" || ssh.FingerprintSHA256(key) != a.cfg.PanelKeyFP {
		slog.Warn("refused a panel key that is not the one the join named", "presented", ssh.FingerprintSHA256(key), "expected", a.cfg.PanelKeyFP)
		return errors.New("not this agent's panel")
	}
	if err := writeFileAtomic(panelKeyFile(), []byte(presented+"\n"), 0o644); err != nil {
		return fmt.Errorf("pin the panel key: %w", err)
	}
	slog.Info("pinned the panel's key", "fingerprint", a.cfg.PanelKeyFP)
	return nil
}

// streamLocal opens the Docker socket, the only Unix socket the panel may
// reach, and copies both ways.
func streamLocal(nch ssh.NewChannel) {
	var p struct {
		Path      string
		Reserved0 string
		Reserved1 uint32
	}
	if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	if p.Path != agentproto.DockerSocket {
		_ = nch.Reject(ssh.Prohibited, "only the Docker socket")
		return
	}
	uc, err := net.Dial("unix", dockerSock)
	if err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		_ = uc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ch, uc); _ = ch.CloseWrite(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(uc, ch); closeWrite(uc); done <- struct{}{} }()
	<-done
	<-done
	_ = ch.Close()
	_ = uc.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
