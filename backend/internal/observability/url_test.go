package observability

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
)

func TestValidateURL(t *testing.T) {
	for _, u := range []string{"", "http://loki:3100", "https://grafana.example.com/d/abc?orgId=1", "http://10.0.0.5:3000"} {
		if err := validateURL("grafana_url", u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	for _, u := range []string{"javascript:alert(1)", "data:text/html,hi", "grafana.example.com", "//evil.example", "ftp://x/y", "https://"} {
		err := validateURL("grafana_url", u)
		if !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%q: accepted (err=%v)", u, err)
		}
	}
}

// Loki's address must reach Docker's log driver on every server: an alias
// is refused before anything is saved (the repository is nil here).
func TestUpsertLokiRefusesAContainerAlias(t *testing.T) {
	_, err := (&Service{}).UpsertLoki(context.Background(), "org", "", UpsertLokiInput{ServiceID: "s", LokiURL: "http://loki:3100"})
	if !errors.Is(err, ErrLokiAlias) || !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("an alias was accepted: %v", err)
	}
}

// A loopback address is refused: on a shared server it would be another
// workspace's catalog Loki (D-148).
func TestUpsertLokiRefusesLoopback(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:39100", "http://localhost:3100", "http://[::1]:3100", "http://0.0.0.0:3100", "http://127.8.0.1"} {
		if _, err := (&Service{}).UpsertLoki(context.Background(), "org", "", UpsertLokiInput{ServiceID: "s", LokiURL: u}); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%s was accepted: %v", u, err)
		}
	}
}

// A service of another workspace is refused before anything is saved (the
// repository is nil here), as an unknown one.
func TestUpsertRefusesAnotherWorkspacesService(t *testing.T) {
	owns := &ownsP1{}
	s, ctx := NewService(nil, owns, nil), context.Background()
	const other = "00000000-0000-4000-8000-000000000001"
	_, loki := s.UpsertLoki(ctx, "org-1", "", UpsertLokiInput{ServiceID: other, LokiURL: "http://loki.example.com"})
	_, grafana := s.UpsertGrafana(ctx, "org-1", "p1", UpsertGrafanaInput{ServiceID: other})
	_, prometheus := s.UpsertPrometheus(ctx, "org-1", "", UpsertPrometheusInput{ServiceID: other})
	for name, err := range map[string]error{"loki": loki, "grafana": grafana, "prometheus": prometheus} {
		if !errors.Is(err, ErrServiceNotFound) {
			t.Errorf("%s: another workspace's service was accepted: %v", name, err)
		}
	}
	if owns.checks != 3 {
		t.Errorf("ownership checked %d times, want 3", owns.checks)
	}
	if _, err := s.UpsertPrometheus(ctx, "org-1", "", UpsertPrometheusInput{ServiceID: "not-a-uuid"}); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("a malformed service_id: %v", err)
	}
}

func TestRedactedDropsTheSecrets(t *testing.T) {
	if got := redacted("https://u:pw@loki.example.com/push?token=t#f"); got != "https://u:xxxxx@loki.example.com/push" {
		t.Fatalf("redacted kept a secret: %q", got)
	}
}

// projectGrafana is a service of org-1 in one of its projects, not a
// workspace service.
type projectGrafana struct{}

func (projectGrafana) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }
func (projectGrafana) IsWorkspaceService(context.Context, string) (bool, error)     { return false, nil }

// Only a workspace service becomes the workspace's Loki, Grafana or
// Prometheus (D-162): a project's deploy registers nothing, and setting one
// by hand is refused. The repository is nil: a write would panic.
func TestOnlyWorkspaceServicesRegister(t *testing.T) {
	s, ctx := NewService(nil, projectGrafana{}, zap.NewNop()), context.Background()
	const id = "00000000-0000-4000-8000-000000000002"
	for _, slug := range []string{"loki", "grafana", "prometheus"} {
		if err := s.RegisterIfObservability(ctx, "org-1", id, slug); err != nil {
			t.Errorf("%s: %v", slug, err)
		}
	}
	if _, err := s.UpsertGrafana(ctx, "org-1", "", UpsertGrafanaInput{ServiceID: id}); !errors.Is(err, ErrNotWorkspaceService) {
		t.Errorf("a project's Grafana set as the workspace's: %v", err)
	}
}
