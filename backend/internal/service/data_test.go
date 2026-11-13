package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
)

// dataOrch answers ServiceData for the services it holds data of.
type dataOrch struct {
	teardownOrch
	vols map[string][]DataVolume
	err  error
}

func (o *dataOrch) ServiceData(_ context.Context, refs []DataRef, _ bool) (map[string][]DataVolume, error) {
	if o.err != nil {
		return nil, o.err
	}
	out := map[string][]DataVolume{}
	for _, r := range refs {
		out[r.ID] = o.vols[r.ID]
	}
	return out, nil
}

type dataFactory struct{ orch *dataOrch }

func (f dataFactory) ForServer(*serverPkg.Server) (ContainerOrchestrator, func(), error) {
	return f.orch, func() {}, nil
}

type dataRepo struct{ deleteRepo }

// ListData gives each service and, after it, its preview copies (copies),
// as the query does.
func (r *dataRepo) ListData(_ context.Context, ids []string) ([]DataRow, error) {
	out := []DataRow{}
	for _, id := range ids {
		for _, cid := range append([]string{id}, r.copies[id]...) {
			if s, ok := r.byID[cid]; ok {
				out = append(out, DataRow{ID: s.ID, Name: s.Name, Volumes: s.Volumes})
			}
		}
	}
	return out, nil
}

// DELETE /services/{id} takes the name once deleting removes data, for
// every caller (sessions and API keys; agents can't reach it).
func TestDeleteConfirmedAsksForTheNameWhenDataGoes(t *testing.T) {
	cases := []struct {
		name    string
		vols    []DataVolume
		readErr error
		confirm string
		refused bool
	}{
		{name: "no data, no name", refused: false},
		{name: "data, no name", vols: []DataVolume{{Name: "paas-vol-s1-data", Bytes: 10, From: DataFromVolume}}, refused: true},
		{name: "data, wrong name", vols: []DataVolume{{Name: "paas-vol-s1-data", Bytes: -1, From: DataFromVolume}}, confirm: "DB", refused: true},
		{name: "data, its name", vols: []DataVolume{{Name: "paas-vol-s1-data", Bytes: 10, From: DataFromVolume}}, confirm: "db", refused: false},
		{name: "unread, no name", readErr: errors.New("ssh: connection refused"), refused: true},
		{name: "unread, its name", readErr: errors.New("ssh: connection refused"), confirm: "db", refused: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dataRepo{deleteRepo{byID: map[string]*Service{"s1": {ID: "s1", Name: "db", SourceType: SourceTypeDockerImage}}}}
			orch := &dataOrch{vols: map[string][]DataVolume{"s1": tc.vols}, err: tc.readErr}
			m := NewServiceManager(repo, zap.NewNop())
			m.WithOrchestration(nil, dataFactory{orch})
			m.WithConfigFileSeeder(&configRemover{})

			err := m.DeleteConfirmed(context.Background(), "s1", tc.confirm)
			if tc.refused {
				assert.ErrorIs(t, err, ErrConfirmName)
				assert.Empty(t, repo.deleted)
				assert.Empty(t, orch.stopped)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{"s1"}, repo.deleted)
		})
	}
}

// A server that can't be read still names the volumes Toft knows of.
func TestDataUnreadNamesKnownVolumes(t *testing.T) {
	repo := &dataRepo{deleteRepo{byID: map[string]*Service{"s1": {ID: "s1", Name: "db", Volumes: []VolumeMount{
		{Name: "data", Target: "/data"},
		{Name: "host", Target: "/h", HostPath: "/srv/h"},
	}}}}}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, dataFactory{&dataOrch{err: errors.New("timeout")}})

	got, err := m.Data(context.Background(), "", "s1", true)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].Read)
	assert.NotEmpty(t, got[0].Reason)
	assert.Equal(t, []DataVolume{{Name: "paas-vol-s1-data", Bytes: -1, From: DataFromVolume}}, got[0].Volumes)
}

// A preview copy's volumes go with the service (D-97): they need the name
// too, even when the service itself holds nothing.
func TestDeleteConfirmedCountsPreviewCopies(t *testing.T) {
	repo := &dataRepo{deleteRepo{
		byID: map[string]*Service{
			"web":      {ID: "web", Name: "web", SourceType: SourceTypeDockerImage},
			"web-pr42": {ID: "web-pr42", Name: "web", SourceType: SourceTypeDockerImage},
		},
		copies: map[string][]string{"web": {"web-pr42"}},
	}}
	orch := &dataOrch{vols: map[string][]DataVolume{"web-pr42": {{Name: "paas-vol-web-pr42-data", Bytes: 1, From: DataFromVolume}}}}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, dataFactory{orch})
	m.WithConfigFileSeeder(&configRemover{})

	assert.ErrorIs(t, m.DeleteConfirmed(context.Background(), "web", ""), ErrConfirmName)
	assert.Empty(t, repo.deleted)
	require.NoError(t, m.DeleteConfirmed(context.Background(), "web", "web"))
	assert.Equal(t, []string{"web-pr42", "web"}, repo.deleted)
}

// The API holds the name for every caller: 400 confirm_name without it.
func TestHandlerDeleteNeedsTheNameWhenDataGoes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &Service{ID: "s1", Name: "db", SourceType: SourceTypeDockerImage}
	repo := &dataRepo{deleteRepo{fakeRepo: fakeRepo{svc: svc}, byID: map[string]*Service{"s1": svc}}}
	orch := &dataOrch{vols: map[string][]DataVolume{"s1": {{Name: "paas-vol-s1-data", Bytes: 1, From: DataFromVolume}}}}
	m := NewServiceManager(repo, zap.NewNop())
	m.WithOrchestration(nil, dataFactory{orch})
	m.WithConfigFileSeeder(&configRemover{})
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", "dev")
		c.Set("orgID", "org-a")
		c.Set("memberRole", "developer")
		c.Next()
	})
	NewHandler(m, nil, nil, &auditLog{}, nil, zap.NewNop()).RegisterRoutes(g)
	del := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, url, nil))
		return w
	}

	w := del("/api/v1/services/s1")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"confirm_name"`)
	assert.Empty(t, repo.deleted)

	w = del("/api/v1/services/s1?confirm=db")
	assert.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, []string{"s1"}, repo.deleted)
}
