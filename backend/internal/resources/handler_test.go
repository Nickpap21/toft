package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeServices are the workspaces' services: org-a's, and one of org-b.
type fakeServices struct {
	refs map[string]ServiceRef
	org  map[string]string   // service ID → workspace
	on   map[string][]string // server ID → service IDs, by name
}

func (f *fakeServices) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return f.org[id] == orgID, nil
}

func (f *fakeServices) ResourceRef(_ context.Context, id string) (*ServiceRef, error) {
	ref, ok := f.refs[id]
	if !ok {
		return nil, errors.New("no such service")
	}
	return &ref, nil
}

func (f *fakeServices) ResourceRefsOnServer(_ context.Context, _, serverID string) ([]ServiceRef, error) {
	out := make([]ServiceRef, 0, len(f.on[serverID]))
	for _, id := range f.on[serverID] {
		out = append(out, f.refs[id])
	}
	return out, nil
}

type fakeServers map[string]string // server ID → workspace

func (f fakeServers) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return f[id] == orgID, nil
}

type fakeAudit struct{ entries []string }

func (f *fakeAudit) WriteAuditLog(_ context.Context, orgID, actorID, action, resource string) {
	f.entries = append(f.entries, strings.Join([]string{orgID, actorID, action, resource}, " "))
}

// apiNow is the handler tests' clock: yesterday is 18 Oct, the last week
// starts on 12 Oct.
var apiNow = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

// week is 7 full days (12 to 18 Oct) at peak MiB and p95 percent of a core.
func week(peak, p95 float64) []DailyUsage {
	out := make([]DailyUsage, 0, 7)
	for ago := 7; ago >= 1; ago-- {
		out = append(out, DailyUsage{Day: time.Date(2026, 10, 19-ago, 0, 0, 0, 0, time.UTC), Samples: 1440, MemoryPeakMB: peak, CPUP95: p95})
	}
	return out
}

type harness struct {
	repo  *fakeRepo
	audit *fakeAudit
	r     *gin.Engine
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	grace := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	webDays := week(200, 12)
	wait := 3.1
	webDays[6].CPUWaitAvg = &wait // yesterday
	repo := &fakeRepo{
		graceUntil: &grace,
		usageBy: map[string]map[string][]DailyUsage{
			"svc-web":   {"": webDays},
			"svc-cache": {"redis": week(250, 5)},
		},
		panelBy:    map[string]map[string]ComposeLimit{"svc-cache": {"redis": {MemoryMB: 256}}},
		kills:      map[string]int{"": 1},
		serverWait: map[string]float64{"srv-1 2026-10-18": 12.4, "srv-1 2026-10-19": 50},
	}
	s := settledService(t, repo, apiNow)
	s.WithLookups(&fakeServices{
		refs: map[string]ServiceRef{
			"svc-web":   {ID: "svc-web", Name: "web", SourceType: "docker-image", DockerImage: "nginx", MemoryLimit: "1g"},
			"svc-stack": {ID: "svc-stack", Name: "stack", SourceType: sourceCompose, Compose: inline("services:\n  web:\n    image: nginx\n    mem_limit: 512m\n  db:\n    image: postgres:16\n")},
			"svc-cache": {ID: "svc-cache", Name: "cache", SourceType: sourceCompose}, // from a repository: the file is unknown
			"svc-repo":  {ID: "svc-repo", Name: "repo", SourceType: sourceCompose},
			"svc-other": {ID: "svc-other", Name: "other", SourceType: sourceCompose},
		},
		org: map[string]string{"svc-web": "org-a", "svc-stack": "org-a", "svc-cache": "org-a", "svc-repo": "org-a", "svc-other": "org-b"},
		on:  map[string][]string{"srv-1": {"svc-cache", "svc-stack", "svc-web"}},
	}, fakeServers{"srv-1": "org-a", "srv-empty": "org-a", "srv-b": "org-b"})
	audit := &fakeAudit{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-a")
		c.Set("userID", "user-1")
		c.Set("memberRole", c.GetHeader("X-Role"))
		c.Next()
	})
	NewHandler(s, audit, zap.NewNop()).RegisterRoutes(g)
	return &harness{repo: repo, audit: audit, r: r}
}

// noVersion is a /resources body without its version; replace_version_test
// covers that.
func noVersion(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &m), body)
	require.NotEmpty(t, m["version"], body)
	delete(m, "version")
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

