package rollout

import (
	"strings"
	"testing"
)

// A service's file names only its own routers, middlewares and service
// (D-113): labels that would take another service's names, use its
// middlewares or route to Traefik's API are refused.
func TestRoutesForRefusesWhatIsntTheServices(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"traefik.enable": "true",
			"traefik.http.routers.paas-aaaaaaaa-0.rule":                        "Host(`a.test`)",
			"traefik.http.routers.paas-aaaaaaaa-0.middlewares":                 "secure-headers@file",
			"traefik.http.routers.paas-aaaaaaaa-0.entrypoints":                 "websecure",
			"traefik.http.services.paas-svc-aaaaaaaa.loadbalancer.server.port": "80",
		}
	}
	if f, err := RoutesFor("aaaaaaaa", base(), []string{"c"}); err != nil || f.HTTP.Routers["paas-aaaaaaaa-0"].Service != "paas-svc-aaaaaaaa" {
		t.Fatalf("the service's own routes: %v %+v", err, f)
	}
	bad := map[string][2]string{
		"another's router":      {"traefik.http.routers.paas-bbbbbbbb-0.rule", "Host(`b.test`)"},
		"another's middleware":  {"traefik.http.routers.paas-aaaaaaaa-0.middlewares", "paas-bbbbbbbb-basicauth@file"},
		"the API":               {"traefik.http.routers.paas-aaaaaaaa-0.service", "api@internal"},
		"another's service":     {"traefik.http.services.paas-svc-bbbbbbbb.loadbalancer.server.port", "80"},
		"an unknown setting":    {"traefik.http.middlewares.paas-aaaaaaaa-x.headers.customrequestheaders.x", "1"},
		"tcp":                   {"traefik.tcp.routers.paas-aaaaaaaa-0.rule", "HostSNI(`*`)"},
		"the API's entry point": {"traefik.http.routers.paas-aaaaaaaa-0.entrypoints", "websecure,toft-api"},
		"every entry point":     {"traefik.http.routers.paas-aaaaaaaa-0.entrypoints", ""},
	}
	for name, kv := range bad {
		l := base()
		l[kv[0]] = kv[1]
		if _, err := RoutesFor("aaaaaaaa", l, []string{"c"}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A rendered file always parses back: one Traefik can't parse would stop
// it from loading any change in the directory.
func TestRouteFileParsesBack(t *testing.T) {
	l := map[string]string{
		"traefik.enable": "true",
		"traefik.http.routers.paas-aaaaaaaa-0.rule":                        "Host(`a.test`) || Host(`b: \"c\".test`)",
		"traefik.http.middlewares.paas-aaaaaaaa-basicauth.basicauth.users": "u:$2y$04$abc/def",
		"traefik.http.routers.paas-aaaaaaaa-0.middlewares":                 "paas-aaaaaaaa-basicauth",
		"traefik.http.routers.paas-aaaaaaaa-0.entrypoints":                 "websecure",
		"traefik.http.services.paas-svc-aaaaaaaa.loadbalancer.server.port": "8080",
	}
	f, err := RoutesFor("aaaaaaaa", l, []string{"paas-a-blue", "paas-a-green"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseRouteFile(raw)
	if err != nil || back.HTTP.Routers["paas-aaaaaaaa-0"].Rule != l["traefik.http.routers.paas-aaaaaaaa-0.rule"] ||
		back.HTTP.Middlewares["paas-aaaaaaaa-basicauth"].BasicAuth.Users[0] != "u:$2y$04$abc/def" ||
		len(back.HTTP.Services["paas-svc-aaaaaaaa"].LoadBalancer.Servers) != 2 {
		t.Fatalf("%v\n%s", err, raw)
	}
	if _, err := ParseRouteFile([]byte("http:\n  routers: [oops\n")); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("a broken file parsed: %v", err)
	}
}
