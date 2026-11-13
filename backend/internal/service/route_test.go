package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// recordingRepo records the update and create inputs it receives.
type recordingRepo struct {
	fakeRepo
	lastUpdate *UpdateConfigInput
	lastCreate *CreateInput
}

func (r *recordingRepo) UpdateConfig(ctx context.Context, id string, in UpdateConfigInput) (*Service, error) {
	r.lastUpdate = &in
	return r.fakeRepo.UpdateConfig(ctx, id, in)
}

func (r *recordingRepo) Create(ctx context.Context, envID string, in CreateInput) (*Service, error) {
	r.lastCreate = &in
	return r.fakeRepo.Create(ctx, envID, in)
}

func strPtr(s string) *string { return &s }

func TestUpdateConfig_ComposeRouteService(t *testing.T) {
	ctx := context.Background()
	for _, ok := range []string{"", "web", "web.v2", "api_1", "a-b", "9", strings.Repeat("a", 63)} {
		repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose}}}
		m := NewServiceManager(repo, zap.NewNop())
		_, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{ComposeRouteService: strPtr(ok), Port: 8080})
		require.NoError(t, err, "%q", ok)
		require.NotNil(t, repo.lastUpdate.ComposeRouteService)
		assert.Equal(t, ok, *repo.lastUpdate.ComposeRouteService)
		assert.Equal(t, 8080, repo.lastUpdate.Port, "compose services save their port")
	}
	for _, bad := range []string{"-web", ".web", "web app", "web:", "${HOME}", "web\n", "wéb", "we/b", strings.Repeat("a", 64)} {
		repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose}}}
		m := NewServiceManager(repo, zap.NewNop())
		_, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{ComposeRouteService: strPtr(bad)})
		assert.ErrorIs(t, err, ErrInvalidConfig, "%q", bad)
		assert.Nil(t, repo.lastUpdate, "nothing saved for %q", bad)
	}

	// Not sent: the stored choice is kept (the repository leaves it alone).
	repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "stack"}}}
	_, err := NewServiceManager(repo, zap.NewNop()).UpdateConfig(ctx, "s1", UpdateConfigInput{})
	require.NoError(t, err)
	assert.Nil(t, repo.lastUpdate.ComposeRouteService)
}

func TestCreate_ComposeRouteServiceIsValidated(t *testing.T) {
	repo := &recordingRepo{}
	m := NewServiceManager(repo, zap.NewNop())
	_, err := m.Create(context.Background(), "env-1", "", CreateInput{Name: "stack", SourceType: SourceTypeDockerCompose, ComposeRouteService: "bad name"})
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Nil(t, repo.lastCreate)

	_, err = m.Create(context.Background(), "env-1", "", CreateInput{Name: "stack", SourceType: SourceTypeDockerCompose, ComposeRouteService: "web"})
	require.NoError(t, err)
	assert.Equal(t, "web", repo.lastCreate.ComposeRouteService)
}

// Access control given at create is kept (it reaches the repository), and
// what Traefik could not load is refused on create and on save.
func TestAccessControlIsCheckedAndKept(t *testing.T) {
	ctx := context.Background()
	repo := &recordingRepo{}
	m := NewServiceManager(repo, zap.NewNop())
	ok := CreateInput{Name: "web", SourceType: SourceTypeDockerImage, BasicAuthUser: "u", BasicAuthPassword: "p", IPAllowlist: []string{"10.0.0.0/8", "203.0.113.7"}}
	_, err := m.Create(ctx, "env-1", "", ok)
	require.NoError(t, err)
	assert.Equal(t, "u", repo.lastCreate.BasicAuthUser)
	assert.Equal(t, []string{"10.0.0.0/8", "203.0.113.7"}, repo.lastCreate.IPAllowlist)

	for name, in := range map[string]CreateInput{
		"user without password": {BasicAuthUser: "u"},
		"password without user": {BasicAuthPassword: "p"},
		"colon in user":         {BasicAuthUser: "a:b", BasicAuthPassword: "p"},
		"bad allowlist entry":   {IPAllowlist: []string{"10.0.0.0/33"}},
	} {
		repo := &recordingRepo{}
		in.Name, in.SourceType = "web", SourceTypeDockerImage
		_, err := NewServiceManager(repo, zap.NewNop()).Create(ctx, "env-1", "", in)
		assert.ErrorIs(t, err, ErrInvalidConfig, name)
		assert.Nil(t, repo.lastCreate, "nothing created for %s", name)
	}

	repo = &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "web"}}}
	_, err = NewServiceManager(repo, zap.NewNop()).UpdateConfig(ctx, "s1", UpdateConfigInput{IPAllowlist: []string{"office"}})
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Nil(t, repo.lastUpdate)
}

func TestHandler_UpdateConfig_InvalidRouteServiceIs400(t *testing.T) {
	repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "stack", SourceType: SourceTypeDockerCompose}}}
	r := newTestRouter(NewServiceManager(repo, zap.NewNop()), "developer")

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/services/s1/config", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := put(`{"compose_route_service": "web: {privileged: true}"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "compose_route_service")
	assert.Nil(t, repo.lastUpdate)

	w = put(`{"port": 70000}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, "an invalid port is a 400, not a 500: %s", w.Body.String())

	w = put(`{"compose_route_service": "web", "port": 8080}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, repo.lastUpdate.ComposeRouteService)
	assert.Equal(t, "web", *repo.lastUpdate.ComposeRouteService)
}

// sourceRepo also takes a save's repository source and deploy on push.
type sourceRepo struct {
	recordingRepo
	deployOnPush *bool
}

