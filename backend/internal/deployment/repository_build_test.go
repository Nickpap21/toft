package deployment_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoSource reads repositories through the executor's socket, as
// code.Service does, and records the checks reported.
type repoSource struct {
	c       *gitstore.Client
	mu      sync.Mutex
	reports []string
}

func (r *repoSource) Archive(ctx context.Context, _, repositoryID, ref string) (*gitstore.Archive, error) {
	return r.c.Archive(ctx, repositoryID, ref)
}

func (r *repoSource) ReportDeploy(_ context.Context, _, sha, environment, service, state, _, deploymentID string, _ bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, strings.Join([]string{environment, service, state, sha, deploymentID}, " "))
}

func (r *repoSource) FailDeployChecks(context.Context, []string, string) {}

func (r *repoSource) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reports...)
}

// TestBuildFromTheProjectsRepository deploys a service that builds from the
// project's own repository (D-77): the build host gets git archive's tree of
// the branch's tip, without .git, with TOFT_GIT_SHA and TOFT_GIT_BRANCH; the
// deployment records the commit, and its check runs then passes, or fails
// with the build.
func TestBuildFromTheProjectsRepository(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", gitstore.MinVersion[0], gitstore.MinVersion[1], err)
	}
	root := t.TempDir()
	const id = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	bare, _ := gitstore.RepoPath(root, id)
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com"}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	git(root, "init", "-q", "--bare", "-b", "main", bare)
	w := t.TempDir()
	git(w, "init", "-q", "-b", "main")
	for name, body := range map[string]string{"api/Dockerfile": "FROM scratch\n", "api/main.go": "package main\n", "README.md": "# Shop\n"} {
		require.NoError(t, os.MkdirAll(filepath.Join(w, filepath.Dir(name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(w, name), []byte(body), 0o644))
	}
	git(w, "add", ".")
	git(w, "commit", "-qm", "Add the api")
	head := git(w, "rev-parse", "HEAD")
	git(w, "push", "-q", bare, "main")

	sockDir, err := os.MkdirTemp("", "td")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: gitstore.NewExecutor(root).Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	src := &repoSource{c: gitstore.NewClient(sock)}
	repo := &mockRepo{createdID: "dep-1"}
	orch := &mockOrchestrator{}
	svc := newTestService(repo, &mockFactory{orch: orch}, &mockStatusUpdater{})
	svc.WithRepositories(src)
	input := deployment.TriggerInput{
		ServiceID: "svc-1", ProjectID: "p1", SourceType: "git", RepositoryID: id, GitBranch: "main",
		BaseDirectory: "api", ServiceName: "api", EnvironmentName: "production",
	}
	_, err = svc.Trigger(context.Background(), input)
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 10*time.Second)

	orch.mu.Lock()
	assert.Equal(t, "FROM scratch\n", orch.archived["api/Dockerfile"])
	assert.Equal(t, "# Shop\n", orch.archived["README.md"])
	for name := range orch.archived {
		assert.False(t, strings.HasPrefix(name, ".git/"), "the tar holds %s", name)
	}
	require.NotNil(t, orch.buildArgs["TOFT_GIT_SHA"])
	assert.Equal(t, head, *orch.buildArgs["TOFT_GIT_SHA"])
	assert.Equal(t, "main", *orch.buildArgs["TOFT_GIT_BRANCH"])
	orch.mu.Unlock()
	repo.mu.Lock()
	assert.Contains(t, repo.commits, deployment.Commit{SHA: head, Subject: "Add the api"})
	repo.mu.Unlock()
	assert.Equal(t, []string{
		"production api running " + head + " dep-1",
		"production api success " + head + " dep-1",
	}, src.reported())

	// A build that fails fails the check of the commit it built.
	orch.mu.Lock()
	orch.buildErr = errors.New("step 3/5 exited 1")
	orch.mu.Unlock()
	repo.mu.Lock()
	repo.statusHistory = nil
	repo.mu.Unlock()
	_, err = svc.Trigger(context.Background(), input)
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 10*time.Second)
	got := src.reported()
	require.NotEmpty(t, got)
	assert.True(t, strings.HasPrefix(got[len(got)-1], "production api failure "+head), "%v", got)

	// A ref the repository doesn't have fails before any check.
	orch.mu.Lock()
	orch.buildErr = nil
	orch.mu.Unlock()
	repo.mu.Lock()
	repo.statusHistory = nil
	repo.mu.Unlock()
	before := len(src.reported())
	input.GitBranch = "nope"
	_, err = svc.Trigger(context.Background(), input)
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusFailed, 10*time.Second)
	assert.Len(t, src.reported(), before)
}
