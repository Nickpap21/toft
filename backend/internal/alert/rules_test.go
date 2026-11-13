package alert

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
)

// run feeds samples (true = bad) and returns, per sample, "fire", "resolve"
// or "", storing each outcome as the monitor does.
func run(t *tracker, k key, samples ...bool) []string {
	out := make([]string, 0, len(samples))
	for _, bad := range samples {
		fire, resolve := t.observe(k, bad)
		switch {
		case fire:
			t.setOpen(k, true)
			out = append(out, "fire")
		case resolve:
			t.setOpen(k, false)
			out = append(out, "resolve")
		default:
			out = append(out, "")
		}
	}
	return out
}

func TestServiceAlertFiresOnSecondBadSampleAndResolvesOnThirdGood(t *testing.T) {
	k := key{KindServiceDown, "svc-1"}
	assert.Equal(t,
		[]string{"", "fire", "", "", "", "resolve"},
		run(newTracker(), k, true, true, true, false, false, false))
}

func TestOneBadSampleDoesNotFire(t *testing.T) {
	// A deploy or a restart that recovers within a minute.
	k := key{KindServiceDown, "svc-1"}
	assert.Equal(t, []string{"", "", "", ""}, run(newTracker(), k, true, false, true, false))
}

func TestCrashLoopDoesNotFlap(t *testing.T) {
	// A crash loop seen alternately restarting and running fires once and
	// stays open: the good samples never reach three in a row.
	k := key{KindServiceRestarting, "svc-1"}
	got := run(newTracker(), k, true, true, false, true, false, false, true, false)
	assert.Equal(t, []string{"", "fire", "", "", "", "", "", ""}, got)
}

func TestAnOpenAlertLoadedAtStartupIsNotFiredAgainButResolves(t *testing.T) {
	tr := newTracker()
	k := key{KindServiceUnhealthy, "svc-1"}
	tr.setOpen(k, true) // loaded from the database
	assert.Equal(t, []string{"", "", "", "", "resolve"}, run(tr, k, true, true, false, false, false))
}

func TestServerUnreachableResolvesOnFirstGoodSample(t *testing.T) {
	k := key{KindServerUnreachable, "srv-1"}
	assert.Equal(t, []string{"", "fire", "resolve"}, run(newTracker(), k, true, true, false))
}

func TestConditions(t *testing.T) {
	cs := func(name, state, health string, code int) service.ContainerState {
		return service.ContainerState{Name: name, State: state, Health: health, ExitCode: code}
	}
	cases := []struct {
		name   string
		states []service.ContainerState
		want   []Kind
	}{
		{"healthy", []service.ContainerState{cs("a", "running", "healthy", 0)}, nil},
		{"no container", nil, []Kind{KindServiceDown}},
		{"stopped", []service.ContainerState{cs("a", "exited", "", 0)}, []Kind{KindServiceDown}},
		{"crashed", []service.ContainerState{cs("a", "exited", "", 1)}, []Kind{KindServiceDown, KindContainerExited}},
		{"crash loop", []service.ContainerState{cs("a", "restarting", "", 0)}, []Kind{KindServiceRestarting}},
		{"unhealthy", []service.ContainerState{cs("a", "running", "unhealthy", 0)}, []Kind{KindServiceUnhealthy}},
		{"compose job failed", []service.ContainerState{cs("web", "running", "", 0), cs("migrate", "exited", "", 2)}, []Kind{KindContainerExited}},
		{"compose job done", []service.ContainerState{cs("web", "running", "", 0), cs("migrate", "exited", "", 0)}, nil},
	}
	for _, tc := range cases {
		got := conditions(tc.states, service.ContainerHealthImage)
		var kinds []Kind
		for _, k := range serviceKinds {
			if _, ok := got[k]; ok {
				kinds = append(kinds, k)
			}
		}
		assert.Equal(t, tc.want, kinds, tc.name)
	}
}

// The message says which check fails: Toft's HTTP check, with its probe's
// reason, or Docker's, and whose check Docker runs.
func TestConditionsSayWhyTheHealthCheckFails(t *testing.T) {
	probed := []service.ContainerState{{Name: "web-blue", State: "running", Health: "unhealthy", HealthDetail: "GET /health: HTTP 500"}}
	assert.Equal(t, "health check failing: web-blue (Toft's HTTP check: GET /health: HTTP 500)",
		conditions(probed, service.ContainerHealthImage)[KindServiceUnhealthy])

	docker := []service.ContainerState{{Name: "worker-blue", State: "running", Health: "unhealthy"}}
	for check, want := range map[string]string{
		service.ContainerHealthImage:   "health check failing: worker-blue (Docker's check: the image's HEALTHCHECK)",
		service.ContainerHealthCommand: "health check failing: worker-blue (Docker's check: the service's container check command)",
		service.ContainerHealthOff:     "health check failing: worker-blue (Docker's check: the image's HEALTHCHECK, which the next deploy turns off)",
		"":                             "health check failing: worker-blue (Docker's check: the compose file's or the image's HEALTHCHECK)",
	} {
		assert.Equal(t, want, conditions(docker, check)[KindServiceUnhealthy], check)
	}
}

func TestFullestDisk(t *testing.T) {
	d, ok := fullestDisk([]service.DiskUsage{{Mount: "/", UsedPercent: 40}, {Mount: "/var/lib/docker", UsedPercent: 91}})
	assert.True(t, ok)
	assert.Equal(t, "/var/lib/docker", d.Mount)
	_, ok = fullestDisk(nil)
	assert.False(t, ok)
}
