package sharedvolume_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/sharedvolume"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type oneService struct{}

func (oneService) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (oneService) Get(_ context.Context, id string) (*service.Service, error) {
	return &service.Service{ID: id, SourceType: "docker-image"}, nil
}

type api struct {
	t       *testing.T
	r       *gin.Engine
	path    string
	a, b, c string // volume IDs
}

func newAPI(t *testing.T) *api {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, orgID, projectID, envID, svcID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, &projectID, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'web', 'docker-image', 'web:1') RETURNING id::text`, &svcID, envID)
	a := &api{t: t, path: "/api/v1/services/" + svcID + "/shared-volumes"}
	for name, dst := range map[string]*string{"a": &a.a, "b": &a.b, "c": &a.c} {
		q(`INSERT INTO shared_volumes (org_id, name, size_limit_bytes) VALUES ($1, $2, 1024) RETURNING id::text`, dst, orgID, name)
	}

	gin.SetMode(gin.TestMode)
	a.r = gin.New()
	g := a.r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", orgID)
		c.Set("userID", userID)
		c.Set("memberRole", "owner")
		c.Next()
	})
	svc := sharedvolume.NewService(sharedvolume.NewPostgresRepository(db), oneService{}, nil, nil, zap.NewNop())
	sharedvolume.NewHandler(svc, zap.NewNop()).RegisterRoutes(g)
	return a
}

func (a *api) call(method, body string) (int, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequest(method, a.path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	var out map[string]any
	require.NoError(a.t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	apispec.Check(a.t, method, a.path, w.Code, out)
	return w.Code, out
}

func (a *api) read() (map[string]string, string) {
	a.t.Helper()
	code, out := a.call(http.MethodGet, "")
	require.Equal(a.t, http.StatusOK, code)
	mounts := map[string]string{}
	for _, m := range out["data"].([]any) {
		row := m.(map[string]any)
		mounts[row["target"].(string)] = row["shared_volume_id"].(string)
	}
	version, _ := out["version"].(string)
	return mounts, version
}

func body(version string, mounts map[string]string) string {
	list := make([]map[string]any, 0, len(mounts))
	for target, vol := range mounts {
		list = append(list, map[string]any{"shared_volume_id": vol, "target": target, "read_only": false})
	}
	m := map[string]any{"mounts": list}
	if version != "" {
		m["version"] = version
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func codeOf(out map[string]any) string {
	code, _ := out["code"].(string)
	return code
}

// A replace with no version, or a stale one, writes nothing; the version it
// read replaces; an empty list has its own, so a first save works.
func TestReplaceMountsNeedsTheVersionItRead(t *testing.T) {
	a := newAPI(t)
	first := map[string]string{"/a": a.a, "/b": a.b}

	got, empty := a.read()
	require.Empty(t, got)
	require.NotEmpty(t, empty, "an empty list has a version")
	code, out := a.call(http.MethodPut, body(empty, first))
	require.Equal(t, http.StatusOK, code, out)
	saved, _ := out["version"].(string)
	got, read := a.read()
	assert.Equal(t, first, got)
	assert.NotEqual(t, empty, read)
	assert.Equal(t, read, saved, "the PUT's version is not the next GET's")

	// No version: nothing written, and an empty body can't clear the list.
	for _, b := range []string{body("", map[string]string{"/a": a.a}), `{"mounts":[]}`, `{}`} {
		code, out = a.call(http.MethodPut, b)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Equal(t, "version_required", codeOf(out))
		assert.Equal(t, "version", out["field"])
	}
	got, _ = a.read()
	assert.Equal(t, first, got, "a request with no version wrote")

	code, out = a.call(http.MethodPut, body("stale", map[string]string{"/a": a.a}))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", codeOf(out))
	got, _ = a.read()
	assert.Equal(t, first, got, "a stale version wrote")

	second := map[string]string{"/a": a.a, "/c": a.c}
	code, out = a.call(http.MethodPut, body(read, second))
	require.Equal(t, http.StatusOK, code, out)
	got, _ = a.read()
	assert.Equal(t, second, got)

	// The first read is stale now; only what the last save answered is good.
	next, _ := out["version"].(string)
	code, out = a.call(http.MethodPut, body(read, first))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", codeOf(out))
	code, _ = a.call(http.MethodPut, body(next, map[string]string{}))
	assert.Equal(t, http.StatusOK, code)
	got, _ = a.read()
	assert.Empty(t, got)
}

// A replace that leaves the list out is refused, even with the right version:
// only an empty list clears it, so an unset field can't remove every mount.
func TestReplaceMountsLeftOutWritesNothing(t *testing.T) {
	a := newAPI(t)
	first := map[string]string{"/a": a.a}
	_, empty := a.read()
	code, out := a.call(http.MethodPut, body(empty, first))
	require.Equal(t, http.StatusOK, code, out)
	_, read := a.read()

	for _, b := range []string{
		fmt.Sprintf(`{"version":%q}`, read),
		fmt.Sprintf(`{"version":%q,"mounts":null}`, read),
	} {
		code, out = a.call(http.MethodPut, b)
		assert.Equal(t, http.StatusBadRequest, code, b)
		assert.Contains(t, out["error"], "mounts", b)
	}
	got, _ := a.read()
	assert.Equal(t, first, got, "a request that left the list out wrote")

	code, out = a.call(http.MethodPut, fmt.Sprintf(`{"version":%q,"mounts":[]}`, read))
	require.Equal(t, http.StatusOK, code, out)
	got, _ = a.read()
	assert.Empty(t, got, "an empty list clears them")
}

// Two replaces with the same version: exactly one wins, the other is 409 and
// changes nothing.
func TestConcurrentMountReplacesOneWins(t *testing.T) {
	a := newAPI(t)
	for round := range 25 {
		_, version := a.read()
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := [2]int{}
		targets := [2]string{fmt.Sprintf("/r%d-0", round), fmt.Sprintf("/r%d-1", round)}
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = a.call(http.MethodPut, body(version, map[string]string{targets[i]: a.a}))
			}()
		}
		close(start)
		wg.Wait()
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes[:], "round %d", round)
		winner := targets[0]
		if codes[1] == http.StatusOK {
			winner = targets[1]
		}
		got, _ := a.read()
		require.Equal(t, map[string]string{winner: a.a}, got, "round %d: the 409 wrote", round)
	}
}
