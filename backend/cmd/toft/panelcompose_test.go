package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"gopkg.in/yaml.v3"
)

// panelTraefik is the panel's Traefik service in the repository's
// docker-compose.yml, the file every install runs.
func panelTraefik(t *testing.T) (command []string, logging map[string]any, volumes []string) {
	s := panelTraefikService(t)
	return s.Command, s.Logging, s.Volumes
}

type composeService struct {
	Command []string       `yaml:"command"`
	Logging map[string]any `yaml:"logging"`
	Volumes []string       `yaml:"volumes"`
	Ports   []string       `yaml:"ports"`
	Labels  []string       `yaml:"labels"`
}

func panelTraefikService(t *testing.T) composeService {
	t.Helper()
	b, err := os.ReadFile("../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	s, ok := f.Services["traefik"]
	if !ok {
		t.Fatal("no traefik service")
	}
	return s
}

// flagValue is the value of a --name=value flag in command, matched
// without case as Traefik reads them; "" when absent.
func flagValue(command []string, name string) string {
	for _, c := range command {
		if k, v, ok := strings.Cut(c, "="); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// The panel's Traefik builds no route from a container that carries a
// service's label and a compose project, with the constraint every server
// reading labels has. `toft upgrade` brings the file up, so a route such a
// container had before the upgrade is gone at the first start after it.
func TestPanelTraefikPassesOverLookAlikes(t *testing.T) {
	if got := flagValue(panelTraefikService(t).Command, "--providers.docker.constraints"); got != rollout.LookAlikeConstraint {
		t.Errorf("docker-compose.yml: the Docker provider's constraints are %q, want %q", got, rollout.LookAlikeConstraint)
	}
}

// The panel's Traefik keeps the same latency buckets as every remote one
// (D-156): the backend reads its page with the same parser.
func TestPanelTraefikLatencyBuckets(t *testing.T) {
	s := panelTraefikService(t)
	if got := flagValue(s.Command, "--metrics.prometheus.buckets"); got != hoststats.LatencyBounds {
		t.Errorf("buckets %q, want hoststats.LatencyBounds %q", got, hoststats.LatencyBounds)
	}
	if flagValue(s.Command, "--metrics.prometheus.addServicesLabels") != "true" {
		t.Error("services are not labelled")
	}
}

// The panel's metrics page names every service on the panel host: it is
// never on the web entrypoints, its port isn't published on the host (the
// backend reads it over the control plane's network), and the one router
// that serves it takes only the metrics entrypoint, behind the
// control-plane allowlist. These fail if it ever becomes reachable from
// outside.
func TestPanelTraefikMetricsAreControlPlaneOnly(t *testing.T) {
	s := panelTraefikService(t)
	if ep := flagValue(s.Command, "--metrics.prometheus.entryPoint"); ep != "metrics" {
		t.Fatalf("metrics on entrypoint %q, want metrics", ep)
	}
	if flagValue(s.Command, "--metrics.prometheus.manualrouting") != "true" {
		t.Error("without manual routing Traefik routes the page by itself, without the allowlist")
	}
	addr := flagValue(s.Command, "--entrypoints.metrics.address")
	_, port, ok := strings.Cut(addr, ":")
	if !ok || port == "" || port == "80" || port == "443" ||
		addr == flagValue(s.Command, "--entrypoints.web.address") || addr == flagValue(s.Command, "--entrypoints.websecure.address") {
		t.Fatalf("metrics entrypoint %q shares the web's port", addr)
	}
	for _, p := range s.Ports {
		if parts := strings.Split(p, ":"); parts[len(parts)-1] == port {
			t.Errorf("port %q: the metrics port is published on the host", p)
		}
	}

	labels := map[string]string{}
	for _, l := range s.Labels {
		if k, v, ok := strings.Cut(l, "="); ok {
			labels[k] = v
		}
	}
	for k, v := range labels {
		if strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".service") && v == "prometheus@internal" && k != "traefik.http.routers.toft-metrics.service" {
			t.Errorf("router %s also serves the metrics page", k)
		}
	}
	if labels["traefik.http.routers.toft-metrics.service"] != "prometheus@internal" {
		t.Fatal("no toft-metrics router")
	}
	if eps := labels["traefik.http.routers.toft-metrics.entrypoints"]; eps != "metrics" {
		t.Errorf("toft-metrics on entrypoints %q, want metrics only", eps)
	}
	if mw := labels["traefik.http.routers.toft-metrics.middlewares"]; mw != "toft-control-plane-only" {
		t.Errorf("toft-metrics middlewares %q, want toft-control-plane-only", mw)
	}
	ranges := labels["traefik.http.middlewares.toft-control-plane-only.ipallowlist.sourcerange"]
	if !strings.HasPrefix(ranges, "127.0.0.1/32,::1/128,") || strings.Contains(ranges, "0.0.0.0/0") || strings.Contains(ranges, "::/0") {
		t.Errorf("allowlist %q is not the control plane's", ranges)
	}
}

// The panel's Traefik logs to its container, capped like every app's
// (rollout.LocalLogConfig), not to an access log file nothing rotated.
func TestPanelTraefikLogsAreCapped(t *testing.T) {
	command, logging, volumes := panelTraefik(t)
	if logging["driver"] != "local" {
		t.Errorf("logging driver = %v, want local", logging["driver"])
	}
	opts, _ := logging["options"].(map[string]any)
	if opts["max-size"] != "20m" || opts["max-file"] != "5" {
		t.Errorf("logging options = %v, want max-size 20m, max-file 5", opts)
	}
	for _, c := range command {
		if strings.HasPrefix(c, "--accesslog.filepath") {
			t.Errorf("command still writes a file: %s", c)
		}
	}
	for _, v := range volumes {
		if strings.HasSuffix(v, ":/logs") {
			t.Errorf("volume %s: the access log file is gone", v)
		}
	}
}

// Every panel container's log is capped as every app's is
// (rollout.LocalLogConfig): Docker's default json-file driver keeps
// everything a container ever wrote.
func TestPanelContainersLogsAreCapped(t *testing.T) {
	seen := map[string]bool{} // docker-compose.git.yml's backend adds to the main file's
	for _, file := range []string{"../../../docker-compose.yml", "../../../docker-compose.git.yml"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Services map[string]struct {
				Logging struct {
					Driver  string            `yaml:"driver"`
					Options map[string]string `yaml:"options"`
				} `yaml:"logging"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		if len(f.Services) == 0 {
			t.Fatalf("%s: no services", file)
		}
		for name, s := range f.Services {
			if seen[name] {
				continue
			}
			seen[name] = true
			if s.Logging.Driver != "local" || s.Logging.Options["max-size"] != "20m" || s.Logging.Options["max-file"] != "5" {
				t.Errorf("%s: %s logs with %+v, want the local driver at 20m x 5", file, name, s.Logging)
			}
		}
	}
}

// The backend runs with an init as pid 1, which reaps the processes
// Nixpacks' sandbox kills (internal/docker nixpacksJail): the server reaps
// only its own children.
func TestPanelBackendHasAnInit(t *testing.T) {
	b, err := os.ReadFile("../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Services map[string]struct {
			Init bool `yaml:"init"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if !f.Services["backend"].Init {
		t.Error("docker-compose.yml: the backend has no init: true")
	}
}
