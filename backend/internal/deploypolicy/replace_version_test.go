package deploypolicy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type exceptionAPI struct {
	t    *testing.T
	r    *gin.Engine
	path string
}

func newExceptionAPI(t *testing.T) *exceptionAPI {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, orgID, projectID, envID, svcID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('op@toft.test', 'Op', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, &projectID, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, compose_content) VALUES ($1, 'agent', 'docker-compose', E'services:\n  web:\n    image: nginx\n') RETURNING id::text`, &svcID, envID)

	svc := deploypolicy.NewService(deploypolicy.NewPostgresRepository(db), deploypolicy.ModeEnforce, zap.NewNop()).
		WithOperatorChecker(fakeOperators{userID: true})
	return &exceptionAPI{t: t, r: newRouter(svc, userID, orgID), path: "/api/v1/services/" + svcID + "/deploy-policy"}
}

func (a *exceptionAPI) call(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		b, err := json.Marshal(body)
		require.NoError(a.t, err)
		reader = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	var out map[string]any
	require.NoError(a.t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	apispec.Check(a.t, method, path, w.Code, out)
	return w.Code, out
}

// read is the rules the exception allows, the digest of the values and the
// read's version.
func (a *exceptionAPI) read() (rules []string, digest, version string) {
	a.t.Helper()
	code, out := a.call(http.MethodGet, a.path, nil)
	require.Equal(a.t, http.StatusOK, code)
	d := out["data"].(map[string]any)
	digest, _ = d["values_digest"].(string)
	version, _ = out["version"].(string)
	rules = []string{}
	if e, ok := d["exception"].(map[string]any); ok {
		for _, r := range e["allowed_rules"].([]any) {
			rules = append(rules, r.(string))
		}
	}
	slices.Sort(rules)
	return rules, digest, version
}

func (a *exceptionAPI) grant(version, digest string, rules ...string) (int, map[string]any) {
	a.t.Helper()
	body := map[string]any{"allowed_rules": rules, "reason": "the agent needs it", "values_digest": digest}
	if version != "" {
		body["version"] = version
	}
	return a.call(http.MethodPost, a.path+"/exception", body)
}

func TestGrantNeedsTheVersionItRead(t *testing.T) {
	a := newExceptionAPI(t)
	got, digest, empty := a.read()
	require.Empty(t, got)
	require.NotEmpty(t, empty, "no exception yet is a version too")

	code, out := a.grant(empty, digest, "docker_socket", "host_exec")
	require.Equal(t, http.StatusOK, code, out)
	saved, _ := out["version"].(string)
	got, digest, read := a.read()
	assert.Equal(t, []string{"docker_socket", "host_exec"}, got)
	assert.NotEqual(t, empty, read)
	assert.Equal(t, read, saved, "the POST's version is not the next GET's")

	code, out = a.grant("", digest, "docker_socket")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "version_required", out["code"])
	assert.Equal(t, "version", out["field"])
	got, _, _ = a.read()
	assert.Equal(t, []string{"docker_socket", "host_exec"}, got, "a request with no version wrote")

	code, out = a.grant("stale", digest, "docker_socket")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", out["code"])
	got, _, _ = a.read()
	assert.Equal(t, []string{"docker_socket", "host_exec"}, got, "a stale version wrote")

	code, out = a.grant(read, digest, "docker_socket")
	require.Equal(t, http.StatusOK, code, out)
	got, _, next := a.read()
	assert.Equal(t, []string{"docker_socket"}, got)
	assert.NotEqual(t, read, next, "a new grant didn't move the version")

	code, out = a.grant(read, digest, "host_exec")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", out["code"])
	got, _, _ = a.read()
	assert.Equal(t, []string{"docker_socket"}, got)
}

func TestConcurrentGrantsOneWins(t *testing.T) {
	a := newExceptionAPI(t)
	for round := range 25 {
		_, digest, version := a.read()
		rules := [2]string{"docker_socket", "host_exec"}
		if round%2 == 1 {
			rules = [2]string{"host_exec", "docker_socket"}
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := [2]int{}
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = a.grant(version, digest, rules[i])
			}()
		}
		close(start)
		wg.Wait()
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes[:], "round %d", round)
		winner := rules[0]
		if codes[1] == http.StatusOK {
			winner = rules[1]
		}
		got, _, _ := a.read()
		require.Equal(t, []string{winner}, got, "round %d: the 409 wrote", round)
	}
}
