package issue

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func d(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

// span is a cycle as "number from to".
func span(c StoredCycle) string {
	return strings.Join([]string{itoa0(c.Number), fmtDate(c.StartsOn), fmtDate(c.EndsOn)}, " ")
}

func spans(cs []StoredCycle) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, span(c))
	}
	return out
}

// TestPlanCycles: the first cycle starts on this week's start day; two wait
// after it; an ended cycle closes; a new schedule redraws only what hasn't
// started; after a pause the next cycle starts this week, after the last.
func TestPlanCycles(t *testing.T) {
	mon2 := Schedule{Weeks: 2, StartDay: time.Monday}
	eq := func(name string, got, want []string) {
		t.Helper()
		if strings.Join(got, ", ") != strings.Join(want, ", ") {
			t.Errorf("%s:\n got %v\nwant %v", name, got, want)
		}
	}
	// 2026-10-21 is a Wednesday.
	p := planCycles(mon2, nil, d("2026-10-21"))
	eq("first", spans(p.Add), []string{"1 2026-10-19 2026-11-01", "2 2026-11-02 2026-11-15", "3 2026-11-16 2026-11-29"})
	if p.Current != 1 || len(p.Close) != 0 || len(p.Move) != 0 {
		t.Errorf("first: %+v", p)
	}
	have := append([]StoredCycle{}, p.Add...)
	for i := range have {
		have[i].ID = "c" + itoa0(have[i].Number)
	}
	if again := planCycles(mon2, have, d("2026-10-25")); len(again.Add)+len(again.Move)+len(again.Close) != 0 || again.Current != 1 {
		t.Errorf("nothing to do mid-cycle: %+v", again)
	}
	p = planCycles(mon2, have, d("2026-11-02"))
	eq("ended", spans(p.Close), []string{"1 2026-10-19 2026-11-01"})
	eq("one more", spans(p.Add), []string{"4 2026-11-30 2026-12-13"})
	if p.Current != 2 {
		t.Errorf("current: %d", p.Current)
	}
	// One week from Wednesday: cycle 2 starts after 1 and runs to the day
	// before a Wednesday; 3 starts on one.
	p = planCycles(Schedule{Weeks: 1, StartDay: time.Wednesday}, have, d("2026-10-21"))
	eq("redrawn", spans(p.Move), []string{"2 2026-11-02 2026-11-10", "3 2026-11-11 2026-11-17"})
	// Paused since November: 2026-12-30 is a Wednesday.
	p = planCycles(mon2, have, d("2026-12-30"))
	eq("closed after a pause", spans(p.Close), []string{"1 2026-10-19 2026-11-01", "2 2026-11-02 2026-11-15", "3 2026-11-16 2026-11-29"})
	eq("back", spans(p.Add), []string{"4 2026-12-28 2027-01-10", "5 2027-01-11 2027-01-24", "6 2027-01-25 2027-02-07"})
	if lastStartDay(d("2026-10-19"), time.Monday) != d("2026-10-19") || lastStartDay(d("2026-10-18"), time.Monday) != d("2026-10-12") {
		t.Error("lastStartDay")
	}
}