func (h *harness) do(role, method, path, body string) (int, string) {
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Role", role)
	w := httptest.NewRecorder()
	h.r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// latency_24h as the contract has it: measured, and not measured.
func TestServiceResources_Latency(t *testing.T) {
	h := newHarness(t)
	first := apiNow.Add(-2 * time.Hour)
	h.repo.latency = LatencyWindow{Minutes: 120, FirstMeasured: &first, Buckets: []int64{0, 0, 400, 500, 100, 0, 0, 0, 0, 0, 0, 0}}
	code, body := h.do("viewer", http.MethodGet, "/services/svc-web/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	var raw any
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	apispec.Check(t, http.MethodGet, "/api/v1/services/svc-web/resources", code, raw)
	assert.Contains(t, body, `"latency_24h":{"requests":1000,"p95_ms":75,"p95_bucket":{"low_ms":50,"high_ms":100},"since":"2026-10-19T08:00:00Z","unmeasured":null}`)

	h.repo.latency = LatencyWindow{Minutes: 5, NewestUnmeasured: "agent"}
	code, body = h.do("viewer", http.MethodGet, "/services/svc-web/resources", "")
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	apispec.Check(t, http.MethodGet, "/api/v1/services/svc-web/resources", code, raw)
	assert.Contains(t, body, `"latency_24h":{"requests":0,"p95_ms":null,"p95_bucket":null,"since":null,"unmeasured":"agent"}`)
}

const nullCPU = `"cpu":{"limit":"","p95_cores":null,"throttled_max_pct":null,"wait_avg_pct":null,"suggestion":null}`

const noRestarts = `"restarts_24h":null,"restarts_since":null`

// A row's restarts are its compose service's (a single container's under
// ""): a count with "since" while the counted samples start inside the 24
// hours, without once they cover it, none without a counted sample. One
// query for the service, from 24 hours before now.
func TestServiceResources_Restarts(t *testing.T) {
	h := newHarness(t)
	first := apiNow.Add(-3 * time.Hour)
	h.repo.restartsBy = map[string]map[string]Restarts{
		"svc-stack": {
			"db":   {Samples: 1440, Restarts: 2, FirstAt: apiNow.Add(-24 * time.Hour), Earlier: true},
			"web":  {Samples: 180, Restarts: 0, FirstAt: first},
			"gone": {Samples: 10, Restarts: 9, FirstAt: first}, // no longer in the file: no row
		},
		"svc-web": {"": {Samples: 60, Restarts: 1, FirstAt: first}},
	}
	code, body := h.do("viewer", http.MethodGet, "/services/svc-stack/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	var got struct {
		Data ServiceResources `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	var raw any
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	apispec.Check(t, http.MethodGet, "/api/v1/services/svc-stack/resources", code, raw)
	require.Len(t, got.Data.Rows, 2)
	db, web := got.Data.Rows[0], got.Data.Rows[1]
	assert.Equal(t, []string{"db", "web"}, []string{db.ComposeService, web.ComposeService})
	assert.Equal(t, 2, *db.Restarts24h)
	assert.Nil(t, db.RestartsSince, "the samples cover the 24 hours")
	assert.Equal(t, 0, *web.Restarts24h)
	assert.True(t, first.Equal(*web.RestartsSince))
	assert.Equal(t, apiNow.Add(-24*time.Hour), h.repo.restartsSince)

	_, body = h.do("viewer", http.MethodGet, "/services/svc-web/resources", "")
	assert.Contains(t, body, `"restarts_24h":1,"restarts_since":"2026-10-19T07:00:00Z"`)

	h.repo.restartsErr = errors.New("db down")
	code, body = h.do("viewer", http.MethodGet, "/services/svc-web/resources", "")
	require.Equal(t, http.StatusOK, code, "the limits and usage still answer")
	assert.Contains(t, body, noRestarts)
}

func TestServiceResources_Roles(t *testing.T) {
	h := newHarness(t)
	limits := `{"version":"v","limits":{"db":{"memory_mb":1024,"cpu_cores":null}}}`

	code, _ := h.do("viewer", http.MethodGet, "/services/svc-web/resources", "")
	assert.Equal(t, http.StatusOK, code, "viewers read")
	code, _ = h.do("viewer", http.MethodPut, "/services/svc-stack/compose-limits", limits)
	assert.Equal(t, http.StatusForbidden, code, "viewers never write")
	code, _ = h.do("developer", http.MethodPut, "/services/svc-stack/compose-limits", limits)
	assert.Equal(t, http.StatusOK, code, "developers set limits")

	code, _ = h.do("owner", http.MethodGet, "/services/svc-other/resources", "")
	assert.Equal(t, http.StatusNotFound, code, "another workspace's service is not found")
	code, _ = h.do("owner", http.MethodPut, "/services/svc-other/compose-limits", limits)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = h.do("owner", http.MethodGet, "/services/no-such/resources", "")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestComposeLimits_Invalid(t *testing.T) {
	h := newHarness(t)
	entries := make([]string, 0, 101)
	for i := range 101 {
		entries = append(entries, fmt.Sprintf(`"s%d":{"memory_mb":128}`, i))
	}
	over100 := `{"limits":{` + strings.Join(entries, ",") + `}}`
	for name, c := range map[string]struct{ path, body string }{
		"not JSON":              {"svc-stack", `{"limits":`},
		"memory not a number":   {"svc-stack", `{"limits":{"db":{"memory_mb":"lots"}}}`},
		"limits missing":        {"svc-stack", `{}`},
		"memory under 64":       {"svc-stack", `{"limits":{"db":{"memory_mb":63}}}`},
		"memory missing":        {"svc-stack", `{"limits":{"db":{"cpu_cores":1}}}`},
		"memory over 1 PiB":     {"svc-stack", `{"limits":{"db":{"memory_mb":1073741825}}}`},
		"name with a space":     {"svc-stack", `{"limits":{"my db":{"memory_mb":128}}}`},
		"name starting with -":  {"svc-stack", `{"limits":{"-db":{"memory_mb":128}}}`},
		"cpu 0":                 {"svc-stack", `{"limits":{"db":{"memory_mb":128,"cpu_cores":0}}}`},
		"cpu negative":          {"svc-stack", `{"limits":{"db":{"memory_mb":128,"cpu_cores":-1}}}`},
		"cpu over 64":           {"svc-stack", `{"limits":{"db":{"memory_mb":128,"cpu_cores":64.5}}}`},
		"not a compose service": {"svc-web", `{"limits":{"db":{"memory_mb":128}}}`},
		"over 100 entries":      {"svc-stack", over100},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := h.do("owner", http.MethodPut, "/services/"+c.path+"/compose-limits", strings.Replace(c.body, "{", `{"version":"v",`, 1))
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Contains(t, body, `"error":"`)
		})
	}
	assert.NotContains(t, h.repo.panelBy, "svc-stack", "nothing stored")
	assert.Empty(t, h.audit.entries, "nothing audited")
}

func TestComposeLimits_PutThenGet(t *testing.T) {
	h := newHarness(t)
	fileOnly := `{"grace_until":"2026-10-26T12:00:00Z","latency_24h":null,"rows":[
		{"compose_service":"db","kind":"database",
		 "memory":{"limit":"","source":"none","file_limit":"","peak_14d_mb":null,"days":0,"oom_kills_7d":0,"suggestion":null},` + nullCPU + `,` + noRestarts + `},
		{"compose_service":"web","kind":"app",
		 "memory":{"limit":"512m","source":"compose_file","file_limit":"512m","peak_14d_mb":null,"days":0,"oom_kills_7d":0,"suggestion":null},` + nullCPU + `,` + noRestarts + `}]}`
	// In grace (until 26 Oct): db has no limit and no usage, so it is being
	// measured, not offered its kind's default.
	code, body := h.do("viewer", http.MethodGet, "/services/svc-stack/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"data":`+fileOnly+`}`, noVersion(t, body))

	code, put := h.do("developer", http.MethodPut, "/services/svc-stack/compose-limits",
		`{"version":"v","limits":{"db":{"memory_mb":1024,"cpu_cores":1.5},"web":{"memory_mb":256,"cpu_cores":null}}}`)
	require.Equal(t, http.StatusOK, code, put)
	panel := `{"grace_until":"2026-10-26T12:00:00Z","latency_24h":null,"rows":[
		{"compose_service":"db","kind":"database",
		 "memory":{"limit":"1g","source":"panel","file_limit":"","peak_14d_mb":null,"days":0,"oom_kills_7d":0,"suggestion":null},
		 "cpu":{"limit":"1.5","p95_cores":null,"throttled_max_pct":null,"wait_avg_pct":null,"suggestion":null},` + noRestarts + `},
		{"compose_service":"web","kind":"app",
		 "memory":{"limit":"256m","source":"panel","file_limit":"512m","peak_14d_mb":null,"days":0,"oom_kills_7d":0,"suggestion":null},` + nullCPU + `,` + noRestarts + `}]}`
	assert.JSONEq(t, `{"data":`+panel+`}`, noVersion(t, put), "the PUT answers as the GET does")
	_, body = h.do("viewer", http.MethodGet, "/services/svc-stack/resources", "")
	assert.JSONEq(t, `{"data":`+panel+`}`, noVersion(t, body))
	assert.Equal(t, []string{"org-a user-1 service.compose_limits.updated service:svc-stack"}, h.audit.entries)

	code, _ = h.do("owner", http.MethodPut, "/services/svc-stack/compose-limits", `{"version":"v","limits":{}}`)
	require.Equal(t, http.StatusOK, code)
	_, body = h.do("owner", http.MethodGet, "/services/svc-stack/resources", "")
	assert.JSONEq(t, `{"data":`+fileOnly+`}`, noVersion(t, body), "an empty set hands control back to the file")
}

func TestServiceResources_Single(t *testing.T) {
	h := newHarness(t)
	code, body := h.do("viewer", http.MethodGet, "/services/svc-web/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"data":{"grace_until":"2026-10-26T12:00:00Z","latency_24h":null,"rows":[{"compose_service":"","kind":"app",
		"memory":{"limit":"1g","source":"panel","file_limit":"","peak_14d_mb":200,"days":7,"oom_kills_7d":1,
		          "suggestion":{"direction":"down","value":"320m","reason":"Peak 200m of 1g over 7 days","days":7}},
		"cpu":{"limit":"","p95_cores":0.12,"throttled_max_pct":null,"wait_avg_pct":3.1,"suggestion":null},`+noRestarts+`}]}}`, noVersion(t, body))
	assert.Equal(t, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), h.repo.killsSince, "kills of the suggestion's week")
	assert.Equal(t, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), h.repo.since, "14 days of usage, today included")
}

func TestServiceResources_Empty(t *testing.T) {
	h := newHarness(t)
	code, body := h.do("owner", http.MethodGet, "/services/svc-repo/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"data":{"grace_until":"2026-10-26T12:00:00Z","latency_24h":null,"rows":[]}}`, noVersion(t, body))
}

