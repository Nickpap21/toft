package service

import (
	"context"
	"errors"
	"testing"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAdviseIsolation(t *testing.T) {
	for _, c := range []struct {
		slug, image, server string
		want                IsolationAdvice
	}{
		{"postgres", "", "srv", IsolationAdvice{IsolationStandard, "database"}},
		{"", "redis:7", "", IsolationAdvice{IsolationStandard, "database"}}, // even on the panel's server
		{"pgadmin", "dpage/pgadmin4", "srv", IsolationAdvice{IsolationSandboxed, "tool"}},
		{"", "dpage/pgadmin4:8", "srv", IsolationAdvice{IsolationSandboxed, "tool"}}, // typed in (D-150)
		{"", "ghcr.io/acme/web:1", "", IsolationAdvice{IsolationSandboxed, "panel_server"}},
		{"", "ghcr.io/acme/web:1", "srv", IsolationAdvice{IsolationStandard, "app"}},
	} {
		assert.Equal(t, c.want, AdviseIsolation(c.slug, c.image, c.server), "%s %s %q", c.slug, c.image, c.server)
	}
	b, err := Service{ServerID: "srv", AddonSlug: "adminer"}.MarshalJSON()
	require.NoError(t, err)
	assert.Contains(t, string(b), `"isolation_advice":{"level":"sandboxed","reason":"tool"}`)
}

type isoRepo struct {
	fakeRepo
	set []string
	def string // the workspace's default; "" is standard
}

func (r *isoRepo) Isolation(context.Context, string) (string, string, error) {
	if r.def == "" {
		return "", IsolationStandard, nil
	}
	return "", r.def, nil
}

func (r *isoRepo) SetIsolation(_ context.Context, id, level string) error {
	r.set = append(r.set, id+"="+level)
	return nil
}

type sandboxes map[string]string // server → state

func (s sandboxes) Sandboxed(_ context.Context, id string) (serverPkg.Level, string, error) {
	if s[id] == "error" {
		return serverPkg.Level{}, "", errors.New("down")
	}
	return serverPkg.Level{ID: "sandboxed", State: s[id]}, "", nil
}

// SandboxedStrict reads "strict:"+id.
func (s sandboxes) SandboxedStrict(_ context.Context, id string) (serverPkg.Level, string, error) {
	return serverPkg.Level{ID: "sandboxed_strict", State: s["strict:"+id]}, "", nil
}

// A new tool, or app on the panel's own server, starts Sandboxed where its
// server can run it (D-99); nothing else changes level.
func TestCreate_StartsSandboxedWhereRecommended(t *testing.T) {
	caps := sandboxes{"": "available", "gv": "available", "plain": "unavailable", "bad": "error", "none": "not_installed", "new": "unknown"}
	for _, c := range []struct {
		name string
		in   CreateInput
		want bool
	}{
		{"tool on a server with gVisor", CreateInput{ServerID: "gv", AddonSlug: "phpmyadmin", SourceType: SourceTypeDockerImage}, true},
		{"app on the panel's server", CreateInput{DockerImage: "acme/web", SourceType: SourceTypeDockerImage}, true},
		{"git app on the panel's server", CreateInput{SourceType: SourceTypeGit, GitRepoURL: "https://example.com/a.git"}, true},
		{"tool without gVisor", CreateInput{ServerID: "plain", AddonSlug: "adminer", SourceType: SourceTypeDockerImage}, false},
		{"typed-in tool on a server with gVisor", CreateInput{ServerID: "gv", DockerImage: "dpage/pgadmin4:8", SourceType: SourceTypeDockerImage}, true},
		// No gVisor (D-150): it runs Standard, where it deploys; Settings →
		// Isolation names Sandboxed as recommended and why it isn't there.
		{"typed-in tool without gVisor", CreateInput{ServerID: "none", DockerImage: "dpage/pgadmin4:8", SourceType: SourceTypeDockerImage}, false},
		{"typed-in tool on a server not checked yet", CreateInput{ServerID: "new", DockerImage: "adminer:5", SourceType: SourceTypeDockerImage}, false},
		{"tool when the check fails", CreateInput{ServerID: "bad", AddonSlug: "adminer", SourceType: SourceTypeDockerImage}, false},
		{"app elsewhere", CreateInput{ServerID: "gv", DockerImage: "acme/web", SourceType: SourceTypeDockerImage}, false},
		{"database on the panel's server", CreateInput{AddonSlug: "postgres", DockerImage: "postgres:16", SourceType: SourceTypeDockerImage}, false},
		{"compose stack on the panel's server", CreateInput{SourceType: SourceTypeDockerCompose, ComposeContent: "services: {}"}, false},
	} {
		c.in.Name = "x"
		repo := &isoRepo{}
		m := NewServiceManager(repo, zap.NewNop())
		m.WithSandboxes(caps)
		m.WithTransactor(inTx{})
		m.WithVariables(discardVariables{})
		saved, err := m.Create(context.Background(), "env-1", "", c.in)
		require.NoError(t, err, c.name)
		if c.want {
			assert.Equal(t, []string{"new=sandboxed"}, repo.set, c.name)
			assert.Equal(t, IsolationSandboxed, saved.EffectiveIsolation, c.name)
		} else {
			assert.Empty(t, repo.set, c.name)
		}
	}
}

// A new tool under a strict workspace default keeps following it: Sandboxed
// would pin it below the default (D-115).
func TestCreate_KeepsAStrictDefault(t *testing.T) {
	repo := &isoRepo{def: IsolationSandboxedStrict}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithSandboxes(sandboxes{"gv": "available"})
	m.WithTransactor(inTx{})
	m.WithVariables(discardVariables{})
	_, err := m.Create(context.Background(), "env-1", "", CreateInput{Name: "x", ServerID: "gv", AddonSlug: "pgadmin", SourceType: SourceTypeDockerImage})
	require.NoError(t, err)
	assert.Empty(t, repo.set)
}

// levelRepo answers Isolation with own and def, and keeps what a preview
// copy was written with.
type levelRepo struct {
	fakeRepo
	own, def string
	copied   PreviewCopy
}

func (r *levelRepo) Isolation(context.Context, string) (string, string, error) {
	return r.own, r.def, nil
}
func (r *levelRepo) CopyForPreview(_ context.Context, in PreviewCopy, _ string) (string, error) {
	r.copied = in
	return "copy", nil
}

// Below the workspace's default is the owners' (D-95), in the order
// standard, sandboxed, sandboxed_strict (D-115).
func TestCheckIsolationOrdersTheLevels(t *testing.T) {
	for _, c := range []struct {
		def, level, role string
		refused          bool
	}{
		{IsolationSandboxed, IsolationStandard, "developer", true},
		{IsolationSandboxed, IsolationStandard, "owner", false},
		{IsolationSandboxedStrict, IsolationSandboxed, "developer", true},
		{IsolationSandboxedStrict, IsolationSandboxedStrict, "developer", false},
		{IsolationSandboxedStrict, isolationDefault, "developer", false},
		{IsolationSandboxed, IsolationSandboxedStrict, "developer", false},
		{IsolationStandard, IsolationStandard, "developer", false},
	} {
		m := NewServiceManager(&levelRepo{def: c.def}, zap.NewNop())
		err := m.CheckIsolation(context.Background(), "s", c.level, c.role)
		if c.refused {
			assert.ErrorIs(t, err, ErrIsolationNeedsOwner, "%+v", c)
		} else {
			assert.NoError(t, err, "%+v", c)
		}
	}
}

// An untrusted preview's copy runs with a strict network where its server
// can, or where the original already does; else Sandboxed (D-115).
func TestUntrustedCopyTakesTheStrictNetworkWhereItRuns(t *testing.T) {
	caps := sandboxes{"strict:agent": "available", "strict:ssh": "unavailable"}
	for _, c := range []struct {
		name, server, own, def, want string
	}{
		{"agent server", "agent", "", IsolationStandard, IsolationSandboxedStrict},
		{"SSH server", "ssh", "", IsolationStandard, IsolationSandboxed},
		{"original strict", "ssh", IsolationSandboxedStrict, IsolationStandard, IsolationSandboxedStrict},
		{"workspace default strict", "ssh", "", IsolationSandboxedStrict, IsolationSandboxedStrict},
	} {
		repo := &levelRepo{own: c.own, def: c.def}
		repo.svc = &Service{ID: "orig", ServerID: c.server, SourceType: SourceTypeDockerImage, MemoryLimit: "256m"}
		m := NewServiceManager(repo, zap.NewNop())
		m.WithSandboxes(caps)
		_, err := m.CopyForPreview(context.Background(), PreviewCopy{OriginalID: "orig", Untrusted: true})
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, repo.copied.Isolation, c.name)
	}
}
