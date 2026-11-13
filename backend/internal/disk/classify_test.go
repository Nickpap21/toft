package disk

import (
	"reflect"
	"slices"
	"sort"
	"testing"
)

const (
	apiID    = "aaaaaaaa-0000-4000-8000-000000000001"
	webID    = "bbbbbbbb-0000-4000-8000-000000000002"
	goneID   = "cccccccc-0000-4000-8000-000000000003"
	stackID  = "dddddddd-0000-4000-8000-000000000004"
	pulledID = "eeeeeeee-0000-4000-8000-000000000005"
)

func img(id string, containers int64, tags ...string) Image {
	return Image{ID: id, Tags: tags, Size: 100, SharedSize: 40, Containers: containers}
}

func kindsOf(p plan) map[string]Kind {
	out := map[string]Kind{}
	for k, ims := range p.byKind {
		for _, im := range ims {
			out[im.ID] = k
		}
	}
	return out
}

func TestClassify(t *testing.T) {
	var deps []LiveDeployment
	// api keeps 3; 5 deploys, each its own build.
	for i := 1; i <= 5; i++ {
		deps = append(deps, LiveDeployment{ID: "dep-api-" + string(rune('0'+i)), ServiceID: apiID, ImageTag: "paas/" + apiID + ":" + string(rune('0'+i)), Rank: i})
	}
	// web, an image service on the default (10), moved nginx:1.25 → nginx:1.27.
	deps = append(deps,
		LiveDeployment{ID: "dep-web-1", ServiceID: webID, ImageTag: "nginx:1.27", Rank: 1},
		LiveDeployment{ID: "dep-web-2", ServiceID: webID, ImageTag: "docker.io/library/nginx:1.25", Rank: 2},
	)
	// pulled: an image service deployed 12 times from a moving tag, then from redis.
	for i := 1; i <= 11; i++ {
		deps = append(deps, LiveDeployment{ID: "dep-pulled-" + string(rune('a'+i)), ServiceID: pulledID, ImageTag: "redis", Rank: i + 1})
	}
	deps = append(deps, LiveDeployment{ID: "dep-pulled-old", ServiceID: pulledID, ImageTag: "valkey/valkey:8", Rank: 12})
	f := Facts{
		Services:    []HostService{{ID: apiID, Name: "api", Kept: 3}, {ID: webID, Name: "web"}, {ID: stackID, Name: "stack"}, {ID: pulledID, Name: "cache"}},
		Deployments: deps,
		Prefixes:    map[string]bool{"aaaaaaaa": true, "bbbbbbbb": true, "dddddddd": true, "eeeeeeee": true},
	}
	u := Usage{Images: []Image{
		img("running", 1, "paas/"+apiID+":1"),
		img("api2", 0, "paas/"+apiID+":2"),
		img("api3", 0, "paas/"+apiID+":3"),
		img("api4", 0, "paas/"+apiID+":4"),
		img("api5", 0, "paas/"+apiID+":5"),
		img("gone", 0, "paas/"+goneID+":x"),
		img("nginx127", 0, "nginx:1.27"),
		img("nginx125", 0, "nginx:1.25"),
		img("redis", 0, "redis:latest"),
		img("valkey", 0, "valkey/valkey:8"),
		img("dangling", 0),
		img("stackbuild", 0, "paas-stack-dddddddd-app:latest"),
		img("deadstack", 0, "paas-old-cccccccc-app:latest"),
		img("postgres", 0, "postgres:15"),
		img("unknown", -1, "mysql:8"),
		{ID: "bydigest", Digests: []string{"ghcr.io/x/app@sha256:abc"}, Size: 10, SharedSize: -1},
	}}
	f.Deployments = append(f.Deployments, LiveDeployment{ID: "dep-digest", ServiceID: webID, ImageTag: "ghcr.io/x/app@sha256:abc", Rank: 3})
	p := classify(u, f)
	want := map[string]Kind{
		"running": InUse, "api2": Rollback, "api3": Rollback, "api4": OldBuilds, "api5": OldBuilds,
		"gone": OldBuilds, "nginx127": Rollback, "nginx125": Rollback, "redis": Rollback, "valkey": UnusedPulled,
		"dangling": Untagged, "stackbuild": Rollback, "deadstack": OldBuilds, "postgres": UnusedPulled,
		"unknown": InUse, "bydigest": Rollback,
	}
	if got := kindsOf(p); !reflect.DeepEqual(got, want) {
		t.Errorf("kinds:\n got %v\nwant %v", got, want)
	}
	if got := p.marks["api4"]; !reflect.DeepEqual(got, []string{"dep-api-4"}) {
		t.Errorf("marks for api4 = %v", got)
	}
	if got := p.marks["valkey"]; len(got) != 0 {
		t.Errorf("a pulled image marked its deployments: %v", got) // a rollback pulls it again
	}
	if len(p.marks["dangling"]) != 0 || len(p.marks["postgres"]) != 0 {
		t.Errorf("unexpected marks: %v", p.marks)
	}
	var api RollbackKept
	for _, rk := range p.rollback {
		if rk.ServiceID == apiID {
			api = rk
		}
	}
	if api.Kept != 3 || api.Images != 2 || api.Bytes != 120 {
		t.Errorf("api's rollback = %+v", api)
	}
	for _, c := range p.classes([]Kind{Untagged}) {
		if c.Kind == OldBuilds && (c.Count != 4 || c.Bytes != 240 || c.Automatic) {
			t.Errorf("old builds = %+v", c)
		}
		if c.Kind == Untagged && !c.Automatic {
			t.Errorf("untagged not automatic: %+v", c)
		}
	}
}

