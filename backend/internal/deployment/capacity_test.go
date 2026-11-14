package deployment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
)

// fullShare: the workspace "guest" has a 1.5 GiB share of "owner-ws"'s
// server, all of it allocated; "owner-ws" has the rest of an 8 GiB server,
// also all allocated.
type fullShare struct{}

func (fullShare) GetCapacitySummaryFor(_ context.Context, serverID, orgID string) (*server.CapacitySummary, error) {
	if orgID == "guest" {
		return &server.CapacitySummary{ServerID: serverID, ServerName: "qa-node", OwnerOrgID: "owner-ws", Share: true, CapacityDiscovered: true,
			EffectiveCPUCores: 1, EffectiveMemoryMB: 1536, AllocatedMemoryMB: 1536}, nil
	}
	return &server.CapacitySummary{ServerID: serverID, OwnerOrgID: "owner-ws", CapacityDiscovered: true,
		EffectiveCPUCores: 4, EffectiveMemoryMB: 6656, AllocatedMemoryMB: 6656}, nil
}

// orgs: bypass is for owners in every workspace; "alice" owns owner-ws,
// "bob" is only a developer there.
type orgs struct{}

func (orgs) GetSettings(context.Context, string) (*orgPkg.OrgSettings, error) {
	return &orgPkg.OrgSettings{ResourceBypassMinRole: orgPkg.RoleOwner}, nil
}

func (orgs) RoleIn(_ context.Context, userID, orgID string) (string, error) {
	if orgID != "owner-ws" {
		return "", orgPkg.ErrNotFound
	}
	return map[string]string{"alice": orgPkg.RoleOwner, "bob": orgPkg.RoleDeveloper}[userID], nil
}

// On its own server a workspace's bypass setting decides; on a share of
// another workspace's server only an owner of that workspace may deploy past
// the share, whatever the deploying workspace's own role.
func TestCheckCapacity_WhoMayBypass(t *testing.T) {
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithCapacitySvc(fullShare{})
	s.WithOrgSettingsSvc(orgs{})
	check := func(userID, role, orgID string) bool {
		t.Helper()
		capErr, err := s.checkCapacity(context.Background(), "srv-1", "svc-1", userID, role, orgID, 0, 512)
		require.NoError(t, err)
		require.NotNil(t, capErr, "512 MiB more does not fit")
		return capErr.CanBypass
	}

	assert.True(t, check("carol", orgPkg.RoleOwner, "owner-ws"), "own server: an owner, per the workspace's setting")
	assert.False(t, check("carol", orgPkg.RoleDeveloper, "owner-ws"), "own server: a developer, below the setting")

	assert.True(t, check("alice", orgPkg.RoleOwner, "guest"), "share: an owner of the server's workspace")
	assert.False(t, check("bob", orgPkg.RoleOwner, "guest"), "share: owner of the guest workspace, developer of the server's")
	assert.False(t, check("dave", orgPkg.RoleOwner, "guest"), "share: not a member of the server's workspace")
}

// quarterLeft: 256 MiB of a 1 GiB server left; a stopped service isn't in
// the 768 MiB allocated.
type quarterLeft struct{}

func (quarterLeft) GetCapacitySummaryFor(_ context.Context, serverID, _ string) (*server.CapacitySummary, error) {
	return &server.CapacitySummary{ServerID: serverID, CapacityDiscovered: true,
		EffectiveCPUCores: 2, EffectiveMemoryMB: 1024, AllocatedMemoryMB: 768}, nil
}

type stoppedService struct{}

func (stoppedService) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ServerID: "srv-1", SourceType: "docker-image", MemoryLimit: "512m", RuntimeStatus: "stopped"}, nil
}

// A stopped service holds nothing of the allocated sum, so deploying it back
// gets only what is left: its own limits aren't subtracted again.
func TestCheckCapacity_StoppedServiceGetsNoRoomBack(t *testing.T) {
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithCapacitySvc(quarterLeft{})
	s.WithOrgSettingsSvc(orgs{})
	s.WithServiceReader(stoppedService{})
	capErr, err := s.checkCapacity(context.Background(), "srv-1", "svc-1", "carol", orgPkg.RoleOwner, "owner-ws", 0, 512)
	require.NoError(t, err)
	require.NotNil(t, capErr, "512 MiB doesn't fit in the 256 MiB left")
	assert.Equal(t, 768.0, capErr.AllocatedMemoryMB)
}

