package resources

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
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type oneStack struct{ id string }

func (s oneStack) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (s oneStack) ResourceRef(_ context.Context, id string) (*ServiceRef, error) {
	return &ServiceRef{ID: id, Name: "stack", SourceType: sourceCompose, Compose: inline("services:\n  web:\n    image: nginx\n")}, nil
}
func (s oneStack) ResourceRefsOnServer(context.Context, string, string) ([]ServiceRef, error) {
	return nil, nil
}

type noAudit struct{}

func (noAudit) WriteAuditLog(context.Context, string, string, string, string) {}

type limitsAPI struct {
	t         *testing.T
	db        *pgxpool.Pool
	r         *gin.Engine
	id        string
	read, put string
}

func newLimitsAPI(t *testing.T) *limitsAPI {
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
	q(`INSERT INTO services (environment_id, name, source_type, compose_content) VALUES ($1, 'stack', 'docker-compose', 'services: {}') RETURNING id::text`, &svcID, envID)

	s := NewService(NewPostgresRepository(db), zap.NewNop())
	s.WithLookups(oneStack{svcID}, fakeServers{})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-1")
		c.Set("userID", userID)
		c.Set("memberRole", "owner")
		c.Next()
	})
	NewHandler(s, noAudit{}, zap.NewNop()).RegisterRoutes(g)
	return &limitsAPI{t: t, db: db, r: r, id: svcID,
		read: "/api/v1/services/" + svcID + "/resources", put: "/api/v1/services/" + svcID + "/compose-limits"}
}

func (a *limitsAPI) call(method, path, body string) (int, map[string]any) {
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

// get is the stored limits, compose service to memory, and the read's version.
func (a *limitsAPI) get() (map[string]int, string) {
	a.t.Helper()
	code, out := a.call(http.MethodGet, a.read, "")
	require.Equal(a.t, http.StatusOK, code)
	rows, err := a.db.Query(context.Background(), `SELECT compose_service, memory_mb FROM service_compose_limits WHERE service_id = $1`, a.id)
	require.NoError(a.t, err)
	defer rows.Close()
	limits := map[string]int{}
	for rows.Next() {
		var name string
		var mb int
		require.NoError(a.t, rows.Scan(&name, &mb))
		limits[name] = mb
	}
	version, _ := out["version"].(string)
	return limits, version
}

func limitsBody(version string, limits map[string]int) string {
	set := map[string]any{}
	for name, mb := range limits {
		set[name] = map[string]any{"memory_mb": mb, "cpu_cores": nil}
	}
	m := map[string]any{"limits": set}
	if version != "" {
		m["version"] = version
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func problemCode(out map[string]any) string {
	code, _ := out["code"].(string)
	return code
}

// A replace with no version, or a stale one, writes nothing; the version it
// read replaces; an empty list has its own.
func TestReplaceLimitsNeedsTheVersionItRead(t *testing.T) {
	a := newLimitsAPI(t)
	first := map[string]int{"web": 128, "db": 256}

	got, empty := a.get()
	require.Empty(t, got)
	require.NotEmpty(t, empty, "an empty list has a version")
	code, out := a.call(http.MethodPut, a.put, limitsBody(empty, first))
	require.Equal(t, http.StatusOK, code, out)
	saved, _ := out["version"].(string)
	got, read := a.get()
	assert.Equal(t, first, got)
	assert.NotEqual(t, empty, read)
	assert.Equal(t, read, saved, "the PUT's version is not the next GET's")

	// The memory_mb of a row is what its version follows, not only the names.
	code, out = a.call(http.MethodPut, a.put, limitsBody(read, map[string]int{"web": 129, "db": 256}))
	require.Equal(t, http.StatusOK, code, out)
	_, read2 := a.get()
	assert.NotEqual(t, read, read2, "changing a limit didn't move the version")

	for _, b := range []string{limitsBody("", map[string]int{"web": 128}), `{"limits":{}}`} {
		code, out = a.call(http.MethodPut, a.put, b)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Equal(t, "version_required", problemCode(out))
		assert.Equal(t, "version", out["field"])
	}
	got, _ = a.get()
	assert.Equal(t, map[string]int{"web": 129, "db": 256}, got, "a request with no version wrote")

	code, out = a.call(http.MethodPut, a.put, limitsBody(read, map[string]int{"web": 128}))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "changed_since_read", problemCode(out))
	got, _ = a.get()
	assert.Equal(t, map[string]int{"web": 129, "db": 256}, got, "a stale version wrote")

	code, _ = a.call(http.MethodPut, a.put, limitsBody(read2, map[string]int{}))
	assert.Equal(t, http.StatusOK, code)
	got, _ = a.get()
	assert.Empty(t, got)
}

// A default limit being added when the replace starts is one it hasn't
// seen: the replace waits for it through the service's lock, sees it, and
// refuses.
func TestReplaceLimitsWaitsForADefaultBeingAdded(t *testing.T) {
	a := newLimitsAPI(t)
	ctx := context.Background()
	_, version := a.get()

	tx, err := a.db.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `INSERT INTO service_compose_limits (service_id, compose_service, memory_mb) VALUES ($1, 'late', 128)`, a.id)
	require.NoError(t, err)

	done := make(chan int, 1)
	go func() {
		code, _ := a.call(http.MethodPut, a.put, limitsBody(version, map[string]int{"web": 128}))
		done <- code
	}()
	select {
	case code := <-done:
		t.Fatalf("the replace didn't wait for the limit being added: %d", code)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx))

	assert.Equal(t, http.StatusConflict, <-done)
	got, _ := a.get()
	assert.Equal(t, map[string]int{"late": 128}, got, "nothing was to be written")
}

// Two replaces with the same version: exactly one wins, the other is 409 and
// changes nothing.
func TestConcurrentLimitReplacesOneWins(t *testing.T) {
	a := newLimitsAPI(t)
	for round := range 25 {
		_, version := a.get()
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := [2]int{}
		names := [2]string{fmt.Sprintf("r%d-0", round), fmt.Sprintf("r%d-1", round)}
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = a.call(http.MethodPut, a.put, limitsBody(version, map[string]int{names[i]: 128}))
			}()
		}
		close(start)
		wg.Wait()
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes[:], "round %d", round)
		winner := names[0]
		if codes[1] == http.StatusOK {
			winner = names[1]
		}
		got, _ := a.get()
		require.Equal(t, map[string]int{winner: 128}, got, "round %d: the 409 wrote", round)
	}
}