// TestCyclesAPI works cycles through the API (D-90): turning them on, an
// issue's cycle, dates and estimate with their history and refusals, the
// cycle filter, a cycle's page, a rename, the project's cycles, the
// Timeline, and a cycle ending that carries its unfinished issues over;
// every answer is checked against openapi.yaml.
func TestCyclesAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newFake()
	svc := NewService(repo, fakeCode{}, zap.NewNop())
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) // a Wednesday
	svc.clock = func() time.Time { return now }
	router := func(user, role string) http.Handler {
		r := gin.New()
		NewHandler(svc, projectsIn{"p1": "o1", "p2": "o1"}, zap.NewNop()).RegisterRoutes(r.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", user)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return r
	}
	dev, own, viewer := router("dev", "developer"), router("own", "owner"), router("eve", "viewer")
	call := func(h http.Handler, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, req)
		out := map[string]any{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body)
		}
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		return w.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }
	list := func(m map[string]any) []any { l, _ := m["data"].([]any); return l }
	base := "/api/v1/projects/p1"

	// Off: no cycle to plan into, no estimates.
	call(dev, "POST", base+"/issues", map[string]any{"title": "Retry failed order emails"})
	if code, out := call(dev, "PATCH", base+"/issues/1", map[string]any{"cycle": 1}); code != 422 || data(out)["code"] != "bad_cycle" {
		t.Errorf("a cycle while off: %d %v", code, out)
	}
	if code, out := call(dev, "PATCH", base+"/issues/1", map[string]any{"estimate": 3}); code != 422 || data(out)["code"] != "estimates_off" {
		t.Errorf("an estimate while off: %d %v", code, out)
	}
	on := map[string]any{"cycles": map[string]any{"enabled": true, "weeks": 2, "start_day": 1, "time_zone": "Europe/Athens"}, "estimates": "fibonacci"}
	if code, _ := call(dev, "PATCH", "/api/v1/workspace/issue-settings", on); code != 403 {
		t.Errorf("a developer changed the schedule: %d", code)
	}
	for _, bad := range []map[string]any{
		{"cycles": map[string]any{"enabled": true, "weeks": 5, "start_day": 1, "time_zone": "UTC"}},
		{"cycles": map[string]any{"enabled": true, "weeks": 2, "start_day": 1, "time_zone": "Mars/Olympus"}},
		{"estimates": "days"},
	} {
		if code, _ := call(own, "PATCH", "/api/v1/workspace/issue-settings", bad); code != 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	code, out := call(own, "PATCH", "/api/v1/workspace/issue-settings", on)
	if c := data(out)["cycles"].(map[string]any); code != 200 || c["enabled"] != true || c["time_zone"] != "Europe/Athens" || data(out)["estimates"] != "fibonacci" {
		t.Fatalf("turn on: %d %v", code, out)
	}
	_, out = call(viewer, "GET", "/api/v1/workspace/cycles", nil)
	if cs := list(out); len(cs) != 3 || cs[2].(map[string]any)["state"] != "current" || cs[2].(map[string]any)["starts_on"] != "2026-10-19" ||
		cs[2].(map[string]any)["days_left"] != float64(11) {
		t.Fatalf("cycles: %v", out)
	}
	if _, out = call(viewer, "GET", "/api/v1/workspace/cycles?state=upcoming&limit=1", nil); len(list(out)) != 1 || out["next"] != "3" {
		t.Errorf("a page of upcoming ones: %v", out)
	}

	// An issue's cycle, dates and estimate, each a history row.
	code, out = call(dev, "PATCH", base+"/issues/1", map[string]any{"cycle": 1, "due_on": "2026-10-24", "start_on": "2026-10-20", "estimate": 5})
	if c, _ := data(out)["cycle"].(map[string]any); code != 200 || c["number"] != float64(1) || c["state"] != "current" ||
		data(out)["due_on"] != "2026-10-24" || data(out)["estimate"] != float64(5) {
		t.Fatalf("plan: %d %v", code, out)
	}
	for _, bad := range []map[string]any{{"estimate": 4}, {"due_on": "24/10/2026"}, {"start_on": "2026-10-30"}, {"cycle": 9}} {
		if code, _ := call(dev, "PATCH", base+"/issues/1", bad); code != 400 && code != 422 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	kinds := map[string]bool{}
	for _, e := range repo.events {
		kinds[e.Kind+" "+e.From+">"+e.To] = true
	}
	for _, k := range []string{"cycle >1", "due >2026-10-24", "start >2026-10-20", "estimate >5"} {
		if !kinds[k] {
			t.Errorf("no history row %q in %v", k, kinds)
		}
	}
	// An epic is never in a cycle; its estimate is its children's.
	_, out = call(own, "GET", "/api/v1/workspace/issue-types", nil)
	epic := ""
	for _, ty := range list(out) {
		if ty.(map[string]any)["name"] == "Epic" {
			epic = ty.(map[string]any)["id"].(string)
		}
	}
	if code, out := call(dev, "POST", base+"/issues", map[string]any{"title": "Checkout v2", "type_id": epic, "cycle": 1}); code != 422 || data(out)["code"] != "container_not_in_cycle" {
		t.Errorf("an epic in a cycle: %d %v", code, out)
	}
	call(dev, "POST", base+"/issues", map[string]any{"title": "Checkout v2", "type_id": epic, "start_on": "2026-10-19", "due_on": "2026-11-06"})
	call(dev, "PATCH", base+"/issues/1", map[string]any{"parent": "STO-2"})
	if _, out = call(dev, "GET", base+"/issues/2", nil); data(out)["estimate"] != float64(5) || len(data(out)["cycles"].([]any)) != 1 {
		t.Errorf("an epic's estimate and cycles: %v", out)
	}
	// A sub-task takes its parent's cycle.
	call(dev, "POST", base+"/issues", map[string]any{"title": "Templates", "parent": "STO-1"})
	if _, out = call(dev, "GET", base+"/issues/3", nil); data(out)["cycle"] == nil {
		t.Errorf("a sub-issue's cycle: %v", out)
	}

	// The cycle filter and the due order.
	if _, out = call(viewer, "GET", base+"/issues?cycle=current", nil); len(list(out)) != 2 {
		t.Errorf("current cycle: %v", out)
	}
	if _, out = call(viewer, "GET", base+"/issues?cycle=none", nil); len(list(out)) != 1 {
		t.Errorf("no cycle: %v", out)
	}
	if _, out = call(viewer, "GET", base+"/issue-counts?cycle=next", nil); data(out)["backlog"] != float64(0) {
		t.Errorf("next cycle's counts: %v", out)
	}
	if code, _ := call(viewer, "GET", base+"/issues?cycle=soon", nil); code != 400 {
		t.Errorf("a bad cycle filter: %d", code)
	}
	if code, _ := call(viewer, "GET", base+"/issues?order=due", nil); code != 200 {
		t.Errorf("due order: %d", code)
	}

	// A cycle's page, its issues, a rename; the project's cycles.
	call(dev, "PATCH", base+"/issues/3", map[string]any{"status": "shipped"})
	_, out = call(viewer, "GET", "/api/v1/workspace/cycles/1", nil)
	if c := data(out)["counts"].(map[string]any); c["scope"] != float64(2) || c["shipped"] != float64(1) || data(out)["next"] != float64(2) || data(out)["previous"] != nil {
		t.Errorf("cycle 1: %v", out)
	}
	if _, out = call(viewer, "GET", "/api/v1/workspace/cycles/1/issues?project_id=p1", nil); len(list(out)) != 2 {
		t.Errorf("cycle 1's issues: %v", out)
	}
	if code, _ := call(viewer, "PATCH", "/api/v1/workspace/cycles/1", map[string]any{"name": "Checkout"}); code != 403 {
		t.Errorf("a viewer renamed a cycle: %d", code)
	}
	if _, out = call(dev, "PATCH", "/api/v1/workspace/cycles/1", map[string]any{"name": "Checkout"}); data(out)["name"] != "Checkout" {
		t.Errorf("rename: %v", out)
	}
	if code, _ := call(viewer, "GET", "/api/v1/workspace/cycles/99", nil); code != 404 {
		t.Errorf("a cycle that isn't: %d", code)
	}
	if _, out = call(viewer, "GET", base+"/cycles", nil); data(out)["enabled"] != true || len(data(out)["cycles"].([]any)) != 3 {
		t.Errorf("the project's cycles: %v", out)
	}

	// The Timeline: the epic with its issues; one undated issue left off.
	call(dev, "POST", base+"/issues", map[string]any{"title": "Undated"})
	_, out = call(viewer, "GET", base+"/issue-timeline?from=2026-10-01", nil)
	items := data(out)["items"].([]any)
	if len(items) != 3 || data(out)["undated"] != float64(1) || len(data(out)["cycles"].([]any)) != 3 {
		t.Errorf("timeline: %v", out)
	}
	for _, it := range items {
		if m := it.(map[string]any); m["key"] == "STO-1" && m["parent"] != "STO-2" {
			t.Errorf("STO-1's parent: %v", m)
		}
	}
	if code, _ := call(viewer, "GET", base+"/issue-timeline?from=soon", nil); code != 400 {
		t.Errorf("a bad from: %d", code)
	}

	// Cycle 1 ends: STO-1 moves to cycle 2 with an automatic history row;
	// the shipped sub-task stays.
	now = time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC) // Monday 03:00 in Athens
	svc.tickCycles(context.Background())
	_, out = call(viewer, "GET", base+"/issues/1", nil)
	if c, _ := data(out)["cycle"].(map[string]any); c["number"] != float64(2) || data(out)["carried_from"] != float64(1) {
		t.Errorf("carried over: %v", out)
	}
	if e := repo.events[len(repo.events)-1]; e.Kind != "cycle" || !e.Automatic || e.From != "1" || e.To != "2" {
		t.Errorf("history: %+v", e)
	}
	_, out = call(viewer, "GET", "/api/v1/workspace/cycles/1", nil)
	if c := data(out)["counts"].(map[string]any); data(out)["state"] != "ended" || c["carried"] != float64(1) || c["shipped"] != float64(1) {
		t.Errorf("cycle 1 ended: %v", out)
	}
	if _, out = call(viewer, "GET", "/api/v1/workspace/cycles/1/issues", nil); len(list(out)) != 2 {
		t.Errorf("an ended cycle's issues, the carried one too: %v", out)
	}
	if code, _ := call(dev, "PATCH", base+"/issues/4", map[string]any{"cycle": 1}); code != 422 {
		t.Errorf("planned into an ended cycle: %d", code)
	}
	if len(repo.cycles) != 4 {
		t.Errorf("cycles after the end: %v", spans(repo.cycles))
	}
}
