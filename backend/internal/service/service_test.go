package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/resources"
)

func (f *fakeRepo) CreateProjectScoped(ctx context.Context, _ string, in CreateInput) (*Service, error) {
	return f.Create(ctx, "", in)
}

func (f *fakeRepo) CreateOrgScoped(ctx context.Context, _ string, in CreateInput) (*Service, error) {
	return f.Create(ctx, "", in)
}

// inTx runs fn as it is, and discardVariables stores nothing: a catalog create's
// generated secrets on a fake repository.
type inTx struct{}

func (inTx) InTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type discardVariables struct{}

func (discardVariables) Seed(context.Context, string, string, []envvar.UpsertInput) error {
	return nil
}
func (discardVariables) CreateSecret(context.Context, string, string, envvar.CreateSecretInput) (*envvar.SecretDTO, error) {
	return &envvar.SecretDTO{}, nil
}
func (discardVariables) InheritEnvironment(context.Context, string, string) error { return nil }
func (discardVariables) ReplaceSecret(context.Context, string, string, string, string) error {
	return nil
}
func (discardVariables) List(context.Context, string) ([]envvar.EnvVar, error) { return nil, nil }

// ─── Default memory limits at create (D-41) ──────────────────────────────────

func TestCreate_DefaultMemoryLimit(t *testing.T) {
	creates := map[string]func(*ServiceManager, CreateInput) (*SavedService, error){
		"environment": func(m *ServiceManager, in CreateInput) (*SavedService, error) {
			return m.Create(context.Background(), "env-1", "", in)
		},
		"project": func(m *ServiceManager, in CreateInput) (*SavedService, error) {
			return m.CreateProjectScoped(context.Background(), "proj-1", "", in)
		},
		"org": func(m *ServiceManager, in CreateInput) (*SavedService, error) {
			return m.CreateOrgScoped(context.Background(), "org-1", "", in)
		},
	}
	for _, tc := range []struct {
		name string
		in   CreateInput
		want string
	}{
		{"postgres template", CreateInput{AddonSlug: "postgres", DockerImage: "postgres:16"}, "1g"},
		{"nginx image", CreateInput{DockerImage: "nginx"}, "512m"},
		{"admin tool typed in", CreateInput{DockerImage: "dpage/pgadmin4:8"}, "512m"},
		{"admin tool from the catalog", CreateInput{AddonSlug: "adminer", DockerImage: "adminer"}, "64m"},
		{"explicit limit kept", CreateInput{DockerImage: "postgres:16", MemoryLimit: "256m"}, "256m"},
	} {
		for scope, create := range creates {
			t.Run(tc.name+"/"+scope, func(t *testing.T) {
				repo := &fakeRepo{}
				in := tc.in
				in.Name, in.Type, in.SourceType = "web", TypeWeb, SourceTypeDockerImage
				m := NewServiceManager(repo, zap.NewNop())
				m.WithTransactor(inTx{})
				m.WithVariables(discardVariables{})
				_, err := create(m, in)
				require.NoError(t, err)
				assert.Equal(t, tc.want, repo.createdIn.MemoryLimit)
			})
		}
	}
}

// ─── The memory rule on config saves (D-41) ──────────────────────────────────

// limitsRepo is the resources data of one service.
type limitsRepo struct {
	resources.Repository
	grace *time.Time
	panel map[string]resources.ComposeLimit
}

func (r *limitsRepo) GraceUntil(context.Context, string) (*time.Time, error) { return r.grace, nil }
func (r *limitsRepo) ClaimGrace(context.Context, time.Time) (bool, int64, error) {
	return false, 0, nil // an earlier start made the claim
}
func (r *limitsRepo) UsageDaily(context.Context, string, time.Time) (map[string][]resources.DailyUsage, error) {
	return nil, nil
}
func (r *limitsRepo) ComposeLimits(context.Context, string) (map[string]resources.ComposeLimit, error) {
	return r.panel, nil
}
func (r *limitsRepo) AddDefaultComposeLimits(_ context.Context, _ string, defaults map[string]int64) error {
	for name, mb := range defaults {
		r.panel[name] = resources.ComposeLimit{MemoryMB: mb}
	}
	return nil
}