func (r *sourceRepo) SetSource(_ context.Context, _ string, _ *string, deployOnPush *bool) error {
	r.deployOnPush = deployOnPush
	return nil
}

// A save changes what its body sends and keeps the rest: a body with only
// deploy_on_push once emptied the branch, the base directory, the build pack
// and the port, and a body without memory_limit was refused for removing it.
func TestHandler_UpdateConfig_KeepsWhatTheBodyLeavesOut(t *testing.T) {
	stored := &Service{
		ID: "s1", Name: "site", SourceType: SourceTypeGit, RepositoryID: "06a0341f-ff6f-4dcb-8a4e-401fa1355563",
		GitBranch: "main", BaseDirectory: "web", BuildPack: "dockerfile", Port: 8080, MemoryLimit: "512m",
		HealthCheckPath: "/health", HealthCheckTimeout: 5,
		Volumes:     []VolumeMount{{Name: "data", Target: "/data", HostPath: "/srv/data"}},
		IPAllowlist: []string{"10.0.0.0/8"},
	}
	repo := &sourceRepo{recordingRepo: recordingRepo{fakeRepo: fakeRepo{svc: stored}}}
	r := newTestRouter(NewServiceManager(repo, zap.NewNop()), "developer")
	put := func(body string) *UpdateConfigInput {
		t.Helper()
		repo.lastUpdate = nil
		req := httptest.NewRequest(http.MethodPut, "/api/v1/services/s1/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", body, w.Body.String())
		return repo.lastUpdate
	}

	in := put(`{"deploy_on_push": false}`)
	assert.Equal(t, "main", in.GitBranch)
	assert.Equal(t, "web", in.BaseDirectory)
	assert.Equal(t, "dockerfile", in.BuildPack)
	assert.Equal(t, 8080, in.Port)
	assert.Equal(t, "512m", in.MemoryLimit)
	assert.Equal(t, "/health", in.HealthCheckPath)
	assert.Equal(t, stored.Volumes, in.Volumes)
	assert.Equal(t, stored.IPAllowlist, in.IPAllowlist)
	assert.Nil(t, in.RepositoryID, "the source is kept")
	require.NotNil(t, repo.deployOnPush)
	assert.False(t, *repo.deployOnPush)

	in = put(`{"memory_limit": null, "volumes": null}`)
	assert.Equal(t, "512m", in.MemoryLimit, "null keeps")
	assert.Equal(t, stored.Volumes, in.Volumes, "null keeps")

	in = put(`{"git_branch": "", "ip_allowlist": []}`)
	assert.Empty(t, in.GitBranch, `"" clears`)
	assert.Empty(t, in.IPAllowlist, "[] clears")
	assert.Equal(t, "web", in.BaseDirectory)

	// A list sent is the new list: nothing of the stored elements survives
	// into it (encoding/json would decode it over them).
	in = put(`{"volumes": [{"name": "data", "target": "/data"}]}`)
	assert.Equal(t, []VolumeMount{{Name: "data", Target: "/data"}}, in.Volumes)
	assert.Equal(t, []VolumeMount{{Name: "data", Target: "/data", HostPath: "/srv/data"}}, stored.Volumes, "the stored service is untouched")
}

// configOver fills every field of a save from the stored service but the
// write-only secrets and the fields a nil already keeps. A field added to
// UpdateConfigInput without it here would be emptied by every save that
// leaves it out.
func TestConfigOverFillsEveryField(t *testing.T) {
	keptByRepository := map[string]bool{
		"RepositoryID": true, "DeployOnPush": true, "ComposeRouteService": true, "PidsLimit": true, "DeployTimeoutMinutes": true, "RollbackImagesKept": true, "Isolation": true,
		"ContainerHealth": true, "ContainerHealthCommand": true,
		"RegistryPassword": true, "GitToken": true, "BasicAuthPassword": true,
		// Set by UpdateConfig alone (credentialsMoved), never from the stored service.
		"ClearGitToken": true, "ClearRegistryPassword": true,
	}
	var s Service
	sv := reflect.ValueOf(&s).Elem()
	for i := 0; i < sv.NumField(); i++ {
		switch f := sv.Field(i); f.Kind() {
		case reflect.String:
			f.SetString("set")
		case reflect.Int:
			f.SetInt(7)
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		}
	}
	in, err := configOver(&s, func(*UpdateConfigInput) error { return nil })
	require.NoError(t, err)
	iv := reflect.ValueOf(in)
	for i := 0; i < iv.NumField(); i++ {
		name := iv.Type().Field(i).Name
		if keptByRepository[name] {
			assert.True(t, iv.Field(i).IsZero(), "%s stays empty: the repository keeps it", name)
			continue
		}
		assert.False(t, iv.Field(i).IsZero(), "configOver leaves %s empty, so a save without it clears it", name)
	}
}

func TestService_ComposeRouteServiceIsAlwaysInTheJSON(t *testing.T) {
	b, err := json.Marshal(Service{ID: "s1"})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"compose_route_service":""`)

	var in UpdateConfigInput
	require.NoError(t, json.Unmarshal([]byte(`{"name":"x"}`), &in))
	assert.Nil(t, in.ComposeRouteService, "absent means keep")
	require.NoError(t, json.Unmarshal([]byte(`{"compose_route_service":""}`), &in))
	require.NotNil(t, in.ComposeRouteService)
	assert.Equal(t, "", *in.ComposeRouteService, "empty means automatic")
	assert.False(t, errors.Is(validateComposeRouteService(""), ErrInvalidConfig))
}
