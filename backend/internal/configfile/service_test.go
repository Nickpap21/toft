package configfile

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type listRepo struct{ files []ConfigFile }

func (r listRepo) List(context.Context, string) ([]ConfigFile, error) { return r.files, nil }
func (r listRepo) Upsert(context.Context, string, UpsertInput) (ConfigFile, error) {
	return ConfigFile{}, errors.New("not implemented")
}
func (r listRepo) Delete(context.Context, string, string) (string, error) { return "", nil }

// execHost records ExecInput calls; nothing else of RemoteHost is used.
type execHost struct {
	sshpool.RemoteHost
	cmds   []string
	inputs []string
	ctxs   []context.Context
	fail   map[string]bool // commands containing a key fail
}

func (h *execHost) ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	in, _ := io.ReadAll(stdin)
	h.inputs = append(h.inputs, string(in))
	h.cmds = append(h.cmds, cmd)
	h.ctxs = append(h.ctxs, ctx)
	for frag := range h.fail {
		if strings.Contains(cmd, frag) {
			return "", errors.New("exit status 1")
		}
	}
	return "", nil
}

type countingProvider struct {
	h     *execHost
	calls int
}

func (p *countingProvider) SSH(*serverPkg.Server) sshpool.RemoteHost {
	p.calls++
	return p.h
}

func TestWriteToHostRemoteUsesOnePooledConnection(t *testing.T) {
	repo := listRepo{files: []ConfigFile{
		{Path: "/etc/nginx/conf.d/default.conf", Content: "server {}\n"},
		{Path: "/app/config.yml", Content: "a: 1\n"},
		{Path: "/app/broken.yml", Content: "x"},
	}}
	host := &execHost{fail: map[string]bool{"broken.yml": true}}
	provider := &countingProvider{h: host}
	s := NewService(repo, zap.NewNop())
	s.WithSSH(provider)

	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "deploy")
	mounts, err := s.WriteToHost(ctx, "svc-1", &serverPkg.Server{ID: "srv-1", Name: "web-1"})
	require.NoError(t, err)

	assert.Equal(t, 1, provider.calls, "one connection for every file")
	require.Len(t, host.cmds, 3)
	for _, c := range host.ctxs {
		assert.Equal(t, "deploy", c.Value(key{}), "the deploy's ctx reaches every command")
	}
	assert.Equal(t,
		"mkdir -p '/var/paas/config/svc-1/etc/nginx/conf.d' && cat > '/var/paas/config/svc-1/etc/nginx/conf.d/default.conf'",
		host.cmds[0])
	assert.Equal(t, "server {}\n", host.inputs[0], "the content goes on stdin, not the command line")

	// The failed write is skipped, the others are mounted.
	require.Len(t, mounts, 2)
	assert.Equal(t, "/var/paas/config/svc-1/etc/nginx/conf.d/default.conf", mounts[0].HostPath)
	assert.Equal(t, "/app/config.yml", mounts[1].Target)
	assert.True(t, mounts[1].ReadOnly)
}

func TestWriteToHostRemoteWithoutSSHSkips(t *testing.T) {
	s := NewService(listRepo{files: []ConfigFile{{Path: "/app/a.yml", Content: "a"}}}, zap.NewNop())
	mounts, err := s.WriteToHost(context.Background(), "svc-1", &serverPkg.Server{ID: "srv-1"})
	require.NoError(t, err)
	assert.Empty(t, mounts)
}

func TestWriteToHostNoFilesNeedsNoConnection(t *testing.T) {
	provider := &countingProvider{h: &execHost{}}
	s := NewService(listRepo{}, zap.NewNop())
	s.WithSSH(provider)
	mounts, err := s.WriteToHost(context.Background(), "svc-1", &serverPkg.Server{ID: "srv-1"})
	require.NoError(t, err)
	assert.Empty(t, mounts)
	assert.Zero(t, provider.calls)
}

// On the control-plane server config files go under the data dir, which is
// mounted at the same path on the host, so Docker finds the bind-mount source.
func TestWriteToHostLocalUsesTheDataDir(t *testing.T) {
	dataDir := t.TempDir()
	s := NewService(listRepo{files: []ConfigFile{{Path: "/etc/app.yml", Content: "a: 1\n"}}}, zap.NewNop())
	s.WithDataDir(dataDir)

	mounts, err := s.WriteToHost(context.Background(), "svc-1", nil)
	require.NoError(t, err)
	require.Len(t, mounts, 1)
	want := filepath.Join(dataDir, "config", "svc-1", "etc", "app.yml")
	assert.Equal(t, want, mounts[0].HostPath)
	b, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, "a: 1\n", string(b))
}
