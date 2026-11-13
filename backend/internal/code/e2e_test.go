package code

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// fakeKeys: API key → who it acts for.
type fakeKeys map[string]middleware.APIKeyIdentity

func (k fakeKeys) ValidateAPIKey(_ context.Context, raw string) (middleware.APIKeyIdentity, error) {
	if id, ok := k[raw]; ok {
		return id, nil
	}
	return middleware.APIKeyIdentity{}, errors.New("invalid api key")
}

// gitEnv runs the git client with no configuration of the machine's.
func gitEnv(t *testing.T) []string {
	home := t.TempDir()
	return []string{
		"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x",
	}
}

func run(t *testing.T, env []string, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env, cmd.Dir = env, dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestGitOverHTTPAndSSH clones and pushes through both front doors with the
// real git client, against the executor on a socket, as the panel runs it.
func TestGitOverHTTPAndSSH(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		testenv.Skip(t, "git is not installed")
	}
	gin.SetMode(gin.TestMode)

	// The executor, on a socket short enough for macOS's 104-byte limit.
	sockDir, err := os.MkdirTemp("", "tg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	executor := &http.Server{Handler: gitstore.NewExecutor(t.TempDir()).Handler()}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	repoID := "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	repo := &fakeRepo{
		targets: map[string]*Target{"acme/shop": {ID: repoID, OrgID: "o1", ProjectID: "p1"}},
		keys:    map[string][2]string{},
	}
	members := fakeMembers{"dev": {"o1": "developer"}, "viewer": {"o1": "viewer"}}
	svc := NewService(repo, gitstore.NewClient(sock), members, func() string { return "panel.test" }, 2222, zap.NewNop())

	keys := fakeKeys{
		"paas_dev":    {UserID: "dev"},
		"paas_viewer": {UserID: "viewer"},
		"paas_read":   {UserID: "dev", ProjectID: "p1", Scopes: []string{ScopeCodeRead}},
		"paas_other":  {UserID: "dev", ProjectID: "p9", Scopes: []string{ScopeCodeWrite}},
	}
	r := gin.New()
	NewGitHTTP(svc, keys, zap.NewNop()).RegisterRoutes(r, func(c *gin.Context) { c.Next() })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	url := func(key, path string) string {
		return strings.Replace(srv.URL, "http://", "http://git:"+key+"@", 1) + "/" + path
	}

	env := gitEnv(t)
	work := t.TempDir()

	// An empty repository clones; a developer pushes to it.
	if out, err := run(t, env, work, "git", "clone", url("paas_dev", "acme/shop.git"), "dev"); err != nil {
		t.Fatalf("clone empty: %v\n%s", err, out)
	}
	dev := filepath.Join(work, "dev")
	os.WriteFile(filepath.Join(dev, "README.md"), []byte("# shop\n"), 0o644)
	for _, args := range [][]string{{"git", "add", "."}, {"git", "commit", "-qm", "first"}, {"git", "push", "-q", "origin", "HEAD:main"}} {
		if out, err := run(t, env, dev, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Server-owned names refuse a push.
	if out, err := run(t, env, dev, "git", "push", "origin", "HEAD:refs/pull/1/head"); err == nil || !strings.Contains(out, "hidden ref") {
		t.Fatalf("pushed to refs/pull:\n%s", out)
	}

	// A viewer and a code_read key clone and can't push; a key for another
	// project and a wrong key don't see the repository.
	for _, key := range []string{"paas_viewer", "paas_read"} {
		dir := key
		if out, err := run(t, env, work, "git", "clone", "-q", url(key, "acme/shop"), dir); err != nil {
			t.Fatalf("%s clone: %v\n%s", key, err, out)
		}
		if _, err := os.Stat(filepath.Join(work, dir, "README.md")); err != nil {
			t.Fatalf("%s: the clone has no README", key)
		}
		os.WriteFile(filepath.Join(work, dir, "x"), []byte("x"), 0o644)
		run(t, env, filepath.Join(work, dir), "git", "add", ".")
		run(t, env, filepath.Join(work, dir), "git", "commit", "-qm", "x")
		if out, err := run(t, env, filepath.Join(work, dir), "git", "push", "origin", "HEAD:main"); err == nil || !strings.Contains(out, "403") {
			t.Fatalf("%s pushed: %v\n%s", key, err, out)
		}
	}
	for _, key := range []string{"paas_other", "paas_nope"} {
		if out, err := run(t, env, work, "git", "ls-remote", url(key, "acme/shop.git")); err == nil {
			t.Fatalf("%s reached the repository:\n%s", key, out)
		}
	}
	if out, err := run(t, env, work, "git", "ls-remote", url("paas_dev", "acme/nothing.git")); err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("an unknown repository: %v\n%s", err, out)
	}

	// ── SSH ──────────────────────────────────────────────────────────────────
	if _, err := exec.LookPath("ssh"); err != nil {
		testenv.Skip(t, "ssh is not installed")
	}
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewSignerFromKey(hostPriv)
	sshSrv := NewSSHServer(svc, hostKey, zap.NewNop())
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go sshSrv.Serve(sshLn)
	t.Cleanup(sshSrv.Close)
	port := sshLn.Addr().(*net.TCPAddr).Port

	keyFor := func(user string) string {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		sp, _ := ssh.NewPublicKey(pub)
		repo.keys[ssh.FingerprintSHA256(sp)] = [2]string{"k-" + user, user}
		block, _ := ssh.MarshalPrivateKey(priv, "")
		path := filepath.Join(t.TempDir(), "id")
		os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
		return path
	}
	sshEnv := func(key string) []string {
		return append(env, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR", key))
	}
	sshURL := fmt.Sprintf("ssh://git@127.0.0.1:%d/acme/shop.git", port)

	devKey := sshEnv(keyFor("dev"))
	if out, err := run(t, devKey, work, "git", "clone", "-q", sshURL, "ssh-dev"); err != nil {
		t.Fatalf("ssh clone: %v\n%s", err, out)
	}
	sshDev := filepath.Join(work, "ssh-dev")
	os.WriteFile(filepath.Join(sshDev, "b"), []byte("b"), 0o644)
	run(t, devKey, sshDev, "git", "add", ".")
	run(t, devKey, sshDev, "git", "commit", "-qm", "b")
	if out, err := run(t, devKey, sshDev, "git", "push", "-q", "origin", "HEAD:main"); err != nil {
		t.Fatalf("ssh push: %v\n%s", err, out)
	}
	// Protocol v0 as well as git's default v2.
	if out, err := run(t, devKey, work, "git", "-c", "protocol.version=0", "clone", "-q", sshURL, "ssh-v0"); err != nil {
		t.Fatalf("ssh clone, protocol v0: %v\n%s", err, out)
	}

	viewerKey := sshEnv(keyFor("viewer"))
	if out, err := run(t, viewerKey, sshDev, "git", "push", sshURL, "HEAD:refs/heads/viewer"); err == nil || !strings.Contains(out, "may read this repository but not push") {
		t.Fatalf("a viewer pushed over ssh: %v\n%s", err, out)
	}
	strangerKey := sshEnv(keyFor("stranger"))
	if out, err := run(t, strangerKey, work, "git", "ls-remote", sshURL); err == nil || !strings.Contains(out, "Repository not found") {
		t.Fatalf("a stranger reached the repository: %v\n%s", err, out)
	}
	// An unknown key is refused at authentication; a shell is refused.
	unknown := filepath.Join(t.TempDir(), "id")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	os.WriteFile(unknown, pem.EncodeToMemory(block), 0o600)
	if out, err := run(t, sshEnv(unknown), work, "git", "ls-remote", sshURL); err == nil {
		t.Fatalf("an unknown key got in:\n%s", out)
	}
	shell := strings.Fields(strings.TrimPrefix(devKey[len(devKey)-1], "GIT_SSH_COMMAND="))
	if out, _ := run(t, env, work, append(shell, "-T", "-p", fmt.Sprint(port), "git@127.0.0.1")...); !strings.Contains(out, "does not provide shell access") {
		t.Fatalf("a shell:\n%s", out)
	}
}
