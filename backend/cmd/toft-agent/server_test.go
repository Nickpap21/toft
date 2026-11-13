package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"golang.org/x/crypto/ssh"
)

// panelKey returns a PEM private key for the panel's side of the test.
func panelKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(priv)
	return string(pem.EncodeToMemory(block)), signer.PublicKey()
}

// connect runs an agent server on one end of a pipe and the panel's SSH
// client (with the agent's key pinned) on the other.
func connect(t *testing.T, panelPEM string, panelFP string) (*sshclient.Client, error) {
	t.Helper()
	stateDir = t.TempDir()
	return connectWith(t, panelPEM, panelFP, nil)
}

// connectWith is connect for an agent that runs rollouts with ro, in the
// stateDir the caller set.
func connectWith(t *testing.T, panelPEM string, panelFP string, ro *rollouts) (*sshclient.Client, error) {
	t.Helper()
	signer, err := loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config{Panel: "https://panel.test", Server: "s1", PanelKeyFP: panelFP}
	a, b := socketPair(t)
	srv := &agentServer{cfg: cfg, signer: &signer, rollouts: ro, restartNow: func() {}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.serve(ctx, a, func() {}) }()
	return sshclient.Dial(context.Background(), sshclient.Config{
		Host: "s1", User: agentproto.SSHUser, PrivateKeyPEM: panelPEM,
		PinnedHostKey: authorizedKey(signer.PublicKey()),
		Conn:          func(context.Context) (net.Conn, error) { return b, nil },
	})
}

func TestAgentServesThePinnedPanel(t *testing.T) {
	pem, pub := panelKey(t)
	c, err := connect(t, pem, ssh.FingerprintSHA256(pub))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	out, err := c.ExecContext(ctx, "echo hello; echo oops >&2")
	if err != nil || !strings.Contains(out, "hello") || !strings.Contains(out, "oops") {
		t.Fatalf("exec: %q %v", out, err)
	}
	var exit *ssh.ExitError
	if _, err := c.ExecContext(ctx, "exit 3"); !asExit(err, &exit) || exit.ExitStatus() != 3 {
		t.Fatalf("exit status 3: %v", err)
	}
	out, err = c.ExecInputContext(ctx, "cat", strings.NewReader("from stdin"))
	if err != nil || out != "from stdin" {
		t.Fatalf("stdin: %q %v", out, err)
	}
	// The panel key was pinned on that first connection.
	if raw, err := os.ReadFile(filepath.Join(stateDir, "panel.pub")); err != nil || !strings.HasPrefix(string(raw), "ssh-ed25519 ") {
		t.Fatalf("panel key not pinned: %q %v", raw, err)
	}
	// A typed request is served by the agent, with its stdin and output.
	out, err = c.SubsystemContext(ctx, agentproto.SubsystemRotate, nil)
	if err != nil || !strings.HasPrefix(out, "ssh-ed25519 ") {
		t.Fatalf("rotate: %q %v", out, err)
	}
	out, err = c.SubsystemContext(ctx, agentproto.SubsystemUpgrade, strings.NewReader(`{"version":"v0.0.1","size":3}`+"\nabc"))
	if err == nil || !strings.Contains(out, "error:") {
		t.Fatalf("a bad upgrade must be refused with its reason: %q %v", out, err)
	}
	// Only the Docker socket.
	if _, err := c.DialUnix(ctx, "/run/other.sock"); err == nil {
		t.Fatal("a socket other than Docker's must be refused")
	}
	// A cancelled command is stopped.
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = c.ExecContext(cctx, "sleep 30")
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancelling did not stop the command")
	}
}

func TestAgentDockerSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			_, _ = c.Write([]byte("docker"))
			_ = c.Close()
		}
	}()
	old := dockerSock
	dockerSock = sock
	defer func() { dockerSock = old }()

	pem, pub := panelKey(t)
	c, err := connect(t, pem, ssh.FingerprintSHA256(pub))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	nc, err := c.DialUnix(context.Background(), agentproto.DockerSocket)
	if err != nil {
		t.Fatalf("docker socket: %v", err)
	}
	buf := make([]byte, 6)
	if _, err := nc.Read(buf); err != nil || string(buf) != "docker" {
		t.Fatalf("read %q %v", buf, err)
	}
}

func TestAgentRefusesAnotherPanel(t *testing.T) {
	pem, _ := panelKey(t)
	_, other := panelKey(t)
	if c, err := connect(t, pem, ssh.FingerprintSHA256(other)); err == nil {
		c.Close()
		t.Fatal("a panel key the join didn't name must be refused")
	}
}

// A panel that admits the agent but never signs in (the server moved to
// SSH) must not hold up a stop: production waited for systemd's 90 s kill.
func TestStopWhileWaitingForThePanel(t *testing.T) {
	stateDir = t.TempDir()
	signer, err := loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := socketPair(t)
	srv := &agentServer{cfg: &config{}, signer: &signer}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.serve(ctx, a, func() {}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve ignored the stop while waiting for the panel's handshake")
	}
}

func TestBackoffStaysInBounds(t *testing.T) {
	for n := 1; n < 40; n++ {
		if d := backoff(n); d < backoffMin || d > backoffMax {
			t.Fatalf("backoff(%d) = %s", n, d)
		}
	}
}

func asExit(err error, target **ssh.ExitError) bool {
	for err != nil {
		if e, ok := err.(*ssh.ExitError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// socketPair is a connected TCP pair (net.Pipe is unbuffered, and both SSH
// ends send their version first).
func socketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	b, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	a, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
