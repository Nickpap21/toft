package issue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type ownsProject struct{}

func (ownsProject) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }

type issueAPI struct {
	t       *testing.T
	db      *pgxpool.Pool
	r       *gin.Engine
	project string
	org     string
}

func newIssueAPI(t *testing.T) *issueAPI {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, orgID, projectID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name, issue_key) VALUES ($1, $2, 'Shop', 'SHOP') RETURNING id::text`, &projectID, userID, orgID)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", orgID)
		c.Set("userID", userID)
		c.Set("memberRole", "owner")
		c.Next()
	})
	svc := NewService(NewPostgresRepository(db), fakeCode{}, zap.NewNop())
	NewHandler(svc, ownsProject{}, zap.NewNop()).RegisterRoutes(g)
	return &issueAPI{t: t, db: db, r: r, project: "/api/v1/projects/" + projectID, org: orgID}
}

func (a *issueAPI) call(method, path, body string) (int, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	var out map[string]any
	require.NoError(a.t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	apispec.Check(a.t, method, path, w.Code, out)
	return w.Code, out
}

func strs(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, s.(string))
	}
	slices.Sort(out)
	return out
}

func problem(out map[string]any) string {
	code, _ := out["code"].(string)
	return code
}

func (a *issueAPI) newIssue(labels ...string) string {
	a.t.Helper()
	b, _ := json.Marshal(map[string]any{"title": "Fix it", "labels": labels})
	code, out := a.call(http.MethodPost, a.project+"/issues", string(b))
	require.Equal(a.t, http.StatusCreated, code, out)
	return fmt.Sprintf("%s/issues/%d", a.project, int(out["data"].(map[string]any)["number"].(float64)))
}

// labels is an issue's labels and its read's version.
func (a *issueAPI) labels(path string) ([]string, string) {
	a.t.Helper()
	code, out := a.call(http.MethodGet, path, "")
	require.Equal(a.t, http.StatusOK, code)
	version, _ := out["version"].(string)
	return strs(out["data"].(map[string]any)["labels"]), version
}

func labelsBody(version string, labels ...string) string {
	if labels == nil {
		labels = []string{}
	}
	m := map[string]any{"labels": labels}
	if version != "" {
		m["version"] = version
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestReplaceLabelsNeedsTheVersionItRead(t *testing.T) {
	a := newIssueAPI(t)
	issue := a.newIssue("api", "bug")
	got, read := a.labels(issue)
	require.Equal(t, []string{"api", "bug"}, got)
	require.NotEmpty(t, read)

	for _, b := range []string{labelsBody("", "api"), `{"labels":[]}`, `{"labels":["api"],"title":"Renamed"}`} {
		code, out := a.call(http.MethodPatch, issue, b)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Equal(t, "version_required", problem(out))
		assert.Equal(t, "version", out["field"])
	}
	got, _ = a.labels(issue)
	assert.Equal(t, []string{"api", "bug"}, got, "a request with no version wrote")

	// Stale: neither the labels nor anything else in the request is written.
	code, out := a.call(http.MethodPatch, issue, `{"labels":["api"],"version":"stale","title":"Renamed"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", problem(out))
	got, _ = a.labels(issue)
	assert.Equal(t, []string{"api", "bug"}, got, "a stale version wrote")
	_, detail := a.call(http.MethodGet, issue, "")
	assert.Equal(t, "Fix it", detail["data"].(map[string]any)["title"], "a refused request changed the title")

	// Changing anything else needs no version, and doesn't move it.
	code, out = a.call(http.MethodPatch, issue, `{"priority":"high"}`)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, read, out["version"], "a change that isn't the labels moved their version")
	got, _ = a.labels(issue)
	assert.Equal(t, []string{"api", "bug"}, got)

	code, out = a.call(http.MethodPatch, issue, labelsBody(read, "api", "new"))
	require.Equal(t, http.StatusOK, code, out)
	saved, _ := out["version"].(string)
	got, reread := a.labels(issue)
	assert.Equal(t, []string{"api", "new"}, got)
	assert.NotEqual(t, read, saved, "the replace changed the labels but not the version")
	assert.Equal(t, reread, saved, "the PATCH's version is not the next GET's")

	code, out = a.call(http.MethodPatch, issue, labelsBody(read, "api"))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", problem(out))

	// A first label on an issue with none: its empty read has a version.
	bare := a.newIssue()
	_, empty := a.labels(bare)
	require.NotEmpty(t, empty)
	code, _ = a.call(http.MethodPatch, bare, labelsBody(empty, "first"))
	assert.Equal(t, http.StatusOK, code)
}

