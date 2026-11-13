package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"gopkg.in/yaml.v3"
)

// panelRoutesDir is where docker-compose.yml mounts traefik/dynamic, the
// directory Traefik's file provider watches, so a change applies without a
// restart (D-62).
const panelRoutesDir = "/etc/toft/traefik"

// gitPaths are git's smart HTTP requests (D-71), routed to the backend
// before the web app: https://<panel>/<workspace>/<repository>.git/…
// apiPaths are the backend's beside git: the API, and agents' MCP endpoint
// and OAuth metadata (D-100).
const apiPaths = "(PathPrefix(`/api/`) || Path(`/mcp`) || PathPrefix(`/.well-known/oauth-`))"

const gitPaths = "PathRegexp(`^/[^/]+/[^/]+/(info/refs|git-upload-pack|git-receive-pack)$`)"

// panelPriority is above any app router's: an app's priority is its rule's
// length, and a host name is at most 253 characters.
const panelPriority = 10000

// panelRoutesFile writes the panel's routes as panel.yml in dir
// (instance.PanelRoutesWriter). Without the directory (the backend is not
// run by docker compose) it writes nothing and says so.
type panelRoutesFile struct{ dir string }

func (f panelRoutesFile) WritePanelRoutes(r instance.PanelRoutes) error {
	for _, h := range []string{r.Domain, r.LegacyAPIHost} {
		// The name goes into a Traefik rule: never anything but a host name.
		if h != "" && (!domain.ValidHostname(h) || domain.IsWildcard(h)) {
			return fmt.Errorf("%q is not a host name", h)
		}
	}
	if _, err := os.Stat(f.dir); err != nil {
		return fmt.Errorf("routes directory: %w", err)
	}
	// A router on the panel's own host names asks for a certificate and
	// outranks every app's: Traefik ranks the routers it has by rule length,
	// which leaves an app given the panel's host name tied with the panel,
	// and an app that wins the tie serves the panel's origin.
	router := func(rule, service string, priority int) map[string]any {
		tls := map[string]any{}
		if priority >= panelPriority {
			tls["certResolver"] = "letsencrypt"
		}
		return map[string]any{
			"rule":        rule,
			"entryPoints": []string{"websecure"},
			"service":     service,
			"middlewares": []string{"secure-headers@file"},
			"tls":         tls,
			"priority":    priority,
		}
	}
	routers := map[string]any{}
	if r.Domain != "" {
		routers["toft-panel"] = router(fmt.Sprintf("Host(`%s`)", r.Domain), "toft-frontend", panelPriority)
		routers["toft-panel-api"] = router(fmt.Sprintf("Host(`%s`) && %s", r.Domain, apiPaths), "toft-backend", panelPriority+1)
		routers["toft-panel-git"] = router(fmt.Sprintf("Host(`%s`) && %s", r.Domain, gitPaths), "toft-backend", panelPriority+2)
	}
	if r.Domain == "" || r.AnyHost {
		// Any host no app claims, the IP included, on Traefik's own
		// certificate. Lowest priorities: any app's own router wins.
		routers["toft-panel-any"] = router("PathPrefix(`/`)", "toft-frontend", 1)
		routers["toft-panel-any-api"] = router(apiPaths, "toft-backend", 2)
		routers["toft-panel-any-git"] = router(gitPaths, "toft-backend", 3)
	}
	if r.LegacyAPIHost != "" {
		routers["toft-panel-api-host"] = router(fmt.Sprintf("Host(`%s`)", r.LegacyAPIHost), "toft-backend", panelPriority)
	}
	if r.Dashboard && r.Domain != "" {
		dash := router(fmt.Sprintf("Host(`traefik.%s`)", r.Domain), "api@internal", panelPriority)
		// Its basic auth is a label on the Traefik container (docker-compose.yml).
		dash["middlewares"] = []string{"dashboard-auth@docker", "secure-headers@file"}
		routers["toft-traefik-dashboard"] = dash
	}
	server := func(url string) map[string]any {
		return map[string]any{"loadBalancer": map[string]any{"servers": []map[string]string{{"url": url}}}}
	}
	conf := map[string]any{
		"routers": routers,
		"services": map[string]any{
			"toft-frontend": server("http://paas-frontend:3000"),
			"toft-backend":  server("http://paas-backend:8080"),
		},
	}
	if r.CertificateAsk != 0 {
		// No router uses it: it changes the configuration, which is what
		// has Traefik ask Let's Encrypt again. Traefik skips a file whose
		// configuration is the same as before.
		conf["middlewares"] = map[string]any{"toft-certificate-ask": map[string]any{
			"headers": map[string]any{"customRequestHeaders": map[string]string{"X-Toft-Certificate-Ask": fmt.Sprint(r.CertificateAsk)}},
		}}
	}
	body, err := yaml.Marshal(map[string]any{"http": conf})
	if err != nil {
		return fmt.Errorf("encode routes: %w", err)
	}
	body = append([]byte("# Written by the Toft backend from the panel's domain (D-62). Edits are overwritten.\n"), body...)
	// The same routes are not written again: a write Traefik saw as a change
	// would have it ask Let's Encrypt again, as at every backend start.
	path := filepath.Join(f.dir, "panel.yml")
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, body) {
		return nil
	}

	// Write then rename, so Traefik never reads half a file; the temporary
	// name doesn't end in .yml, so Traefik ignores it.
	tmp, err := os.CreateTemp(f.dir, ".panel-*.tmp")
	if err != nil {
		return fmt.Errorf("create routes file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write routes file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod routes file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close routes file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename routes file: %w", err)
	}
	return nil
}
