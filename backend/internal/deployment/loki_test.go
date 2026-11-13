package deployment

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/observability"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// lokiQuerier answers with the project's Loki when one is set, else the
// workspace's, as the repository does.
type lokiQuerier struct {
	workspace, project map[string]string
	local              map[string]observability.LokiTarget // a workspace's catalog Loki, no address
}

func (q lokiQuerier) LokiFor(_ context.Context, orgID, projectID string) (observability.LokiTarget, error) {
	if u := q.project[projectID]; u != "" {
		return observability.LokiTarget{URL: u}, nil
	}
	if u := q.workspace[orgID]; u != "" {
		return observability.LokiTarget{URL: u}, nil
	}
	return q.local[orgID], nil
}

// lokiOrch is an Orchestrator that says whether it has the Loki driver.
type lokiOrch struct {
	Orchestrator
	has bool
}

func (o lokiOrch) HasLokiDriver(context.Context) (bool, error) { return o.has, nil }

func TestLokiForEveryDeploy(t *testing.T) {
	q := lokiQuerier{
		workspace: map[string]string{"org": "https://loki.example.com", "aliased": "http://loki:3100"},
		project:   map[string]string{"own": "https://loki.own.example.com"},
	}
	s := &Service{observabilityQuerier: q, log: zap.NewNop()}
	var lines []string
	log := func(l string) { lines = append(lines, l) }

	assert.Equal(t, "https://loki.own.example.com", s.lokiFor(context.Background(), TriggerInput{OrgID: "org", ProjectID: "own"}, lokiOrch{has: true}, log),
		"a project's own Loki comes first")
	assert.Equal(t, "https://loki.example.com", s.lokiFor(context.Background(), TriggerInput{OrgID: "org", ProjectID: "other"}, lokiOrch{has: true}, log))
	assert.Empty(t, lines)

	assert.Empty(t, s.lokiFor(context.Background(), TriggerInput{OrgID: "aliased"}, lokiOrch{has: true}, log), "an alias never resolves in the log driver")
	assert.Contains(t, lines[0], "is a container alias")

	assert.Empty(t, s.lokiFor(context.Background(), TriggerInput{OrgID: "org"}, lokiOrch{has: false}, log), "no driver: the container would not start")
	assert.Contains(t, lines[1], "no Loki log driver")

	assert.Empty(t, (&Service{log: zap.NewNop()}).lokiFor(context.Background(), TriggerInput{OrgID: "org"}, lokiOrch{has: true}, log))
}

// D-148's 1.0: with no address, a service on Loki's own server sends to its
// 127.0.0.1 port; elsewhere its logs stay put and the deploy log says why;
// Loki keeps its own logs.
func TestLokiForOnLokisServer(t *testing.T) {
	loki := observability.LokiTarget{ServiceID: "loki-svc", ServerID: "srv-1", Server: "fsn1-prod-01", Port: 39100}
	panel := observability.LokiTarget{ServiceID: "loki-svc", Port: 39101}
	legacy := observability.LokiTarget{URL: "http://loki:3100", ServiceID: "loki-svc", ServerID: "srv-1", Port: 39102} // an alias from before D-48
	q := lokiQuerier{local: map[string]observability.LokiTarget{"org": loki, "single": panel, "legacy": legacy}}
	s := &Service{observabilityQuerier: q, log: zap.NewNop()}
	var lines []string
	log := func(l string) { lines = append(lines, l) }
	ctx := context.Background()

	assert.Equal(t, "http://127.0.0.1:39100", s.lokiFor(ctx, TriggerInput{OrgID: "org", ServiceID: "app", ServerID: "srv-1"}, lokiOrch{has: true}, log))
	assert.Equal(t, "→ Logs go to Loki on this server (127.0.0.1:39100)", lines[0])

	assert.Empty(t, s.lokiFor(ctx, TriggerInput{OrgID: "org", ServiceID: "app", ServerID: "srv-2"}, lokiOrch{has: true}, log))
	assert.Contains(t, lines[1], "Loki runs on fsn1-prod-01, and central logs from other servers need an external Loki address")

	assert.Empty(t, s.lokiFor(ctx, TriggerInput{OrgID: "org", ServiceID: "app"}, lokiOrch{has: true}, log), "the panel's server is another server")
	assert.Empty(t, s.lokiFor(ctx, TriggerInput{OrgID: "org", ServiceID: "loki-svc", ServerID: "srv-1"}, lokiOrch{has: true}, log), "Loki's own logs stay local")
	assert.Len(t, lines, 3, "and Loki's deploy says nothing about it")

	assert.Equal(t, "http://127.0.0.1:39101", s.lokiFor(ctx, TriggerInput{OrgID: "single", ServiceID: "app"}, lokiOrch{has: true}, log),
		"a single-server install: Loki and the app on the panel's server")
	assert.Equal(t, "http://127.0.0.1:39102", s.lokiFor(ctx, TriggerInput{OrgID: "legacy", ServiceID: "app", ServerID: "srv-1"}, lokiOrch{has: true}, log),
		"an old alias address doesn't hide Loki on this server")
	assert.Empty(t, s.lokiFor(ctx, TriggerInput{OrgID: "org", ServiceID: "app", ServerID: "srv-1"}, lokiOrch{has: false}, log), "no driver: the container would not start")
	assert.Equal(t, "→ Logs stay on this server: it has no Loki log driver plugin (Re-provision installs it)", lines[len(lines)-1],
		"and the log says only that")
}

func TestIsContainerAlias(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://loki:3100":                  true,
		"https://loki.example.com":          false,
		"http://10.0.0.5:3100":              false,
		"http://[2a01:4f8::1]:3100":         false,
		"http://localhost:3100":             false,
		"https://loki.example.com/push?x=1": false,
	} {
		assert.Equal(t, want, IsContainerAlias(raw), raw)
	}
}
