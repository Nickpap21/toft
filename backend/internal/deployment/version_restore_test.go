package deployment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// versionService is an image service on the control-plane server whose
// image writes a test reads back.
type versionService struct {
	guestService
	saved []string // UpdateDockerImage's and PutBackDockerImage's, in order
}

func (v *versionService) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, SourceType: "docker-image", DockerImage: "nginx:1.27", MemoryLimit: "512m"}, nil
}
func (v *versionService) UpdateDockerImage(_ context.Context, _, image string) (bool, error) {
	v.saved = append(v.saved, image)
	return false, nil
}
func (v *versionService) PutBackDockerImage(_ context.Context, _, from, image, _ string) error {
	v.saved = append(v.saved, from+" → "+image)
	return nil
}

type updatingHost struct{}

func (updatingHost) HostUpdating(context.Context, string) (bool, error) { return true, nil }

type noDomains struct{ domainPkg.Repository }

func (noDomains) ListByService(context.Context, string) ([]domainPkg.Domain, error) { return nil, nil }

type noVars struct{}

func (noVars) ListAllForDeployment(context.Context, string) ([]string, error) { return nil, nil }

// stackService is a compose stack whose own IMAGE_TAG is 1.27; Swap
// records its changes.
type stackService struct {
	guestService
	swaps []string
}

func (s *stackService) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, SourceType: service.SourceTypeDockerCompose, ComposeContent: "services:\n  web:\n    image: nginx:${IMAGE_TAG}\n"}, nil
}
func (s *stackService) ListAllForDeployment(context.Context, string) ([]string, error) {
	return []string{"IMAGE_TAG=1.27"}, nil
}
func (s *stackService) Set(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}
func (s *stackService) Unset(context.Context, string, string, string) error { return nil }
func (s *stackService) Swap(_ context.Context, _, key, from, value string) error {
	s.swaps = append(s.swaps, key+": "+from+" → "+value)
	return nil
}

// oneDeployment is a Repository with one deployment of svc-1, which ran IMAGE_TAG 1.26.
type oneDeployment struct{ Repository }

func (oneDeployment) GetByID(_ context.Context, id string) (*Deployment, error) {
	return &Deployment{ID: id, ServiceID: "svc-1", Status: StatusRunning, ImageTag: "1.26"}, nil
}
func (oneDeployment) ListPulls(context.Context, []string) (map[string]PullRef, error) {
	return map[string]PullRef{}, nil
}

// A compose rollback makes the stack's own IMAGE_TAG the rolled-back one,
// and puts it back when no deploy starts.
func TestComposeRollbackSavesItsImageTag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(oneDeployment{}, nil, nil, zap.NewNop())
	s.WithHostUpdates(updatingHost{})
	stack := &stackService{}
	s.WithDeployUseCase(stack, nil, domainPkg.NewService(noDomains{}, nil, zap.NewNop()), stack, stack, nil, &auditLog{})
	h := NewHandler(s, stack, zap.NewNop())
	r := ownerRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments/dep-1/rollback", nil))
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, []string{"IMAGE_TAG: 1.27 → 1.26", "IMAGE_TAG: 1.26 → 1.27"}, stack.swaps)
}

func ownerRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob")
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Next()
	})
	h.RegisterRoutes(g)
	return r
}

// Deploy a version saves the tag before the deploy; when no deploy starts
// (refused before a deployment is recorded), the previous image is put back.
func TestDeployAVersionNotStartedPutsThePreviousImageBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithHostUpdates(updatingHost{})
	sm := &versionService{}
	s.WithDeployUseCase(sm, nil, domainPkg.NewService(noDomains{}, nil, zap.NewNop()), noVars{}, nil, nil, &auditLog{})
	h := NewHandler(s, sm, zap.NewNop())
	r := ownerRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(`{"image_tag":"nginx:1.28"}`)))
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, []string{"nginx:1.28", "nginx:1.28 → nginx:1.27"}, sm.saved)
}

// ownedService is the real ServiceManager; the caller's workspace owns
// every service.
type ownedService struct{ *service.ServiceManager }

func (ownedService) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }

// refusedDeploy posts a deploy of serviceID that Trigger refuses (the host
// is updating) and returns the response code.
func refusedDeploy(t *testing.T, mgr *service.ServiceManager, vars *envvar.Service, serviceID, body string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithHostUpdates(updatingHost{})
	s.WithDeployUseCase(ownedService{mgr}, nil, domainPkg.NewService(noDomains{}, nil, zap.NewNop()), vars, vars, nil, &auditLog{})
	h := NewHandler(s, ownedService{mgr}, zap.NewNop())
	w := httptest.NewRecorder()
	ownerRouter(h).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/"+serviceID+"/deployments", strings.NewReader(body)))
	return w.Code
}