func TestConcurrentLabelReplacesOneWins(t *testing.T) {
	a := newIssueAPI(t)
	issue := a.newIssue()
	for round := range 25 {
		_, version := a.labels(issue)
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := [2]int{}
		names := [2]string{fmt.Sprintf("r%d-a", round), fmt.Sprintf("r%d-b", round)}
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = a.call(http.MethodPatch, issue, labelsBody(version, names[i]))
			}()
		}
		close(start)
		wg.Wait()
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes[:], "round %d", round)
		winner := names[0]
		if codes[1] == http.StatusOK {
			winner = names[1]
		}
		got, _ := a.labels(issue)
		require.Equal(t, []string{winner}, got, "round %d: the 409 wrote", round)
	}
}

// settings is the project's hidden types and its read's version.
func (a *issueAPI) settings() ([]string, string) {
	a.t.Helper()
	code, out := a.call(http.MethodGet, a.project+"/issue-settings", "")
	require.Equal(a.t, http.StatusOK, code)
	version, _ := out["version"].(string)
	return strs(out["data"].(map[string]any)["hidden_type_ids"]), version
}

func (a *issueAPI) typeIDs() []string {
	a.t.Helper()
	code, out := a.call(http.MethodGet, "/api/v1/workspace/issue-types", "")
	require.Equal(a.t, http.StatusOK, code)
	ids := []string{}
	for _, t := range out["data"].([]any) {
		if m := t.(map[string]any); m["kind"] == "standard" {
			ids = append(ids, m["id"].(string))
		}
	}
	slices.Sort(ids)
	require.GreaterOrEqual(a.t, len(ids), 3, "the default types")
	return ids
}

func hiddenBody(version string, ids ...string) string {
	if ids == nil {
		ids = []string{}
	}
	m := map[string]any{"hidden_type_ids": ids}
	if version != "" {
		m["version"] = version
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestReplaceHiddenTypesNeedsTheVersionItRead(t *testing.T) {
	a := newIssueAPI(t)
	types := a.typeIDs()
	settings := a.project + "/issue-settings"

	got, empty := a.settings()
	require.Empty(t, got)
	require.NotEmpty(t, empty)
	code, out := a.call(http.MethodPatch, settings, hiddenBody(empty, types[0]))
	require.Equal(t, http.StatusOK, code, out)
	saved, _ := out["version"].(string)
	got, read := a.settings()
	assert.Equal(t, []string{types[0]}, got)
	assert.NotEqual(t, empty, read)
	assert.Equal(t, read, saved, "the PATCH's version is not the next GET's")

	code, out = a.call(http.MethodPatch, settings, hiddenBody("", types[1]))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "version_required", problem(out))
	assert.Equal(t, "version", out["field"])

	// Stale: nothing in the request is written, the key included.
	code, out = a.call(http.MethodPatch, settings, `{"key":"NEW","hidden_type_ids":[],"version":"stale"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", problem(out))
	got, _ = a.settings()
	assert.Equal(t, []string{types[0]}, got, "a stale version wrote")
	_, out = a.call(http.MethodGet, settings, "")
	assert.Equal(t, "SHOP", out["data"].(map[string]any)["key"], "a refused save changed the key")

	// A save that leaves the list out needs no version and doesn't touch it.
	code, out = a.call(http.MethodPatch, settings, `{"digits":4}`)
	require.Equal(t, http.StatusOK, code, out)
	got, again := a.settings()
	assert.Equal(t, []string{types[0]}, got)
	assert.Equal(t, read, again)

	code, _ = a.call(http.MethodPatch, settings, hiddenBody(read, types[1]))
	assert.Equal(t, http.StatusOK, code)
	got, _ = a.settings()
	assert.Equal(t, []string{types[1]}, got)
}

func TestConcurrentHiddenTypeReplacesOneWins(t *testing.T) {
	a := newIssueAPI(t)
	types := a.typeIDs()
	for round := range 25 {
		_, version := a.settings()
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := [2]int{}
		// Two types that aren't hidden now: neither save is a no-op, which
		// would leave the version where it was.
		hidden, _ := a.settings()
		others := slices.DeleteFunc(slices.Clone(types), func(t string) bool { return slices.Contains(hidden, t) })
		picks := [2]string{others[0], others[1]}
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = a.call(http.MethodPatch, a.project+"/issue-settings", hiddenBody(version, picks[i]))
			}()
		}
		close(start)
		wg.Wait()
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes[:], "round %d", round)
		winner := picks[0]
		if codes[1] == http.StatusOK {
			winner = picks[1]
		}
		got, _ := a.settings()
		require.Equal(t, []string{winner}, got, "round %d: the 409 wrote", round)
	}
}
