package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	dockerclient "github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeBuilds is a Docker that answers each POST /build with the next of its
// streams, and keeps the build context each one was sent.
type fakeBuilds struct {
	mu       sync.Mutex
	streams  []string
	contexts []string
	onBuild  func() // runs after each build has answered, when set
}

func (f *fakeBuilds) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/build") {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	n := len(f.contexts)
	f.contexts = append(f.contexts, string(body))
	f.mu.Unlock()
	_, _ = io.WriteString(w, f.streams[n])
	if f.onBuild != nil {
		f.onBuild()
	}
}

func (f *fakeBuilds) orchestrator(t *testing.T) *LocalOrchestrator {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	dc, err := dockerclient.NewClientWithOpts(dockerclient.WithHost("tcp://"+srv.Listener.Addr().String()), dockerclient.WithVersion("1.47"))
	require.NoError(t, err)
	return &LocalOrchestrator{dockerClient: dc, log: zap.NewNop()}
}

const (
	vanished = `{"stream":"Step 1/2 : FROM alpine\n"}` + "\n" + `{"error":"unable to find image \"sha256:94181e76\""}` + "\n"
	built    = `{"stream":"Step 1/2 : FROM alpine\n"}` + "\n" + `{"stream":"Successfully built 94181e76\n"}` + "\n"
)

// A merge built while its preview's teardown removes the preview's images:
// the classic builder's cache read an image the teardown took, and the
// build failed (the release drill's 0.25 s failure). It is built again,
// once, from the same context.
func TestBuildFromArchiveRetriesAnImageRemovedUnderIt(t *testing.T) {
	f := &fakeBuilds{streams: []string{vanished, built}}
	o := f.orchestrator(t)

	var logged []string
	err := o.BuildFromArchive(context.Background(), strings.NewReader("the tree's tar"), "", "paas/svc:dep", nil, nil, func(l string) { logged = append(logged, l) })

	require.NoError(t, err)
	assert.Equal(t, []string{"the tree's tar", "the tree's tar"}, f.contexts, "two builds, the second sent the whole context again")
	assert.Contains(t, strings.Join(logged, "\n"), "building again", "the deployment log says it built again")
}

// Any other failure is the build's own: it is not run again.
func TestBuildFromArchiveDoesNotRetryOtherFailures(t *testing.T) {
	f := &fakeBuilds{streams: []string{`{"error":"COPY failed: file not found"}` + "\n", built}}
	o := f.orchestrator(t)

	err := o.BuildFromArchive(context.Background(), strings.NewReader("tar"), "", "paas/svc:dep", nil, nil, func(string) {})

	require.ErrorContains(t, err, "COPY failed")
	assert.Len(t, f.contexts, 1)
}

// A deploy cancelled while its build failed is not built again.
func TestBuildFromArchiveDoesNotRetryOnceCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeBuilds{streams: []string{vanished, built}, onBuild: cancel}
	o := f.orchestrator(t)

	var logged []string
	err := o.BuildFromArchive(ctx, strings.NewReader("tar"), "", "paas/svc:dep", nil, nil, func(l string) { logged = append(logged, l) })

	require.Error(t, err)
	assert.Len(t, f.contexts, 1)
	assert.NotContains(t, strings.Join(logged, "\n"), "building again")
}

// With no room for the copy of its context, a build runs as it did before
// the retry existed: only without the retry.
func TestBuildFromArchiveBuildsWithoutRoomForACopy(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	f := &fakeBuilds{streams: []string{built}}
	o := f.orchestrator(t)

	err := o.BuildFromArchive(context.Background(), strings.NewReader("tar"), "", "paas/svc:dep", nil, nil, func(string) {})

	require.NoError(t, err)
	assert.Equal(t, []string{"tar"}, f.contexts)
}
