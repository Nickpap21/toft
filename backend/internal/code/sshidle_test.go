package code

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/testenv"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// TestSSHIdleSessionsGiveSlotsBack: an account that opens git sessions
// over SSH and sends nothing is refused past its cap, with the reason; each
// session ends after the idle limit and its slot is free again.
func TestSSHIdleSessionsGiveSlotsBack(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git: %v", err)
	}
	sockDir, err := os.MkdirTemp("", "ti")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	const idle = 2 * time.Second
	executor := &http.Server{Handler: gitstore.NewExecutor(t.TempDir()).WithSessionIdle(idle).Handler()}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	repoID := "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	repo := &fakeRepo{
		targets: map[string]*Target{"acme/shop": {ID: repoID, OrgID: "o1", ProjectID: "p1"}},
		keys:    map[string][2]string{},
	}
	svc := NewService(repo, gitstore.NewClient(sock), fakeMembers{"dev": {"o1": "developer"}}, func() string { return "panel.test" }, 2222, zap.NewNop())

	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewSignerFromKey(hostPriv)
	sshSrv := NewSSHServer(svc, hostKey, zap.NewNop())
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go sshSrv.Serve(sshLn)
	t.Cleanup(sshSrv.Close)

	_, devPriv, _ := ed25519.GenerateKey(rand.Reader)
	devKey, _ := ssh.NewSignerFromKey(devPriv)
	repo.keys[ssh.FingerprintSHA256(devKey.PublicKey())] = [2]string{"k-dev", "dev"}
	client, err := ssh.Dial("tcp", sshLn.Addr().String(), &ssh.ClientConfig{
		User: "git", Auth: []ssh.AuthMethod{ssh.PublicKeys(devKey)},
		HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	type session struct {
		s      *ssh.Session
		stderr *bytes.Buffer
	}
	// open starts upload-pack and reads the start of its advertisement: git
	// is running and holds a slot. It answers false when git never started.
	open := func() (session, bool) {
		t.Helper()
		s, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		out := session{s: s, stderr: &bytes.Buffer{}}
		s.Stderr = out.stderr
		stdout, _ := s.StdoutPipe()
		if _, err := s.StdinPipe(); err != nil { // held open, never written
			t.Fatal(err)
		}
		if err := s.Start("git-upload-pack '/acme/shop.git'"); err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadFull(stdout, make([]byte, 4))
		return out, err == nil
	}
	wait := func(s session) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- s.s.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(idle + 15*time.Second):
			t.Fatal("an idle session was not closed")
			return nil
		}
	}

	var held []session
	for len(held) < 16 {
		s, ok := open()
		if !ok {
			if err := wait(s); err == nil || !strings.Contains(s.stderr.String(), "your account has") {
				t.Errorf("refused past the cap without the reason: %v\n%s", err, s.stderr)
			}
			break
		}
		held = append(held, s)
	}
	if len(held) == 16 {
		t.Fatal("one account held every slot")
	}
	for _, s := range held {
		if err := wait(s); err == nil || !strings.Contains(s.stderr.String(), "stopped moving") {
			t.Fatalf("an idle session ended: %v\n%s", err, s.stderr)
		}
	}
	s, ok := open()
	if !ok {
		t.Fatalf("no slot after the idle sessions ended:\n%s", s.stderr)
	}
	s.s.Close()
}
