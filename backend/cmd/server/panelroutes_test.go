package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"gopkg.in/yaml.v3"
)

func readRoutes(t *testing.T, dir string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "panel.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		HTTP struct {
			Routers map[string]map[string]any `yaml:"routers"`
		} `yaml:"http"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.HTTP.Routers
}

func TestPanelRoutesFile(t *testing.T) {
	dir := t.TempDir()
	f := panelRoutesFile{dir: dir}

	// No domain: everything no app claims, on Traefik's own certificate.
	if err := f.WritePanelRoutes(instance.PanelRoutes{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	r := readRoutes(t, dir)
	if len(r) != 3 || r["toft-panel-any"]["rule"] != "PathPrefix(`/`)" || r["toft-panel-any"]["priority"] != 1 || r["toft-panel-any-api"]["priority"] != 2 || r["toft-panel-any-git"]["priority"] != 3 {
		t.Fatalf("ip routes: %v", r)
	}
	if tls := r["toft-panel-any"]["tls"].(map[string]any); len(tls) != 0 {
		t.Fatalf("ip routes ask for a certificate: %v", tls)
	}

	// A domain during setup: the domain with its certificate, and still the IP.
	if err := f.WritePanelRoutes(instance.PanelRoutes{Domain: "panel.example", AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	r = readRoutes(t, dir)
	if len(r) != 6 || r["toft-panel"]["tls"].(map[string]any)["certResolver"] != "letsencrypt" || len(r["toft-panel-any"]["tls"].(map[string]any)) != 0 {
		t.Fatalf("setup routes: %v", r)
	}

	// A domain from BASE_DOMAIN: the same origin, plus api.<domain>.
	if err := f.WritePanelRoutes(instance.PanelRoutes{Domain: "panel.example", LegacyAPIHost: "api.panel.example"}); err != nil {
		t.Fatal(err)
	}
	r = readRoutes(t, dir)
	if len(r) != 4 || r["toft-panel-api"]["rule"] != "Host(`panel.example`) && "+apiPaths || r["toft-panel-api"]["service"] != "toft-backend" {
		t.Fatalf("api route: %v", r["toft-panel-api"])
	}
	// git's smart HTTP at the boards' URLs goes to the backend (D-71).
	if r["toft-panel-git"]["rule"] != "Host(`panel.example`) && "+gitPaths || r["toft-panel-git"]["service"] != "toft-backend" {
		t.Fatalf("git route: %v", r["toft-panel-git"])
	}
	if r["toft-panel-api-host"]["rule"] != "Host(`api.panel.example`)" || r["toft-panel"]["tls"].(map[string]any)["certResolver"] != "letsencrypt" {
		t.Fatalf("domain routes: %v", r)
	}
	// The panel's host names outrank any app given them: an app's
	// priority is its rule's length.
	if r["toft-panel"]["priority"] != panelPriority || r["toft-panel-api"]["priority"] != panelPriority+1 ||
		r["toft-panel-git"]["priority"] != panelPriority+2 || r["toft-panel-api-host"]["priority"] != panelPriority {
		t.Fatalf("domain route priorities: %v", r)
	}

	// No ask, no middleware of its own.
	if raw, _ := os.ReadFile(filepath.Join(dir, "panel.yml")); strings.Contains(string(raw), "toft-certificate-ask") {
		t.Fatalf("a certificate ask nobody made:\n%s", raw)
	}
	// A restored panel's ask for its certificate changes the configuration,
	// so Traefik takes it and asks Let's Encrypt again; the routes are the same.
	for _, n := range []int64{1, 2} {
		if err := f.WritePanelRoutes(instance.PanelRoutes{Domain: "panel.example", AnyHost: true, CertificateAsk: n}); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "panel.yml"))
		var doc struct {
			HTTP struct {
				Middlewares map[string]struct {
					Headers struct {
						CustomRequestHeaders map[string]string `yaml:"customRequestHeaders"`
					} `yaml:"headers"`
				} `yaml:"middlewares"`
			} `yaml:"http"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if got := doc.HTTP.Middlewares["toft-certificate-ask"].Headers.CustomRequestHeaders["X-Toft-Certificate-Ask"]; got != fmt.Sprint(n) {
			t.Fatalf("ask %d: header %q in\n%s", n, got, raw)
		}
		if r := readRoutes(t, dir); len(r) != 6 {
			t.Fatalf("ask %d changed the routes: %v", n, r)
		}
	}
	// The same routes again, as at a backend start, leave the file alone:
	// Traefik sees no change, so it doesn't ask Let's Encrypt again.
	before, err := os.Stat(filepath.Join(dir, "panel.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WritePanelRoutes(instance.PanelRoutes{Domain: "panel.example", AnyHost: true, CertificateAsk: 2}); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(filepath.Join(dir, "panel.yml")); err != nil || !os.SameFile(before, after) {
		t.Fatalf("the same routes were written again (%v)", err)
	}

	// Traefik's dashboard on traefik.<domain>, behind its basic auth.
	if err := f.WritePanelRoutes(instance.PanelRoutes{Domain: "panel.example", Dashboard: true}); err != nil {
		t.Fatal(err)
	}
	dash := readRoutes(t, dir)["toft-traefik-dashboard"]
	if dash["rule"] != "Host(`traefik.panel.example`)" || dash["service"] != "api@internal" {
		t.Fatalf("dashboard: %v", dash)
	}
	if mw := dash["middlewares"].([]any); len(mw) != 2 || mw[0] != "dashboard-auth@docker" {
		t.Fatalf("dashboard without its basic auth: %v", mw)
	}

	// Anything but a host name never reaches a rule; the last good file stays.
	for _, bad := range []string{"panel.example`) || PathPrefix(`/", "*.panel.example", "localhost"} {
		if err := f.WritePanelRoutes(instance.PanelRoutes{Domain: bad}); err == nil {
			t.Fatalf("%q was written", bad)
		}
	}
	if readRoutes(t, dir)["toft-traefik-dashboard"] == nil {
		t.Fatal("a refused write replaced the file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".panel-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	if err := (panelRoutesFile{dir: filepath.Join(dir, "missing")}).WritePanelRoutes(instance.PanelRoutes{}); err == nil {
		t.Fatal("wrote without the directory")
	}
}