// guestService is a 512 MiB service of the guest workspace, on the shared server.
type guestService struct{}

func (guestService) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (guestService) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, ServerID: "srv-1", SourceType: "docker-image", MemoryLimit: "512m"}, nil
}
func (guestService) CheckDependencies(context.Context, *service.Service) error { return nil }
func (guestService) UpdateDockerImage(context.Context, string, string) (bool, error) {
	return false, nil
}
func (guestService) PutBackDockerImage(context.Context, string, string, string, string) error {
	return nil
}
func (guestService) UpdateGitBranch(context.Context, string, string) error { return nil }
func (guestService) CheckMove(context.Context, string) error               { return nil }

type auditLog struct{ actions []string }

func (a *auditLog) WriteAuditLog(_ context.Context, _, _, action, _ string) {
	a.actions = append(a.actions, action)
}

// ?force=true is not a bypass by itself: a caller the gate does not allow to
// bypass gets the 409 again, nothing is deployed and nothing audited.
func TestTrigger_ForceNeedsTheRightToBypass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithCapacitySvc(fullShare{})
	s.WithOrgSettingsSvc(orgs{})
	audit := &auditLog{}
	s.WithDeployUseCase(guestService{}, nil, nil, nil, nil, nil, audit)
	h := NewHandler(s, guestService{}, zap.NewNop())

	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob") // owner of guest, only a developer of the server's workspace
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Next()
	})
	h.RegisterRoutes(g)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments?force=true", strings.NewReader(`{}`)))
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"can_bypass":false`)
	assert.Empty(t, audit.actions, "no deploy, no bypass")
}

// A capacity refusal is a problem (RFC 9457) whose detail names the server
// and what is short, with the figures in data, so a client without the
// capacity dialog (a toast, the CLI) has something to say.
func TestTrigger_CapacityRefusalIsAProblem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithCapacitySvc(fullShare{})
	s.WithOrgSettingsSvc(orgs{})
	s.WithDeployUseCase(guestService{}, nil, nil, nil, nil, nil, &auditLog{})
	h := NewHandler(s, guestService{}, zap.NewNop())
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "guest")
		c.Set("userID", "bob")
		c.Set("memberRole", orgPkg.RoleOwner)
		c.Next()
	})
	h.RegisterRoutes(g)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	const msg = "Not enough room on qa-node: needs 512 MiB of memory, 0 MiB left of 1.5 GiB."
	assert.Equal(t, msg, body["error"])
	assert.Equal(t, msg, body["detail"])
	assert.Equal(t, "Conflict", body["title"])
	assert.Equal(t, "capacity_exceeded", body["data"].(map[string]any)["code"])
	apispec.Check(t, http.MethodPost, "/api/v1/services/svc-1/deployments", w.Code, body)
}

// moveRefusals is guestService whose Get and CheckMove a test sets.
type moveRefusals struct {
	guestService
	deploying bool
	moveErr   error
}

func (m moveRefusals) Get(ctx context.Context, id string) (*service.Service, error) {
	s, _ := m.guestService.Get(ctx, id)
	s.Deploying = m.deploying
	return s, nil
}
func (m moveRefusals) CheckMove(context.Context, string) error { return m.moveErr }

// A deploy that moves the service (server_id) is refused while another
// deploy runs, and for a service that mounts a shared volume; nothing is
// deployed or audited.
func TestTrigger_MoveRefusals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, sm := range map[string]moveRefusals{
		"a deploy is in progress": {deploying: true},
		"a shared volume":         {moveErr: service.ErrMountsSharedVolume},
	} {
		t.Run(name, func(t *testing.T) {
			audit := &auditLog{}
			s := NewService(nil, nil, nil, zap.NewNop())
			s.WithDeployUseCase(sm, nil, nil, nil, nil, nil, audit)
			h := NewHandler(s, sm, zap.NewNop())
			r := gin.New()
			g := r.Group("/api/v1")
			g.Use(func(c *gin.Context) {
				c.Set("orgID", "guest")
				c.Set("userID", "bob")
				c.Set("memberRole", orgPkg.RoleOwner)
				c.Next()
			})
			h.RegisterRoutes(g)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(`{"server_id":"srv-2"}`)))
			assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			assert.Empty(t, audit.actions)
		})
	}
}

type isOperator map[string]bool

func (o isOperator) IsOperator(_ context.Context, userID string) (bool, error) {
	return o[userID], nil
}

// panelServices are services on the panel's server, each with a 512 MiB
// limit: "worker" went live with it; "raised" went live with 256 MiB and
// had its limit raised since; "old" went live before deploys recorded what
// they reserve; "new" was never deployed; "stopped" is stopped.
type panelServices struct{}

func (panelServices) Get(_ context.Context, id string) (*service.Service, error) {
	svc := &service.Service{ID: id, SourceType: "docker-image", MemoryLimit: "512m", RuntimeStatus: "running"}
	switch id {
	case "worker", "raised", "old":
		svc.ActiveDeploymentID = "live-" + id
	case "new":
		svc.RuntimeStatus = "idle"
	case "stopped":
		svc.RuntimeStatus = "stopped"
	}
	return svc, nil
}

// liveDeployments is what the services' live deployments reserved.
type liveDeployments struct{ Repository }

func (liveDeployments) GetByID(_ context.Context, id string) (*Deployment, error) {
	reserved := map[string]float64{"live-worker": 512, "live-raised": 256}
	d := &Deployment{ID: id}
	if mb, ok := reserved[id]; ok {
		d.ReservedMemoryMB = &mb
	}
	return d, nil
}

// The panel's server has a ledger too: what it leaves its apps (D-62)
// against what every workspace's services reserve there. Only an instance
// operator deploys past it, since it is every workspace's server.
func TestCheckCapacity_ThePanelsServer(t *testing.T) {
	s := NewService(liveDeployments{}, nil, nil, zap.NewNop())
	s.WithServiceReader(panelServices{})
	s.WithControlPlaneStopgap(false, isOperator{"olga": true})
	check := func(apps, alloc float64, serviceID, userID string, memMB float64) *server.CapacityExceededError {
		t.Helper()
		s.WithPreviews(nil, panelLedger{apps: apps, alloc: alloc}, nil)
		capErr, err := s.checkCapacity(context.Background(), "", serviceID, userID, orgPkg.RoleOwner, "ws", 0, memMB)
		require.NoError(t, err)
		return capErr
	}

	assert.Nil(t, check(747, 235, "", "dave", 512), "fits what is left")

	capErr := check(747, 512, "", "dave", 256)
	require.NotNil(t, capErr, "256 MiB more than the 235 MiB left")
	assert.Equal(t, "Not enough room on the panel's server: needs 256 MiB of memory, 235 MiB left of 747 MiB.", capErr.Error())
	assert.Equal(t, "capacity_exceeded", capErr.Code)
	assert.True(t, capErr.PanelHost)
	assert.False(t, capErr.CanBypass, "an owner of a workspace is not the server's operator")
	assert.True(t, check(747, 512, "", "olga", 256).CanBypass, "an instance operator")

	// Over-committed already. Each service's limit is in the 1024 MiB.
	assert.Nil(t, check(747, 1024, "worker", "dave", 512),
		"a service deployed again with what its live deployment reserved takes no new room")
	assert.NotNil(t, check(747, 1024, "raised", "dave", 512), "a limit raised since it went live is new room")
	assert.Nil(t, check(747, 1024, "old", "dave", 512),
		"live from before reservations were recorded: it holds its limit as it is")
	assert.NotNil(t, check(747, 1024, "new", "dave", 512), "a service never deployed holds nothing")
	assert.NotNil(t, check(747, 1024, "stopped", "dave", 512), "a stopped service holds nothing")
	assert.Nil(t, check(747, 700, "raised", "dave", 512), "a raised limit that fits")

	assert.Nil(t, check(0, 1024, "", "dave", 512), "a ledger that can't say refuses nothing")
	s.WithPreviews(nil, nil, nil)
	capErr, err := s.checkCapacity(context.Background(), "", "", "dave", orgPkg.RoleOwner, "ws", 0, 512)
	require.NoError(t, err)
	assert.Nil(t, capErr, "no ledger")
}
