package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/apispec"
)

// roles: u-both owns org-a and org-b; u-a owns only org-a.
type roles map[string]string

func (r roles) RoleIn(_ context.Context, userID, orgID string) (string, error) {
	if role, ok := r[userID+"/"+orgID]; ok {
		return role, nil
	}
	return "", errors.New("not a member")
}

var bothOwner = roles{"u-both/org-a": "owner", "u-both/org-b": "owner", "u-a/org-a": "owner", "u-a/org-b": "developer"}

func sharingService(isolated bool) (*Service, *fakeRepo) {
	cpu, mem := 4.0, int64(7745)
	repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", Name: "web-1", TotalCPUCores: &cpu, TotalMemoryMB: &mem})
	s := NewService(repo, "")
	s.WithAppNetworks(isolated, "10.203.0.0/16")
	s.WithOrgRoles(bothOwner)
	return s, repo
}

func TestShareWith(t *testing.T) {
	ctx := context.Background()

	s, _ := sharingService(false)
	assert.ErrorIs(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 1, 1536), ErrSharingNeedsIsolation, "legacy app networks")

	s, repo := sharingService(true)
	assert.ErrorIs(t, s.ShareWith(ctx, "u-both", "org-b", testServerID, "org-a", 1, 1536), ErrServerNotFound, "only the owner workspace shares")
	assert.ErrorIs(t, s.ShareWith(ctx, "u-a", "org-a", testServerID, "org-b", 1, 1536), ErrNotTargetOwner, "a developer of the target cannot accept for it")
	assert.ErrorIs(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-a", 1, 1536), ErrInvalidShare)
	assert.ErrorIs(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 0, 1536), ErrInvalidShare)
	assert.ErrorIs(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 4, 1536), ErrShareTooLarge, "the owner would keep no CPU")

	require.NoError(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 1, 1536))
	require.NoError(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 2, 2048), "changing a share does not count the old one")
	assert.Equal(t, Share{OrgID: "org-b", CPUCores: 2, MemoryMB: 2048}, repo.shares[testServerID]["org-b"])

	ok, err := s.CanUse(ctx, testServerID, "org-b")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _ = s.CanUse(ctx, testServerID, "org-c")
	assert.False(t, ok)
	ok, _ = s.CanUse(ctx, testServerID, "org-a")
	assert.True(t, ok, "the owner")
}

func TestCapacityPerWorkspace(t *testing.T) {
	ctx := context.Background()
	s, _ := sharingService(true)
	require.NoError(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 1, 1536))

	owner, err := s.GetCapacitySummaryFor(ctx, testServerID, "org-a")
	require.NoError(t, err)
	assert.Equal(t, 3.0, owner.EffectiveCPUCores)
	assert.Equal(t, int64(7745-1536), owner.EffectiveMemoryMB)

	sharer, err := s.GetCapacitySummaryFor(ctx, testServerID, "org-b")
	require.NoError(t, err)
	assert.Equal(t, 1.0, sharer.EffectiveCPUCores)
	assert.Equal(t, int64(1536), sharer.EffectiveMemoryMB)
	assert.True(t, sharer.CapacityDiscovered)

	_, err = s.GetCapacitySummaryFor(ctx, testServerID, "org-c")
	assert.ErrorIs(t, err, ErrNoShare)
}

func TestUnshareAndDeleteWithServicesOnTheServer(t *testing.T) {
	ctx := context.Background()
	s, repo := sharingService(true)
	require.NoError(t, s.ShareWith(ctx, "u-both", "org-a", testServerID, "org-b", 1, 1536))
	repo.orgServices = map[string]map[string]int64{testServerID: {"org-b": 5}}

	assert.ErrorIs(t, s.Unshare(ctx, "org-a", testServerID, "org-b"), ErrShareInUse)
	assert.ErrorIs(t, s.DeleteServer(ctx, testServerID, false), ErrServerShared)
	_, still := repo.servers[testServerID]
	assert.True(t, still)

	repo.orgServices[testServerID]["org-b"] = 0
	require.NoError(t, s.Unshare(ctx, "org-a", testServerID, "org-b"))
	assert.ErrorIs(t, s.Unshare(ctx, "org-a", testServerID, "org-b"), ErrNoShare)
	require.NoError(t, s.DeleteServer(ctx, testServerID, false))
}

