package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// savingRepo saves a config as after: the service as the save left it.
type savingRepo struct {
	fakeRepo
	after *Service
}

func (r *savingRepo) UpdateConfig(context.Context, string, UpdateConfigInput) (*Service, error) {
	return r.after, nil
}

// detailAudit keeps each entry with its detail.
type detailAudit struct{ entries []detailEntry }

type detailEntry struct {
	actor, action, resource string
	detail                  map[string]any
}

func (a *detailAudit) WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string) {
	a.WriteAuditLogDetail(ctx, orgID, actorID, action, resource, nil)
}

func (a *detailAudit) WriteAuditLogDetail(_ context.Context, _, actorID, action, resource string, detail map[string]any) {
	a.entries = append(a.entries, detailEntry{actorID, action, resource, detail})
}

// A Settings save is in the audit log, naming the settings it changed and
// never a credential's value; a save that changes nothing writes nothing.
func TestHandler_UpdateConfig_AuditsTheSettingsChanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stored := &Service{
		ID: "s1", Name: "whoami", SourceType: SourceTypeDockerImage, DockerImage: "traefik/whoami", Port: 80,
		BasicAuthUser: "ops", BasicAuthPassword: "old-s3cret", IPAllowlist: []string{"10.0.0.0/8"},
	}
	after := *stored
	after.StartCommand = "/whoami --port 80"
	after.HealthCheckRetries = 5
	after.BasicAuthPassword = "n3w-s3cret"
	after.IPAllowlist = []string{"10.0.0.0/8", "192.0.2.0/24"}
	repo := &savingRepo{fakeRepo: fakeRepo{svc: stored}, after: &after}
	audit := &detailAudit{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "org-a")
		c.Set("memberRole", "developer")
		c.Next()
	})
	NewHandler(NewServiceManager(repo, zap.NewNop()), nil, nil, audit, nil, zap.NewNop()).RegisterRoutes(g)
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/services/s1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	put(`{"start_command": "/whoami --port 80", "health_check_retries": 5, "basic_auth_password": "n3w-s3cret",
		"ip_allowlist": ["10.0.0.0/8", "192.0.2.0/24"]}`)
	require.Len(t, audit.entries, 1)
	e := audit.entries[0]
	assert.Equal(t, "dev", e.actor)
	assert.Equal(t, "service.settings_changed", e.action)
	assert.Equal(t, "service:s1", e.resource)
	assert.Equal(t, map[string]any{"fields": []string{"start_command", "health_check_retries", "basic_auth_password", "ip_allowlist"}}, e.detail)
	raw, err := json.Marshal(e.detail)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "s3cret", "a credential is named, never recorded")

	audit.entries = nil
	same := *stored
	repo.after = &same
	put(`{"start_command": ""}`)
	assert.Empty(t, audit.entries, "a save that changes nothing")
}

// moveRepo moves the service to the server it is given.
type moveRepo struct{ fakeRepo }

func (r *moveRepo) AssignServer(_ context.Context, _, serverID string) (*Service, error) {
	r.svc.ServerID = serverID
	return r.svc, nil
}

// pruneOrch records the volumes it removes.
type pruneOrch struct {
	fakeOrch
	pruned []string
}

func (o *pruneOrch) PruneVolume(_ context.Context, name string) error {
	o.pruned = append(o.pruned, name)
	return nil
}

// Moving a service that runs nowhere to another server, and destroying a
// volume's data, are in the audit log; staying put is not.
func TestHandler_ServerChangeAndVolumePurgeAreAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &moveRepo{fakeRepo{svc: &Service{ID: "s1", Name: "web", RuntimeStatus: "idle"}}}
	m := NewServiceManager(repo, zap.NewNop())
	orch := &pruneOrch{}
	m.WithOrchestration(fakeServers{}, reconcilerFactory{orch})
	audit := &detailAudit{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "org-a")
		c.Set("memberRole", "developer")
		c.Next()
	})
	h := NewHandler(m, nil, nil, audit, nil, zap.NewNop())
	h.WithServerChecker(orgServers{})
	h.RegisterRoutes(g)
	do := func(method, path, body string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Less(t, w.Code, 300, w.Body.String())
	}

	do(http.MethodPut, "/api/v1/services/s1/server", `{"server_id": "srv-a"}`)
	do(http.MethodPut, "/api/v1/services/s1/server", `{"server_id": "srv-a"}`)
	do(http.MethodPut, "/api/v1/services/s1/server", `{"server_id": ""}`)
	do(http.MethodDelete, "/api/v1/services/s1/volumes/data", "")
	require.Len(t, orch.pruned, 1)
	assert.Equal(t, []detailEntry{
		{"dev", "service.server_changed", "service:s1", map[string]any{"from": "", "to": "srv-a"}},
		{"dev", "service.server_changed", "service:s1", map[string]any{"from": "srv-a", "to": ""}},
		{"dev", "service.volume_purged", "service:s1", map[string]any{"mount": "data"}},
	}, audit.entries)
}

// Every setting a config save takes is named when it changes: a new one
// fails here until changedSettings compares it. Isolation has its own entry.
func TestChangedSettings_NamesEverySetting(t *testing.T) {
	after := &Service{}
	v := reflect.ValueOf(after).Elem()
	for i := range v.NumField() {
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			f.SetString("x")
		case reflect.Int:
			f.SetInt(1)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		}
	}
	changed := changedSettings(&Service{}, after)
	in := reflect.TypeOf(UpdateConfigInput{})
	for i := range in.NumField() {
		name, _, _ := strings.Cut(in.Field(i).Tag.Get("json"), ",")
		if name == "-" || name == "isolation" {
			continue
		}
		assert.Contains(t, changed, name)
	}
	assert.Empty(t, changedSettings(after, after))
}