// exceptionRepo holds one service's deploy policy exception.
type exceptionRepo struct {
	deploypolicy.Repository
	exc *deploypolicy.Exception
}

func (r exceptionRepo) GetException(context.Context, string) (*deploypolicy.Exception, error) {
	if r.exc == nil {
		return nil, deploypolicy.ErrNotFound
	}
	return r.exc, nil
}

func TestUpdateConfig_ClearingTheMemoryLimit(t *testing.T) {
	passed := time.Now().Add(-24 * time.Hour)
	for name, exc := range map[string]*deploypolicy.Exception{
		"refused":          nil,
		"allowed by grant": {AllowedRules: []deploypolicy.Rule{composespec.RuleMemoryLimit}},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRepo{svc: &Service{ID: "s1", Name: "web", SourceType: SourceTypeDockerImage, DockerImage: "nginx", MemoryLimit: "512m"}}
			m := NewServiceManager(repo, zap.NewNop())
			m.WithPolicyChecker(deploypolicy.NewService(exceptionRepo{exc: exc}, deploypolicy.ModeEnforce, nil))
			m.WithResources(resources.NewService(&limitsRepo{grace: &passed}, zap.NewNop()))

			_, err := m.UpdateConfig(context.Background(), "s1", UpdateConfigInput{DockerImage: "nginx", MemoryLimit: ""})
			if exc != nil {
				require.NoError(t, err)
				assert.Equal(t, 1, repo.updated)
				return
			}
			var pv *composespec.ViolationsError
			require.True(t, errors.As(err, &pv), "got %v", err)
			require.Len(t, pv.Violations, 1)
			assert.Equal(t, composespec.RuleMemoryLimit, pv.Violations[0].Rule)
			assert.Contains(t, pv.Violations[0].Message, "set it to 512m", "the refusal offers the kind's default without usage data")
			assert.Equal(t, 0, repo.updated)
		})
	}
}

// A new stack (no grace date) that gains a compose service without a limit
// is saved: the new service gets its kind's default first.
func TestUpdateConfig_NewComposeServiceGetsADefault(t *testing.T) {
	repo := &fakeRepo{svc: &Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose}}
	limits := &limitsRepo{panel: map[string]resources.ComposeLimit{"web": {MemoryMB: 256}}}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithPolicyChecker(deploypolicy.NewService(exceptionRepo{}, deploypolicy.ModeEnforce, nil))
	res := resources.NewService(limits, zap.NewNop())
	require.NoError(t, res.EnsureGrace(context.Background()))
	m.WithResources(res)

	_, err := m.UpdateConfig(context.Background(), "s1", UpdateConfigInput{
		ComposeContent: "services:\n  web:\n    image: nginx\n  db:\n    image: postgres:16\n",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]resources.ComposeLimit{"web": {MemoryMB: 256}, "db": {MemoryMB: 1024}}, limits.panel)
	assert.Equal(t, 1, repo.updated)
}

// The memory rule sees a compose service's panel limits, never its empty
// single-container limit.
func TestUpdateConfig_ComposeMemoryCheckFromThePanel(t *testing.T) {
	repo := &fakeRepo{svc: &Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose}}
	policy := &fakePolicy{}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithPolicyChecker(policy)
	m.WithResources(resources.NewService(&limitsRepo{panel: map[string]resources.ComposeLimit{"web": {MemoryMB: 256}}}, zap.NewNop()))

	_, err := m.UpdateConfig(context.Background(), "s1", UpdateConfigInput{ComposeRepoURL: "https://example.com/infra.git"})
	require.NoError(t, err)
	require.Len(t, policy.seen, 1)
	mem := policy.seen[0].Memory
	require.NotNil(t, mem)
	assert.True(t, mem.Compose)
	assert.Empty(t, mem.SingleLimit)
	assert.Equal(t, map[string]resources.ComposeLimitSource{"web": {PanelMB: 256}}, mem.ComposeServices)
}

