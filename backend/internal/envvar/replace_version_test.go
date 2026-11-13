package envvar_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type ownsAll struct{}

func (ownsAll) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }

// replaceAPI is the real handler, service and repository over a test
// database, with one service and one environment to replace.
type replaceAPI struct {
	t      *testing.T
	db     *pgxpool.Pool
	r      *gin.Engine
	svc    *envvar.Service
	scopes []replaceScope
}

// replaceScope is one owner of variables, and how to add a child row outside
// the API.
type replaceScope struct {
	name, id, prefix string
	insert           string
}

func newReplaceAPI(t *testing.T) *replaceAPI {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID, svcID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'web', 'docker-image', 'web:1') RETURNING id::text`, &svcID, envID)

	svc := envvar.NewService(envvar.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", "")), zap.NewNop())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-1")
		c.Set("userID", userID)
		c.Set("memberRole", "owner")
		c.Next()
	})
	envvar.NewHandler(svc, ownsAll{}, ownsAll{}, nil, zap.NewNop()).RegisterRoutes(g)
	return &replaceAPI{t: t, db: db, r: r, svc: svc, scopes: []replaceScope{
		{"service", svcID, "/api/v1/services/" + svcID, `INSERT INTO env_vars (service_id, key, value) VALUES ($1, $2, 'x')`},
		{"environment", envID, "/api/v1/environments/" + envID, `INSERT INTO environment_env_vars (environment_id, key, value) VALUES ($1, $2, 'x')`},
	}}
}

// call sends one request and checks the answer against the spec.
func (a *replaceAPI) call(method, path, body string) (int, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	if w.Body.Len() == 0 {
		apispec.Check(a.t, method, path, w.Code, nil)
		return w.Code, nil
	}
	var out map[string]any
	require.NoError(a.t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	apispec.Check(a.t, method, path, w.Code, out)
	return w.Code, out
}

// read is the scope's plain variables, key to value, and their version.
func (a *replaceAPI) read(sc replaceScope) (map[string]string, string) {
	a.t.Helper()
	code, out := a.call(http.MethodGet, sc.prefix+"/env-vars", "")
	require.Equal(a.t, http.StatusOK, code)
	vars := map[string]string{}
	for _, v := range out["data"].([]any) {
		m := v.(map[string]any)
		vars[m["key"].(string)] = m["value"].(string)
	}
	version, _ := out["version"].(string)
	return vars, version
}

func body(version string, vars map[string]string) string {
	list := make([]map[string]string, 0, len(vars))
	for k, v := range vars {
		list = append(list, map[string]string{"key": k, "value": v})
	}
	m := map[string]any{"vars": list}
	if version != "" {
		m["version"] = version
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (a *replaceAPI) post(sc replaceScope, key, value string) {
	a.t.Helper()
	code, _ := a.call(http.MethodPost, sc.prefix+"/env-vars", fmt.Sprintf(`{"key":%q,"value":%q}`, key, value))
	require.Equal(a.t, http.StatusOK, code)
}

func codeOf(out map[string]any) string {
	code, _ := out["code"].(string)
	return code
}

// A replace with no version, or a stale one, writes nothing; the version it
// read replaces, and the version the PUT answers with needs no re-read.
func TestReplaceNeedsTheVersionItRead(t *testing.T) {
	a := newReplaceAPI(t)
	for _, sc := range a.scopes {
		t.Run(sc.name, func(t *testing.T) {
			a.t = t
			a.post(sc, "KEEP", "1")
			a.post(sc, "DROP", "2")
			code, _ := a.call(http.MethodPost, sc.prefix+"/secrets", `{"key":"TOKEN","value":"s3cret"}`)
			require.Equal(t, http.StatusCreated, code)
			want := map[string]string{"KEEP": "1", "DROP": "2"}

			code, out := a.call(http.MethodPut, sc.prefix+"/env-vars", body("", map[string]string{"KEEP": "1"}))
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Equal(t, "version_required", codeOf(out))
			got, read := a.read(sc)
			assert.Equal(t, want, got, "a request with no version wrote")

			code, out = a.call(http.MethodPut, sc.prefix+"/env-vars", body("stale", map[string]string{"KEEP": "1"}))
			assert.Equal(t, http.StatusConflict, code)
			assert.Equal(t, "changed_since_read", codeOf(out))
			got, _ = a.read(sc)
			assert.Equal(t, want, got, "a stale version wrote")

			code, out = a.call(http.MethodPut, sc.prefix+"/env-vars", body(read, map[string]string{"KEEP": "1", "NEW": "3"}))
			require.Equal(t, http.StatusOK, code)
			saved, _ := out["version"].(string)
			got, reread := a.read(sc)
			assert.Equal(t, map[string]string{"KEEP": "1", "NEW": "3"}, got)
			assert.NotEqual(t, read, saved, "the replace changed the list but not the version")
			assert.Equal(t, reread, saved, "the PUT's version is not the next GET's")

			// A second save needs no re-read; the first read is stale now.
			code, _ = a.call(http.MethodPut, sc.prefix+"/env-vars", body(saved, map[string]string{"KEEP": "5"}))
			assert.Equal(t, http.StatusOK, code)
			code, out = a.call(http.MethodPut, sc.prefix+"/env-vars", body(read, map[string]string{"KEEP": "6"}))
			assert.Equal(t, http.StatusConflict, code)
			assert.Equal(t, "changed_since_read", codeOf(out))
			got, _ = a.read(sc)
			assert.Equal(t, map[string]string{"KEEP": "5"}, got)

			code, secrets := a.call(http.MethodGet, sc.prefix+"/secrets", "")
			require.Equal(t, http.StatusOK, code)
			assert.Len(t, secrets["data"], 1, "a replace touched a secret")
		})
	}
}

// A successful read of an empty list has a version, so a first save works;
// a client whose read failed has none to send.
func TestAnEmptyListHasAVersion(t *testing.T) {
	a := newReplaceAPI(t)
	for _, sc := range a.scopes {
		t.Run(sc.name, func(t *testing.T) {
			a.t = t
			got, version := a.read(sc)
			require.Empty(t, got)
			require.NotEmpty(t, version)
			code, _ := a.call(http.MethodPut, sc.prefix+"/env-vars", body(version, map[string]string{"FIRST": "1"}))
			assert.Equal(t, http.StatusOK, code)
			got, _ = a.read(sc)
			assert.Equal(t, map[string]string{"FIRST": "1"}, got)
		})
	}
}

// The version doesn't move on a re-read or a write that changes nothing, and
// moves on any write that does. Secrets are out of it.
func TestTheVersionFollowsThePlainRows(t *testing.T) {
	a := newReplaceAPI(t)
	for _, sc := range a.scopes {
		t.Run(sc.name, func(t *testing.T) {
			a.t = t
			version := func() string {
				_, v := a.read(sc)
				require.NotEmpty(t, v)
				return v
			}
			empty := version()
			assert.Equal(t, empty, version(), "a re-read moved the version")

			a.post(sc, "A", "1")
			one := version()
			assert.NotEqual(t, empty, one, "a one-key POST didn't move the version")
			a.post(sc, "A", "1")
			assert.Equal(t, one, version(), "a POST that changed nothing moved the version")
			a.post(sc, "A", "2")
			two := version()
			assert.NotEqual(t, one, two, "changing a value didn't move the version")

			code, _ := a.call(http.MethodPost, sc.prefix+"/secrets", `{"key":"TOKEN","value":"s3cret"}`)
			require.Equal(t, http.StatusCreated, code)
			assert.Equal(t, two, version(), "adding a secret moved the version")
			code, _ = a.call(http.MethodPut, sc.prefix+"/secrets/TOKEN", `{"value":"new"}`)
			require.Equal(t, http.StatusOK, code)
			assert.Equal(t, two, version(), "replacing a secret moved the version")

			code, _ = a.call(http.MethodDelete, sc.prefix+"/env-vars/A", "")
			require.Equal(t, http.StatusNoContent, code)
			assert.NotEqual(t, two, version(), "a delete didn't move the version")
		})
	}
}

// Two replaces with the same version: exactly one wins, the other is 409 and
// changes nothing.
func TestConcurrentReplacesOneWins(t *testing.T) {
	a := newReplaceAPI(t)
	for _, sc := range a.scopes {
		t.Run(sc.name, func(t *testing.T) {
			a.t = t
			a.post(sc, "A", "start")
			for round := range 25 {
				_, version := a.read(sc)
				start := make(chan struct{})
				var wg sync.WaitGroup
				codes := [2]int{}
				values := [2]string{}
				for i := range 2 {
					values[i] = fmt.Sprintf("r%d-%d", round, i)
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						codes[i], _ = a.call(http.MethodPut, sc.prefix+"/env-vars", body(version, map[string]string{"A": values[i]}))
					}()
				}
				close(start)
				wg.Wait()
				require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes[:], "round %d", round)
				winner := values[0]
				if codes[1] == http.StatusOK {
					winner = values[1]
				}
				got, _ := a.read(sc)
				require.Equal(t, map[string]string{"A": winner}, got, "round %d: the 409 wrote", round)
			}
		})
	}
}

// A key being added when the replace starts is one it hasn't seen: the
// replace waits for it through the parent's lock, sees it, and refuses.
func TestReplaceWaitsForAKeyBeingAdded(t *testing.T) {
	a := newReplaceAPI(t)
	ctx := context.Background()
	for _, sc := range a.scopes {
		t.Run(sc.name, func(t *testing.T) {
			a.t = t
			a.post(sc, "A", "1")
			_, version := a.read(sc)

			tx, err := a.db.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx) //nolint:errcheck
			_, err = tx.Exec(ctx, sc.insert, sc.id, "LATE")
			require.NoError(t, err)

			done := make(chan int, 1)
			go func() {
				code, _ := a.call(http.MethodPut, sc.prefix+"/env-vars", body(version, map[string]string{"A": "2"}))
				done <- code
			}()
			select {
			case code := <-done:
				t.Fatalf("the replace didn't wait for the key being added: %d", code)
			case <-time.After(300 * time.Millisecond):
			}
			require.NoError(t, tx.Commit(ctx))

			assert.Equal(t, http.StatusConflict, <-done)
			got, _ := a.read(sc)
			assert.Equal(t, map[string]string{"A": "1", "LATE": "x"}, got, "nothing was to be written")
		})
	}
}

// Seed, what a new service's creation uses, deletes nothing: only a replace
// with the version it read can.
func TestSeedDeletesNothing(t *testing.T) {
	a := newReplaceAPI(t)
	sc := a.scopes[0]
	a.post(sc, "OLD", "1")
	require.NoError(t, a.svc.Seed(context.Background(), sc.id, "", []envvar.UpsertInput{{Key: "NEW", Value: "2"}}))
	got, _ := a.read(sc)
	assert.Equal(t, map[string]string{"OLD": "1", "NEW": "2"}, got)

	err := a.svc.Seed(context.Background(), sc.id, "", []envvar.UpsertInput{{Key: "1BAD", Value: "x"}})
	assert.ErrorIs(t, err, envvar.ErrInvalidKey)
}