func TestServerResources(t *testing.T) {
	h := newHarness(t)
	code, body := h.do("viewer", http.MethodGet, "/servers/srv-1/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"data":{"services":[
		{"service_id":"svc-cache","service_name":"cache","compose_service":"redis",
		 "suggestion":{"direction":"up","value":"384m","reason":"Peak 250m, over 90% of the limit","days":7}},
		{"service_id":"svc-web","service_name":"web","compose_service":"",
		 "suggestion":{"direction":"down","value":"320m","reason":"Peak 200m of 1g over 7 days","days":7}}],
		"frees_mb":704,
		"cpu_wait":{"day":"2026-10-18","avg_pct":12.4}}}`, body, "frees 1g − 320m: only down suggestions free memory; the server page offers no kind defaults")

	code, body = h.do("owner", http.MethodGet, "/servers/srv-empty/resources", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"data":{"services":[],"frees_mb":0,"cpu_wait":null}}`, body)

	code, _ = h.do("owner", http.MethodGet, "/servers/srv-b/resources", "")
	assert.Equal(t, http.StatusNotFound, code, "as the server's other routes")
}

func TestDefaultLimit(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string]string{
		"/resources/default-limit":                                  `{"kind":"app","memory_mb":512}`,
		"/resources/default-limit?addon_slug=redis":                 `{"kind":"cache","memory_mb":256}`,
		"/resources/default-limit?image=ghcr.io/acme/postgres:16.4": `{"kind":"database","memory_mb":1024}`,
		"/resources/default-limit?image=dpage/pgadmin4:8":           `{"kind":"tool","memory_mb":512}`,
	} {
		code, body := h.do("viewer", http.MethodGet, path, "")
		assert.Equal(t, http.StatusOK, code, path)
		assert.JSONEq(t, `{"data":`+want+`}`, body, path)
	}
}