// Only an inline compose file is carried to resources: a repository's needs
// a clone.
func TestResourceRef(t *testing.T) {
	inline := resourceRef(&Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose, ComposeContent: "services: {}"})
	require.NotNil(t, inline.Compose)
	assert.Equal(t, "services: {}", inline.Compose.Content)
	assert.Equal(t, "stack", inline.Name)

	repo := resourceRef(&Service{ID: "s2", SourceType: SourceTypeDockerCompose, ComposeRepoURL: "https://git.example/infra", ComposeContent: "stale"})
	assert.Nil(t, repo.Compose)

	single := resourceRef(&Service{ID: "s3", SourceType: SourceTypeDockerImage, DockerImage: "nginx", MemoryLimit: "512m", CPULimit: "0.5"})
	assert.Nil(t, single.Compose)
	assert.Equal(t, resources.ServiceRef{ID: "s3", SourceType: "docker-image", DockerImage: "nginx", MemoryLimit: "512m", CPULimit: "0.5"}, single)
}

// D-150: an address by default for a web app with a port; databases, caches,
// admin tools (by catalog or by image), catalog services that aren't apps and
// workers get none.
func TestWantsAddress(t *testing.T) {
	for _, c := range []struct {
		typ, slug, image string
		port             int
		want             bool
	}{
		{"web", "", "nginx:1.27", 80, true},
		{"web", "", "", 3000, true}, // a git build
		{"web", "uptime-kuma", "", 3001, true},
		{"web", "", "nginx:1.27", 0, false},
		{"worker", "", "nginx:1.27", 80, false},
		{"web", "postgres", "postgres:16", 5432, false},
		{"web", "", "redis:7", 6379, false},
		{"web", "pgadmin", "dpage/pgadmin4", 80, false},
		{"web", "", "dpage/pgadmin4", 80, false},
		{"web", "", "mailhog/mailhog", 8025, false},
		{"web", "mailpit", "axllent/mailpit:v1.33.0", 8025, false},
		{"web", "", "axllent/mailpit", 8025, false},
		{"web", "loki", "grafana/loki:3.0.0", 3100, false},
	} {
		assert.Equal(t, c.want, WantsAddress(c.typ, c.slug, c.image, c.port), "%+v", c)
	}
}

type fakeAddresser struct {
	host string
	err  error
}

func (f fakeAddresser) Address(context.Context, string, string, string) (string, error) {
	return f.host, f.err
}
func (f fakeAddresser) ServerAddress(context.Context, string) (string, error) { return "", nil }
func (f fakeAddresser) AddressIn(context.Context, string, string, string, string) (string, error) {
	return f.host, f.err
}

func TestAddressFor(t *testing.T) {
	ctx := context.Background()
	web := AddressQuery{Name: "web", Type: "web", DockerImage: "nginx:1", Port: 80}
	s := &ServiceManager{}
	got, err := s.AddressFor(ctx, "env", AddressQuery{Name: "db", Type: "web", AddonSlug: "postgres", Port: 5432})
	require.NoError(t, err)
	assert.Equal(t, Address{Reason: "not_web"}, got)

	s.WithAddresser(fakeAddresser{err: ErrNoPublicAddress})
	got, err = s.AddressFor(ctx, "env", web)
	require.NoError(t, err)
	assert.Equal(t, Address{Reason: "no_public_address"}, got)

	s.WithAddresser(fakeAddresser{host: "web-production-shop.203-0-113-10.sslip.io"})
	got, err = s.AddressFor(ctx, "env", web)
	require.NoError(t, err)
	assert.Equal(t, Address{Hostname: "web-production-shop.203-0-113-10.sslip.io", Temporary: true}, got)

	s.WithAddresser(fakeAddresser{host: "web-production.preview.example.com"})
	got, err = s.AddressFor(ctx, "env", web)
	require.NoError(t, err)
	assert.False(t, got.Temporary, "the project's own wildcard is a normal domain")

	s.WithAddresser(fakeAddresser{err: errors.New("dns down")})
	_, err = s.AddressFor(ctx, "env", web)
	require.Error(t, err)
}

