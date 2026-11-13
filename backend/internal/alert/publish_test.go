package alert

import (
	"context"
	"sort"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type told struct {
	org string
	e   broker.Event
}

type tellings struct{ got []told }

func (p *tellings) Publish(orgID string, e broker.Event) { p.got = append(p.got, told{orgID, e}) }

// sorted is what was told, by workspace.
func (p *tellings) sorted() []told {
	out := append([]told(nil), p.got...)
	sort.Slice(out, func(i, j int) bool { return out[i].org < out[j].org })
	return out
}

// Announce tells each workspace once, whatever number of alerts changed,
// and a service's alert adds "services": its health dot.
func TestAnnounceTellsEachWorkspaceOnce(t *testing.T) {
	ctx := context.Background()
	pub, repo := &tellings{}, newFakeRepo()
	s := NewService(repo, crypto.NewKeys(testKey, ""), zap.NewNop()).WithPublisher(pub)

	s.Announce(ctx, nil)
	assert.Empty(t, pub.got, "nothing changed")

	s.Announce(ctx, []Event{{Kind: KindServiceUnhealthy, Subject: "svc-1"}, {Kind: KindContainerExited, Subject: "svc-1", Resolved: true}, {Kind: KindDiskFull, Subject: "srv-1"}})
	assert.Equal(t, []told{{"org-1", broker.Changed("", "alerts", "services")}}, pub.got)

	pub.got = nil
	s.Announce(ctx, []Event{{Kind: KindDiskFull, Subject: "srv-1"}})
	assert.Equal(t, []told{{"org-1", broker.Changed("", "alerts")}}, pub.got, "a server's alert is not a service's")

	pub.got = nil
	s.Announce(ctx, []Event{{Kind: KindPanelContainer, Subject: ControlPlane}})
	assert.Equal(t, []told{{"org-1", broker.Changed("", "alerts")}, {"org-2", broker.Changed("", "alerts")}}, pub.sorted(), "the panel's go to every operator's workspace")

	pub.got = nil
	s.Announce(ctx, []Event{{Kind: KindServiceDown, Subject: "gone"}})
	assert.Empty(t, pub.got, "a deleted service's alert has no workspace to tell")

	// An outage changes many services' alerts in one pass: one lookup.
	pub.got, repo.serviceLookups = nil, 0
	s.Announce(ctx, []Event{{Kind: KindServiceDown, Subject: "svc-1"}, {Kind: KindServiceDown, Subject: "gone"}, {Kind: KindServiceUnhealthy, Subject: "svc-1"}})
	assert.Equal(t, 1, repo.serviceLookups)
	assert.Equal(t, []told{{"org-1", broker.Changed("", "alerts", "services")}}, pub.got)
}

type announcements struct{ calls [][]Event }

func (a *announcements) Announce(_ context.Context, changed []Event) {
	a.calls = append(a.calls, append([]Event(nil), changed...))
}

// A pass announces once, with everything it opened or resolved, and only
// when something changed.
func TestMonitorAnnouncesOncePerPassWhenSomethingChanged(t *testing.T) {
	p := &fakeProbe{
		targets: []service.MetricsTarget{{ID: "svc-1", Name: "api", ServerID: "srv-1"}, {ID: "svc-2", Name: "web", ServerID: "srv-1"}},
		states:  map[string][]service.ContainerState{"svc-1": {{Name: "api", State: "restarting"}}, "svc-2": {{Name: "web", State: "restarting"}}},
		disk:    map[string]int{"": 40, "srv-1": 40},
	}
	st, ev, an := &memStore{open: map[key]bool{}}, &events{}, &announcements{}
	m := newTestMonitor(p, st, ev).WithAnnouncer(an)
	ctx := context.Background()

	m.tick(ctx)
	assert.Empty(t, an.calls, "one bad sample opens nothing")
	m.tick(ctx)
	if assert.Len(t, an.calls, 1, "two alerts opened in one pass, one announcement") {
		assert.Len(t, an.calls[0], 2)
	}
	m.tick(ctx)
	assert.Len(t, an.calls, 1, "a pass that changed nothing says nothing")

	p.states["svc-1"], p.states["svc-2"] = running("api"), running("web")
	m.tick(ctx)
	m.tick(ctx)
	assert.Len(t, an.calls, 1, "recovering is not resolved yet")
	m.tick(ctx)
	if assert.Len(t, an.calls, 2) {
		assert.Len(t, an.calls[1], 2, "both resolved in one announcement")
		assert.True(t, an.calls[1][0].Resolved)
	}
}