func TestClassifyPanel(t *testing.T) {
	u := Usage{Images: []Image{
		img("cur", 1, "ghcr.io/o/paas-backend:v0.32.0"),
		img("prev", 0, "ghcr.io/o/paas-backend:v0.31.0"),
		img("prevfe", 0, "ghcr.io/o/paas-frontend:v0.31.0"),
		img("old", 0, "ghcr.io/old/paas-backend:v0.10.0"),
		img("oldfe", 0, "ghcr.io/o/paas-frontend:v0.30.1"),
	}}
	got := kindsOf(classify(u, Facts{Panel: true, PanelReleases: []string{"v0.32.0", "v0.31.0"}}))
	want := map[string]Kind{"cur": InUse, "prev": PanelReleases, "prevfe": PanelReleases, "old": OldPanelReleases, "oldfe": OldPanelReleases}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with state:\n got %v\nwant %v", got, want)
	}
	// Without toft upgrade's state (deploy.sh), every release is kept.
	got = kindsOf(classify(u, Facts{Panel: true}))
	for id, k := range got {
		if k == OldPanelReleases {
			t.Errorf("%s offered without the release state", id)
		}
	}
	// On another server those images are someone's pulls.
	if k := kindsOf(classify(u, Facts{PanelReleases: []string{"v0.32.0"}}))["old"]; k != UnusedPulled {
		t.Errorf("not the panel: %s", k)
	}
}

// On a server shared with other workspaces (D-39) the examples hold only the
// viewer's images, also when the other workspace's services are gone and
// their images are left over.
func TestClassifyExamplesOnSharedServerWithoutForeignServices(t *testing.T) {
	u := Usage{Images: []Image{
		img("own", 0, "paas/"+apiID+":1"),
		img("left", 0, "registry.b.example/private/app:7"),
		img("leftbuild", 0, "paas/"+webID+":1"),
	}}
	f := Facts{OrgID: "org-a", Services: []HostService{{ID: apiID, Name: "api", OrgID: "org-a"}}}
	examples := func(f Facts) []string {
		var all []string
		for _, c := range classify(u, f).classes(nil) {
			all = append(all, c.Examples...)
		}
		return all
	}
	if !slices.Contains(examples(f), "registry.b.example/private/app:7") {
		t.Errorf("unshared server: examples = %v, want its stray image", examples(f))
	}
	f.Shared = true
	for _, e := range examples(f) {
		if e == "registry.b.example/private/app:7" || e == "paas/"+webID+":1" {
			t.Errorf("shared server's examples name another workspace's image %q", e)
		}
	}
}