// A service shows what it is by its catalog entry or, for one typed in by
// its image, the image's name: the founders' staging mysql, redis,
// phpMyAdmin and MailHog were all shown as Web.
func TestPresentedKind(t *testing.T) {
	cases := map[string]struct {
		slug, image string
		want        resources.Kind
	}{
		"catalog postgres":   {"postgres", "postgres:16", resources.KindDatabase},
		"mysql image":        {"", "mysql:8.0", resources.KindDatabase},
		"mariadb by digest":  {"", "docker.io/library/mariadb@sha256:abc", resources.KindDatabase},
		"redis image":        {"", "redis:7-alpine", resources.KindCache},
		"phpmyadmin image":   {"", "phpmyadmin/phpmyadmin:5", resources.KindTool},
		"mailhog image":      {"", "mailhog/mailhog:latest", resources.KindTool},
		"catalog mailhog":    {"mailhog", "mailhog/mailhog", resources.KindTool},
		"catalog mailpit":    {"mailpit", "axllent/mailpit:v1.33.0@sha256:abc", resources.KindTool},
		"mailpit image":      {"", "axllent/mailpit", resources.KindTool},
		"redisinsight image": {"", "redislabs/redisinsight:1.14.0", resources.KindTool},
		"nginx is an app":    {"", "nginx:alpine", resources.KindApp},
		"git build":          {"", "", resources.KindApp},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, Service{AddonSlug: tc.slug, DockerImage: tc.image}.presented().Kind)
		})
	}
}

// ─── The panel host under Only Toft (D-62) ───────────────────────────────────

type panelRole bool

func (r panelRole) PanelHostRunsApps(context.Context) bool { return bool(r) }

// noProjects fails the test if a project is created.
type noProjects struct{ t *testing.T }

func (p noProjects) CreateProjectWith(context.Context, string, string, project.CreateProjectInput,
	func(context.Context, string, string) error) (*project.Project, error) {
	p.t.Error("a project was created")
	return nil, errors.New("unexpected")
}

type noVariables struct{ VariableWriter }

// A service is never created on the panel host while its role is Only Toft:
// its first deploy would be refused (ErrPanelHostNoApps). Another server is fine.
func TestCreate_PanelHostUnderOnlyToft(t *testing.T) {
	creates := map[string]func(*ServiceManager, CreateInput) error{
		"environment": func(m *ServiceManager, in CreateInput) error {
			_, err := m.Create(context.Background(), "env-1", "", in)
			return err
		},
		"project": func(m *ServiceManager, in CreateInput) error {
			_, err := m.CreateProjectScoped(context.Background(), "proj-1", "", in)
			return err
		},
		"org": func(m *ServiceManager, in CreateInput) error {
			_, err := m.CreateOrgScoped(context.Background(), "org-1", "", in)
			return err
		},
		"first app": func(m *ServiceManager, in CreateInput) error {
			_, _, err := m.CreateWithProject(context.Background(), "org-1", "u-1", project.CreateProjectInput{Name: "web"}, in)
			return err
		},
	}
	for scope, create := range creates {
		t.Run(scope, func(t *testing.T) {
			repo := &fakeRepo{}
			m := NewServiceManager(repo, zap.NewNop())
			m.WithPanelRole(panelRole(false))
			m.WithProjects(noProjects{t})
			m.WithVariables(noVariables{})
			in := CreateInput{Name: "web", Type: TypeWeb, SourceType: SourceTypeDockerImage, DockerImage: "nginx"}
			require.ErrorIs(t, create(m, in), ErrPanelHostNoApps)
			assert.Zero(t, repo.created)

			if scope != "first app" {
				in.ServerID = "srv-1"
				require.NoError(t, create(m, in))
				m.WithPanelRole(panelRole(true))
				in.ServerID = ""
				require.NoError(t, create(m, in))
			}
		})
	}
}