// Deploy a version of another registry's image clears the saved registry
// password; when no deploy starts, the image goes back with that password.
func TestDeployAVersionNotStartedPutsTheRegistryPasswordBack(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name, source_type, docker_image, port, registry_server, registry_username)
		VALUES ('org', $1, 'api', 'docker-image', 'registry.a.example/api:1', 80, 'registry.a.example', 'ua') RETURNING id::text`, orgID).Scan(&serviceID))
	sealed, err := keys.Seal(crypto.AD("services.registry_password", serviceID), "pa")
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE services SET registry_password = $2 WHERE id = $1`, serviceID, sealed)
	require.NoError(t, err)
	mgr := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	vars := envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop())

	assert.Equal(t, http.StatusConflict, refusedDeploy(t, mgr, vars, serviceID, `{"image_tag":"registry.b.example/api:2"}`))
	saved, err := mgr.Get(ctx, serviceID)
	require.NoError(t, err)
	assert.Equal(t, []string{"registry.a.example/api:1", "registry.a.example", "ua", "pa"},
		[]string{saved.DockerImage, saved.RegistryServer, saved.RegistryUsername, saved.RegistryPassword})
}

// Deploy a version on a compose stack saves IMAGE_TAG; when no deploy
// starts, the stack's own value comes back, and one it didn't have is
// removed (an environment's IMAGE_TAG stays the one it reads).
func TestComposeDeployAVersionNotStartedPutsTheImageTagBack(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name, source_type, compose_content)
		VALUES ('org', $1, 'stack', 'docker-compose', $2) RETURNING id::text`, orgID, "services:\n  web:\n    image: nginx:${IMAGE_TAG}\n").Scan(&serviceID))
	mgr := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	vars := envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop())
	own := func() []string {
		list, err := vars.List(ctx, serviceID)
		require.NoError(t, err)
		tags := []string{}
		for _, v := range list {
			if v.Key == "IMAGE_TAG" {
				tags = append(tags, v.Value)
			}
		}
		return tags
	}

	_, err := vars.Upsert(ctx, serviceID, "", envvar.UpsertInput{Key: "IMAGE_TAG", Value: "1.27"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, refusedDeploy(t, mgr, vars, serviceID, `{"image_tag":"1.28"}`))
	assert.Equal(t, []string{"1.27"}, own(), "the stack's own IMAGE_TAG comes back")

	require.NoError(t, vars.Delete(ctx, serviceID, "IMAGE_TAG"))
	assert.Equal(t, http.StatusConflict, refusedDeploy(t, mgr, vars, serviceID, `{"image_tag":"1.28"}`))
	assert.Empty(t, own(), "no service-level IMAGE_TAG stays")
}

// liveService is an image service whose live deployment is dep-1.
type liveService struct{ guestService }

func (liveService) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, SourceType: "docker-image", DockerImage: "nginx:1.27", ActiveDeploymentID: "dep-1"}, nil
}

// recordedDeployment is oneDeployment that keeps what Create was given.
type recordedDeployment struct {
	oneDeployment
	triggeredBy []string
}

func (r *recordedDeployment) Create(_ context.Context, _, triggeredBy, _ string, _ int64) (*Deployment, error) {
	r.triggeredBy = append(r.triggeredBy, triggeredBy)
	return nil, ErrAlreadyDeployed
}

// A rollback to the live deployment is a redeploy (7g step 8), and says so;
// one to another deployment stays a rollback.
func TestRollbackToTheLiveDeploymentIsARedeploy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &recordedDeployment{}
	s := NewService(repo, nil, nil, zap.NewNop())
	s.WithDeployUseCase(liveService{}, nil, domainPkg.NewService(noDomains{}, nil, zap.NewNop()), noVars{}, nil, nil, &auditLog{})
	h := NewHandler(s, liveService{}, zap.NewNop())
	r := ownerRouter(h)

	for _, id := range []string{"dep-1", "dep-0"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments/"+id+"/rollback", nil))
		require.NotEqual(t, http.StatusAccepted, w.Code, w.Body.String())
	}
	assert.Equal(t, []string{"Redeploy", "Rollback"}, repo.triggeredBy)
}

// names answers bob's name.
type names struct{}

func (names) UserName(_ context.Context, id string) (string, error) {
	if id == "bob" {
		return "Bob", nil
	}
	return "", nil
}

// An agent's rollback or redeploy is recorded as the agent acting for the
// person (D-100), not as the person.
func TestAgentRollbackIsRecordedAsTheAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &recordedDeployment{}
	s := NewService(repo, nil, nil, zap.NewNop())
	s.WithDeployUseCase(liveService{}, nil, domainPkg.NewService(noDomains{}, nil, zap.NewNop()), noVars{}, nil, nil, &auditLog{})
	h := NewHandler(s, liveService{}, zap.NewNop())
	s.WithUserNames(names{})
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob")
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Request = c.Request.WithContext(actor.WithAgent(c.Request.Context(), actor.Agent{ConnectionID: "conn-1", Name: "Claude Code", UserID: "bob", OrgID: "guest"}))
		c.Next()
	})
	h.RegisterRoutes(g)

	for _, id := range []string{"dep-1", "dep-0"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments/"+id+"/rollback", nil))
		require.NotEqual(t, http.StatusAccepted, w.Code, w.Body.String())
	}
	assert.Equal(t, []string{"Redeploy · Agent · Claude Code for Bob", "Rollback · Agent · Claude Code for Bob"}, repo.triggeredBy)
}