func TestOrphanVolumes(t *testing.T) {
	anon := "350e9373d2f9bbe4fedbbcfef804c87d48e6b671af9dfbde3526686bdf42f483"
	vols := []Volume{
		{Name: "paas-vol-aaaaaaaa-data", RefCount: 0, Bytes: 5}, // live service, stopped
		{Name: "paas-vol-cccccccc-data", RefCount: 0, Bytes: 7}, // deleted
		{Name: "paas-vol-cccccccc-busy", RefCount: 1, Bytes: 9}, // deleted, but mounted
		{Name: "paas-old-cccccccc_pg", RefCount: 0, Bytes: 3, Labels: map[string]string{"com.docker.compose.project": "paas-old-cccccccc"}},
		{Name: "paas-stack-dddddddd_pg", RefCount: 0, Bytes: 3, Labels: map[string]string{"com.docker.compose.project": "paas-stack-dddddddd"}},
		{Name: "paas_pgdata", RefCount: 0, Bytes: 100, Labels: map[string]string{"com.docker.compose.project": "paas"}}, // the panel's
		{Name: anon, RefCount: 0, Bytes: 11, Labels: map[string]string{"com.docker.volume.anonymous": ""}},
		{Name: "mydata", RefCount: 0, Bytes: 11, Labels: map[string]string{"com.docker.volume.anonymous": ""}}, // a name, not an ID
		{Name: "handmade", RefCount: 0, Bytes: 1},
		{Name: "paas-vol-cccccccc-unknown", RefCount: -1, Bytes: 1},
	}
	p := classify(Usage{Volumes: vols}, Facts{Prefixes: map[string]bool{"aaaaaaaa": true, "dddddddd": true}})
	var names []string
	for _, v := range p.volumes {
		names = append(names, v.Name+":"+string(v.Kind))
	}
	sort.Strings(names)
	want := []string{anon + ":anonymous", "paas-old-cccccccc_pg:compose_orphan", "paas-vol-cccccccc-data:deleted_service"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("offered:\n got %v\nwant %v", names, want)
	}
	if p.inUse.Count != 7 {
		t.Errorf("in use = %d", p.inUse.Count)
	}
}

func TestPolicy(t *testing.T) {
	gb := int64(1_000_000_000)
	v := Policy{}.view(false, 160*gb)
	if v.Effective.BuildCacheLimitBytes != 16*gb || v.Effective.AlertPercent != 85 || v.Effective.AlertResolvePercent != 80 ||
		!reflect.DeepEqual(v.Effective.AutoClean, []Kind{Untagged, OldBuilds}) {
		t.Errorf("defaults = %+v", v.Effective)
	}
	if got := (Policy{}).view(false, 5*gb).Effective.BuildCacheLimitBytes; got != gb {
		t.Errorf("small disk limit = %d", got)
	}
	ninety := 90
	v = Policy{AlertPercent: &ninety, AutoClean: []Kind{}}.view(true, 0)
	if v.Effective.AlertResolvePercent != 85 || len(v.Effective.AutoClean) != 0 || v.Effective.BuildCacheLimitBytes != fallbackCacheLimit {
		t.Errorf("set = %+v", v.Effective)
	}
	bad := []Policy{
		{BuildCacheLimitBytes: ptr(gb - 1)},
		{BuildCacheLimitBytes: ptr(200 * gb)},
		{AlertPercent: ptr(49)},
		{AlertPercent: ptr(100)},
		{AutoClean: []Kind{OldPanelReleases}},
		{AutoClean: []Kind{InUse}},
		{AutoClean: []Kind{Untagged, Untagged}},
	}
	for _, p := range bad {
		if p.Validate(false, 160*gb) == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	if err := (Policy{AutoClean: []Kind{OldPanelReleases, UnusedPulled}, BuildCacheLimitBytes: ptr(2 * gb), AlertPercent: ptr(50)}).Validate(true, 160*gb); err != nil {
		t.Errorf("refused: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