// The workspace a server is shared with sees it in its list without an
// address; only owners of the server's workspace manage shares.
func TestShareRoutes(t *testing.T) {
	s, _ := sharingService(true)
	audit := &fakeAudit{}

	owner := roleRouterAs(s, audit, "owner", "org-a", "u-both")
	code, body := callJSON(t, owner, http.MethodPut, "/api/v1/servers/"+testServerID+"/shares/org-b", map[string]any{"cpu_cores": 1, "memory_mb": 1536})
	require.Equal(t, http.StatusOK, code, body)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, "server.shared", audit.entries[0].action)

	dev := roleRouterAs(s, audit, "developer", "org-a", "u-both")
	code, _ = callJSON(t, dev, http.MethodPut, "/api/v1/servers/"+testServerID+"/shares/org-b", map[string]any{"cpu_cores": 1, "memory_mb": 1536})
	assert.Equal(t, http.StatusForbidden, code)

	sharer := roleRouterAs(s, audit, "owner", "org-b", "u-both")
	code, _ = callJSON(t, sharer, http.MethodGet, "/api/v1/servers/"+testServerID+"/shares", nil)
	assert.Equal(t, http.StatusForbidden, code, "the shared-with workspace does not manage shares")
	code, _ = callJSON(t, sharer, http.MethodGet, "/api/v1/servers/"+testServerID, nil)
	assert.Equal(t, http.StatusForbidden, code, "nor reads the server's details")
	code, body = callJSON(t, sharer, http.MethodGet, "/api/v1/servers", nil)
	require.Equal(t, http.StatusOK, code)
	apispec.Check(t, http.MethodGet, "/api/v1/servers", code, body)
	ownCode, ownBody := callJSON(t, owner, http.MethodGet, "/api/v1/servers", nil)
	require.Equal(t, http.StatusOK, ownCode)
	require.Len(t, ownBody["data"].([]any), 1, "the owner's own server")
	apispec.Check(t, http.MethodGet, "/api/v1/servers", ownCode, ownBody)
	list := body["data"].([]any)
	require.Len(t, list, 1)
	entry := list[0].(map[string]any)
	assert.Equal(t, true, entry["shared"])
	assert.Equal(t, "owner", entry["shared_by"])
	assert.Equal(t, "org-a", entry["shared_by_org_id"])
	assert.Equal(t, 1.0, entry["total_cpu_cores"])
	assert.NotContains(t, entry, "ip_address")
	assert.NotContains(t, entry, "port")
}

// The capacity summary: the server's owner sees the whole server; a
// workspace with a share sees its share, marked as one, and none of the
// server's live figures; any other workspace gets 403.
func TestCapacitySummaryForAShare(t *testing.T) {
	s, _ := sharingService(true)
	audit := &fakeAudit{}
	owner := roleRouterAs(s, audit, "owner", "org-a", "u-both")
	code, body := callJSON(t, owner, http.MethodPut, "/api/v1/servers/"+testServerID+"/shares/org-b", map[string]any{"cpu_cores": 1, "memory_mb": 1536})
	require.Equal(t, http.StatusOK, code, body)

	code, body = callJSON(t, owner, http.MethodGet, "/api/v1/servers/"+testServerID+"/capacity-summary", nil)
	require.Equal(t, http.StatusOK, code, body)
	whole := body["data"].(map[string]any)
	assert.Equal(t, 7745.0, whole["effective_memory_mb"], "the owner: the whole server")
	assert.Equal(t, false, whole["share"])

	guest := roleRouterAs(s, audit, "viewer", "org-b", "u-a")
	code, body = callJSON(t, guest, http.MethodGet, "/api/v1/servers/"+testServerID+"/capacity-summary", nil)
	require.Equal(t, http.StatusOK, code, body)
	share := body["data"].(map[string]any)
	assert.Equal(t, 1536.0, share["effective_memory_mb"], "the guest: its share")
	assert.Equal(t, 1.0, share["effective_cpu_cores"])
	assert.Equal(t, true, share["share"])
	assert.Equal(t, 0.0, share["live_memory_mb"], "no live figures of the whole server")
	assert.NotContains(t, share, "owner_org_id")

	other := roleRouterAs(s, audit, "owner", "org-c", "u-c")
	code, _ = callJSON(t, other, http.MethodGet, "/api/v1/servers/"+testServerID+"/capacity-summary", nil)
	assert.Equal(t, http.StatusNotFound, code)
}

