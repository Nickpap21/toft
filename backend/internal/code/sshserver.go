package code

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// SSHServer is git's SSH front door (port 2222, D-28): public keys only,
// and a session may only set GIT_PROTOCOL and run upload-pack or
// receive-pack. No shell, PTY, subsystem or forwarding.
type SSHServer struct {
	svc    *Service
	config *ssh.ServerConfig
	log    *zap.Logger

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	perIP  map[string]int
	closed bool
}

// Connection limits: initial values, not measured.
const (
	maxSSHConns      = 256
	maxSSHConnsPerIP = 20
	sshHandshake     = 10 * time.Second
)

// userExt carries the account an SSH key belongs to from authentication to
// the session: read only from the connection's Permissions, never from
// which key the callback saw last (CVE-2024-45337).
const userExt = "toft-user-id"

func NewSSHServer(svc *Service, hostKey ssh.Signer, log *zap.Logger) *SSHServer {
	s := &SSHServer{svc: svc, log: log, conns: map[net.Conn]struct{}{}, perIP: map[string]int{}}
	s.config = &ssh.ServerConfig{
		MaxAuthTries:  6,
		ServerVersion: "SSH-2.0-Toft",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			userID, err := svc.KeyOwner(ctx, ssh.FingerprintSHA256(key))
			if err != nil {
				return nil, errors.New("unknown key")
			}
			return &ssh.Permissions{Extensions: map[string]string{userExt: userID}}, nil
		},
	}
	s.config.AddHostKey(hostKey)
	return s
}

// Serve accepts connections on ln until Close.
func (s *SSHServer) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return nil
	}
	s.ln = ln
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("git ssh: accept: %w", err)
		}
		if !s.track(conn) {
			conn.Close()
			continue
		}
		go s.handle(conn)
	}
}

// Close stops accepting and ends every open connection, so a stopping
// backend doesn't wait on them.
func (s *SSHServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.ln != nil {
		s.ln.Close()
	}
	for c := range s.conns {
		c.Close()
	}
}

func ipOf(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// track admits conn under the limits.
func (s *SSHServer) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ip := ipOf(conn)
	if s.closed || len(s.conns) >= maxSSHConns || s.perIP[ip] >= maxSSHConnsPerIP {
		return false
	}
	s.conns[conn] = struct{}{}
	s.perIP[ip]++
	return true
}

func (s *SSHServer) untrack(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
	ip := ipOf(conn)
	if s.perIP[ip]--; s.perIP[ip] <= 0 {
		delete(s.perIP, ip)
	}
}

func (s *SSHServer) handle(conn net.Conn) {
	defer s.untrack(conn)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sshHandshake))
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	defer sc.Close()
	go ssh.DiscardRequests(reqs) // global requests, forwarding included: refused
	userID := sc.Permissions.Extensions[userExt]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only git sessions are served")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ctx, userID, ch, chReqs)
	}
}

// exitStatus is the payload of an exit-status request.
type exitStatus struct{ Status uint32 }

func (s *SSHServer) session(ctx context.Context, userID string, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	protocol := ""
	for req := range reqs {
		switch req.Type {
		case "env":
			var kv struct{ Name, Value string }
			ok := ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL"
			if ok {
				protocol = kv.Value
			}
			_ = req.Reply(ok, nil)
		case "shell":
			_ = req.Reply(true, nil)
			fmt.Fprint(ch.Stderr(), "You've authenticated, but Toft does not provide shell access. Clone with git.\n")
			s.exit(ch, 1)
			return
		case "exec":
			var cmd struct{ Command string }
			if ssh.Unmarshal(req.Payload, &cmd) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			s.exit(ch, s.exec(ctx, userID, cmd.Command, protocol, ch))
			return
		default: // pty-req, subsystem, x11-req, agent forwarding…
			_ = req.Reply(false, nil)
		}
	}
}

func (s *SSHServer) exit(ch ssh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{uint32(code)}))
}

// exec runs what a git client asked for and returns its exit code.
func (s *SSHServer) exec(ctx context.Context, userID, command, protocol string, ch ssh.Channel) int {
	fail := func(msg string) int {
		fmt.Fprintf(ch.Stderr(), "ERROR: %s\n", msg)
		return 1
	}
	service, path, ok := ParseSSHCommand(command)
	if !ok {
		return fail("Toft serves git only: git-upload-pack or git-receive-pack '<workspace>/<repository>.git'")
	}
	t, err := s.svc.Resolve(ctx, path)
	if err == nil {
		err = s.svc.Authorize(ctx, Actor{UserID: userID}, t, service == "receive-pack")
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return fail("Repository not found.")
	case errors.Is(err, ErrForbidden):
		return fail(err.Error())
	case err != nil:
		s.log.Error("git ssh: authorize", zap.Error(err))
		return fail("the repository could not be read")
	}
	res, err := s.svc.Serve(ctx, t, Actor{UserID: userID}, service, "stream", protocol, ch, ch)
	if errors.Is(err, gitstore.ErrBusy) {
		return fail(err.Error())
	}
	if err != nil {
		s.log.Warn("git ssh", zap.Error(err), zap.String("repository", t.ID))
		return fail("git is not answering, try again")
	}
	if res.Exit != 0 && res.Stderr != "" {
		fmt.Fprint(ch.Stderr(), res.Stderr)
	}
	if res.Exit < 0 {
		return 1
	}
	return res.Exit
}

// ParseSSHCommand reads `git-upload-pack '<workspace>/<repository>.git'` (or
// receive-pack), the path quoted as git quotes it, with or without a slash.
func ParseSSHCommand(command string) (service, path string, ok bool) {
	program, arg, ok := strings.Cut(strings.TrimSpace(command), " ")
	switch program {
	case "git-upload-pack":
		service = "upload-pack"
	case "git-receive-pack":
		service = "receive-pack"
	default:
		return "", "", false
	}
	arg = strings.TrimSpace(arg)
	if len(arg) >= 2 && arg[0] == '\'' && arg[len(arg)-1] == '\'' {
		arg = arg[1 : len(arg)-1]
	}
	if !ok || arg == "" || strings.ContainsAny(arg, "'\"\\ \t\n") {
		return "", "", false
	}
	return service, arg, true
}
