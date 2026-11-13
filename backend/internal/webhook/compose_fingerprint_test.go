package webhook

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	envPkg "github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// stackRow is the services row of one stack: what a deploy writes to it
// (its status, the compose service it routed) is there for the next read.
type stackRow struct {
	mu  sync.Mutex
	svc service.Service
}

func (r *stackRow) Get(context.Context, string) (*service.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.svc
	return &s, nil
}
func (*stackRow) FindByProjectRepoAndBranch(context.Context, string, string, string, string) ([]service.Service, error) {
	return nil, nil
}
func (r *stackRow) UpdateRuntimeStatus(_ context.Context, _, status, active string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.svc.RuntimeStatus, r.svc.ActiveDeploymentID = status, active
	return nil
}
func (r *stackRow) SetComposeNetworkService(_ context.Context, _, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.svc.ComposeNetworkService = name
	return nil
}
func (*stackRow) SwapDockerImage(context.Context, string, string, string) error { return nil }

// stackDeployments is deployments for a compose deploy, which the panel
// finishes itself.
type stackDeployments struct{ *deployments }

func (stackDeployments) ClearHandOff(context.Context, string) error { return nil }

// composeUp is a Docker that brings any stack up.
type composeUp struct{ builds }

func (composeUp) RunCompose(context.Context, string, string, string, string, map[string]string, *deployment.ComposeRoute, deployment.ComposeNetworks, map[string]deployment.ComposeLimit, *deployment.RegistryAuth, rollout.Hooks) error {
	return nil
}

type onlyCompose struct{}

func (onlyCompose) ForServer(*serverPkg.Server) (deployment.Orchestrator, func(), error) {
	return composeUp{}, func() {}, nil
}

type generatedSecrets struct{}

func (generatedSecrets) ListAllForDeployment(context.Context, string) ([]string, error) {
	return []string{"APP_SECRET=s3cr3t", "POSTGRES_PASSWORD=pw", "TWO_FACTOR_ENCRYPTION_KEY=0f0f"}, nil
}

// A catalog stack created and deployed on a temporary name records the
// fingerprint of what it shipped, and the compose service its deploy
// routed isn't a setting saved since: once the name's certificate is out,
// Toft redeploys it for HTTPS on its own (D-159).
func TestComposeCatalogDeployRedeploysForCertificate(t *testing.T) {
	tpl, ok := catalog.Get("umami")
	require.True(t, ok)
	temp := "umami-production-shop.203-0-113-10.sslip.io"
	row := &stackRow{svc: service.Service{ID: "s1", Name: "umami", ProjectID: projectA, EnvironmentID: "e1",
		SourceType: service.SourceTypeDockerCompose, ComposeContent: tpl.ComposeContent, ComposeRouteService: tpl.RouteService,
		Port: tpl.DefaultPort, AddonSlug: tpl.Slug, RuntimeStatus: "stopped"}}

	repo := &deployments{d: deployment.Deployment{ID: "dep-1"}}
	deps := deployment.NewService(stackDeployments{repo}, onlyCompose{}, row, zap.NewNop())
	deps.WithServiceReader(row)
	deps.WithPreviews(nil, nil, copies(false))
	deps.WithHashKey([]byte("instance-hmac-key"))
	domains := &certDomains{domains: []domainPkg.Domain{{Hostname: temp}}}
	h := &Handler{serviceSvc: row, deploymentSvc: deps, projects: fakeProjects{}, envVarProvider: generatedSecrets{}, log: zap.NewNop(),
		envSvc:    envPkg.NewService(oneEnvironment{env: envPkg.Environment{ID: "e1", Name: "production"}}),
		domainSvc: domainPkg.NewService(domains, nil, zap.NewNop())}

	// Create and deploy: the service as created, its generated secrets.
	ctx := context.Background()
	svc, _ := row.Get(ctx, "s1")
	input := h.inputFor(ctx, svc, "", nil, "production")
	input.EnvVars, _ = generatedSecrets{}.ListAllForDeployment(ctx, "s1")
	_, err := deps.Trigger(ctx, input)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		s, _ := row.Get(ctx, "s1")
		d, _ := repo.GetByID(ctx, "dep-1")
		return d.SettingsFingerprint != "" && s.ComposeNetworkService == "umami" && s.RuntimeStatus == "running"
	}, 2*time.Second, 10*time.Millisecond, "the stack went live, recorded its settings and the compose service it routed")

	certified := time.Now()
	domains.domains[0].CertificateAt = &certified
	in, skip := h.certificateRedeploy(ctx, "s1")
	require.NotNil(t, in, skip)
	require.Equal(t, deployment.OriginSystem, in.Origin)
}