// The fit check: the owner measures against the server less its shares, a
// workspace with a share against its share, by the deploy gate's
// arithmetic; a resource not asked for always fits; anyone else gets 403.
func TestFitRoute(t *testing.T) {
	s, _ := sharingService(true)
	audit := &fakeAudit{}
	owner := roleRouterAs(s, audit, "owner", "org-a", "u-both")
	code, body := callJSON(t, owner, http.MethodPut, "/api/v1/servers/"+testServerID+"/shares/org-b", map[string]any{"cpu_cores": 1, "memory_mb": 1536})
	require.Equal(t, http.StatusOK, code, body)
	fit := func(r *gin.Engine, query string) (int, map[string]any) {
		code, body := callJSON(t, r, http.MethodGet, "/api/v1/servers/"+testServerID+"/fit"+query, nil)
		data, _ := body["data"].(map[string]any)
		return code, data
	}

	code, res := fit(owner, "?memory_mb=6209")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, res["fits"], "7745 less the 1536 share")
	assert.Equal(t, 3.0, res["effective_cpu_cores"])
	_, res = fit(owner, "?memory_mb=6210")
	assert.Equal(t, false, res["fits"])
	assert.Equal(t, false, res["memory_ok"])
	assert.Equal(t, true, res["cpu_ok"], "no CPU asked for")

	guest := roleRouterAs(s, audit, "developer", "org-b", "u-a")
	_, res = fit(guest, "?cpu_cores=1&memory_mb=1536")
	assert.Equal(t, true, res["fits"])
	assert.Equal(t, true, res["share"])
	_, res = fit(guest, "?cpu_cores=1.5")
	assert.Equal(t, false, res["cpu_ok"])

	for _, bad := range []string{"?cpu_cores=-1", "?memory_mb=abc", "?cpu_cores=NaN", "?cpu_cores=5000"} {
		code, _ = fit(owner, bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}
	code, _ = fit(roleRouterAs(s, audit, "owner", "org-c", "u-c"), "")
	assert.Equal(t, http.StatusNotFound, code)
}

type fakeForgetter struct{ servers []string }

func (f *fakeForgetter) ForgetServicesOnServer(_ context.Context, id string) error {
	f.servers = append(f.servers, id)
	return nil
}

// A server with services is not removed (their containers would run on with
// nothing managing them) unless its services are deleted with it.
func TestDeleteServerWithServicesOnIt(t *testing.T) {
	ctx := context.Background()
	s, repo := sharingService(true)
	f := &fakeForgetter{}
	s.WithServiceForgetter(f)
	repo.orgServices = map[string]map[string]int64{testServerID: {"org-a": 2}}

	assert.ErrorIs(t, s.DeleteServer(ctx, testServerID, false), ErrServerInUse)
	assert.Empty(t, f.servers)
	_, still := repo.servers[testServerID]
	assert.True(t, still)

	require.NoError(t, s.DeleteServer(ctx, testServerID, true))
	assert.Equal(t, []string{testServerID}, f.servers)
	_, still = repo.servers[testServerID]
	assert.False(t, still)
}

// Removing a server with its services takes their variables and secrets out
// of Toft: owners only, with the password; an empty server stays any
// writer's to remove.
func TestDeleteServerWithServicesNeedsOwnerAndPassword(t *testing.T) {
	cases := []struct {
		name, role, query string
		reauth            bool
		want              int
	}{
		{"developer, with services", "developer", "?with_services=true", true, http.StatusForbidden},
		{"owner without the password", "owner", "?with_services=true", false, http.StatusForbidden},
		{"owner with the password", "owner", "?with_services=true", true, http.StatusNoContent},
		{"developer, empty server", "developer", "", false, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, repo := sharingService(true)
			s.WithServiceForgetter(&fakeForgetter{})
			if tc.query != "" {
				repo.orgServices = map[string]map[string]int64{testServerID: {"org-a": 2}}
			}
			gin.SetMode(gin.TestMode)
			r := gin.New()
			g := r.Group("/api/v1")
			g.Use(func(c *gin.Context) {
				c.Set("userID", "u-a")
				c.Set("orgID", "org-a")
				c.Set("memberRole", tc.role)
				c.Set("reauthenticated", tc.reauth)
				c.Next()
			})
			NewHandler(s, &fakeAudit{}, zap.NewNop()).RegisterRoutes(g)

			code, body := callJSON(t, r, http.MethodDelete, "/api/v1/servers/"+testServerID+tc.query, nil)
			assert.Equal(t, tc.want, code, body)
			_, still := repo.servers[testServerID]
			assert.Equal(t, tc.want != http.StatusNoContent, still)
		})
	}
}
