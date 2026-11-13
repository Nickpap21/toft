package rollout

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeHost records a rollout's calls in order. healthFails is how many
// health attempts fail before one passes.
type fakeHost struct {
	calls       []string
	previous    []string
	healthFails int
	execErr     error
	// routingChanged is what Start reports; drains records FinishSwitch's
	// drain argument per call.
	routingChanged bool
	drains         []bool
	notHealthy     error // what AwaitHealthy returns
}

func (h *fakeHost) AwaitHealthy(context.Context, string, bool) error {
	h.calls = append(h.calls, "await healthy")
	return h.notHealthy
}

func (h *fakeHost) Pull(_ context.Context, image string, _ *RegistryAuth, _ func(string)) error {
	h.calls = append(h.calls, "pull "+image)
	return nil
}

func (h *fakeHost) RunOnce(_ context.Context, _ Spec, argv []string, resume bool, _ func(string)) error {
	if resume {
		h.calls = append(h.calls, "await")
		return nil
	}
	h.calls = append(h.calls, "once "+argv[0])
	return nil
}

func (h *fakeHost) Start(_ context.Context, _ Spec, sw *SlotSwitch) error {
	h.calls = append(h.calls, "start")
	sw.New, sw.Previous, sw.RoutingChanged = slotGreen, h.previous, h.routingChanged
	if sw.Chosen != nil {
		sw.Chosen()
	}
	return nil
}

func (h *fakeHost) WaitReady(context.Context, string, int, func(string)) error { return nil }

func (h *fakeHost) Health(context.Context, string, int, HealthCheck) error {
	h.calls = append(h.calls, "health")
	if h.healthFails > 0 {
		h.healthFails--
		return errors.New("exit status 7")
	}
	return nil
}

func (h *fakeHost) Slots(context.Context, string) ([]string, error) {
	return append([]string{slotGreen}, h.previous...), nil
}

func (h *fakeHost) Exec(_ context.Context, name, _ string, _ func(string)) error {
	h.calls = append(h.calls, "exec "+name)
	return h.execErr
}

func (h *fakeHost) FinishSwitch(_ context.Context, _, keep string, drain bool) error {
	h.calls = append(h.calls, "finish "+keep)
	h.drains = append(h.drains, drain)
	return nil
}

func (h *fakeHost) AbortSwitch(_ context.Context, _, name string) error {
	h.calls = append(h.calls, "abort "+name)
	return nil
}

func (h *fakeHost) RestorePrevious(context.Context, string, []string) error { return nil }

func TestRunSteps(t *testing.T) {
	old := HealthInterval
	HealthInterval = 0
	t.Cleanup(func() { HealthInterval = old })
	spec := Spec{
		Slot:       Slot{ServiceID: slotSvcID, Image: "app:2"},
		Auth:       &RegistryAuth{Password: "p"},
		Migrate:    "/server migrate",
		Health:     &HealthCheck{Path: "/", Retries: 3},
		PostDeploy: []PostDeploy{{Command: "./seed"}, {Container: slotBlue, Command: "true"}},
	}

	h := &fakeHost{previous: []string{slotBlue}, healthFails: 2}
	res := Run(context.Background(), h, spec, Hooks{})
	assert.Equal(t, Result{Changed: true, New: slotGreen}, res)
	assert.Equal(t, []string{"pull app:2", "once /server", "start", "health", "health", "health",
		"await healthy", "exec " + slotGreen, "exec " + slotBlue, "finish " + slotGreen}, h.calls,
		"a build's image is pulled for the migrate container only when it needs credentials; the health check retries")

	h = &fakeHost{previous: []string{slotBlue}, healthFails: 3}
	res = Run(context.Background(), h, spec, Hooks{})
	assert.Equal(t, StepHealth, res.Step)
	assert.True(t, res.PreviousServes)
	assert.Equal(t, "abort "+slotGreen, h.calls[len(h.calls)-1])

	h = &fakeHost{previous: []string{slotBlue}, execErr: errors.New("exit status 1")}
	res = Run(context.Background(), h, spec, Hooks{Abandon: func() bool { return true }})
	assert.Equal(t, Result{Step: StepPostDeploy, Err: "exit status 1", Changed: true, New: slotGreen}, res,
		"a panel shutting down leaves the new slot for its recovery")
	assert.Equal(t, "exec "+slotGreen, h.calls[len(h.calls)-1])

	spec.PostDeploy = []PostDeploy{{Container: "paas-postgres", Command: "psql"}}
	h = &fakeHost{previous: []string{slotBlue}}
	res = Run(context.Background(), h, spec, Hooks{})
	assert.Equal(t, StepPostDeploy, res.Step)
	assert.NotContains(t, h.calls, "exec paas-postgres", "never another service's container")
}

func TestRunSavesItsProgress(t *testing.T) {
	var saved []Progress
	spec := Spec{Slot: Slot{ServiceID: slotSvcID, Image: "app:2"}, Pull: true, Migrate: "/server migrate",
		Health: &HealthCheck{Path: "/", Retries: 1}, PostDeploy: []PostDeploy{{Command: "./seed"}}}
	res := Run(context.Background(), &fakeHost{previous: []string{slotBlue}}, spec, Hooks{Save: func(p Progress) { saved = append(saved, p) }})
	assert.Equal(t, Result{Changed: true, New: slotGreen}, res)

	var steps []string
	for _, p := range saved {
		steps = append(steps, p.Step)
	}
	assert.Equal(t, []string{StepPull, StepMigrate, StepStart, StepStart, StepHealth, StepPostDeploy, StepSwitch, StepSwitch}, steps,
		"each step before it starts, the slots Start chose, and the end")
	assert.Equal(t, slotGreen, saved[3].Switch.New, "recorded before Start stops or creates anything")
	assert.Equal(t, []string{slotBlue}, saved[3].Switch.Previous)
	assert.Equal(t, &res, saved[len(saved)-1].Result)
}

func TestResume(t *testing.T) {
	spec := Spec{DeploymentID: "dep-2", Slot: Slot{ServiceID: slotSvcID, Image: "app:2"}, Pull: true, Migrate: "/server migrate",
		Health: &HealthCheck{Path: "/", Retries: 1}, PostDeploy: []PostDeploy{{Command: "./seed"}}}
	chosen := SlotSwitch{New: slotGreen, Previous: []string{slotBlue}}
	cases := []struct {
		step  string
		calls []string
		step0 string // the failed step, "" when it succeeds
	}{
		{StepPull, []string{"pull app:2", "once /server", "start", "health", "await healthy", "exec " + slotGreen, "finish " + slotGreen}, ""},
		{StepMigrate, []string{"await", "start", "health", "await healthy", "exec " + slotGreen, "finish " + slotGreen}, ""},
		{StepStart, []string{"start", "health", "await healthy", "exec " + slotGreen, "finish " + slotGreen}, ""},
		{StepHealth, []string{"health", "await healthy", "exec " + slotGreen, "finish " + slotGreen}, ""},
		{StepPostDeploy, []string{"abort " + slotGreen}, StepPostDeploy},
		{StepSwitch, []string{"await healthy", "finish " + slotGreen}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.step, func(t *testing.T) {
			h := &fakeHost{previous: []string{slotBlue}}
			p := Progress{Step: tc.step}
			if stepOrder[tc.step] > stepOrder[StepStart] {
				p.Switch = chosen
			}
			res := Resume(context.Background(), h, spec, p, Hooks{})
			assert.Equal(t, tc.calls, h.calls)
			assert.Equal(t, tc.step0, res.Step)
			if tc.step0 != "" {
				assert.True(t, res.PreviousServes, "the post-deploy commands never run twice: the previous slot serves")
			}
		})
	}
}

// With the agent writing Traefik's routes (D-113), the new slot is routed
// once its checks and post-deploy commands passed and before the previous
// one is retired; a route Traefik won't serve fails the rollout and the
// previous slot keeps serving.
func TestRunRoutesTheNewSlotBeforeRetiringThePrevious(t *testing.T) {
	spec := Spec{Slot: Slot{ServiceID: slotSvcID, Image: "app:2"}, PostDeploy: []PostDeploy{{Command: "./seed"}}}
	h := &fakeHost{previous: []string{slotBlue}}
	route := func(_ context.Context, slot string, _ func(string)) error {
		h.calls = append(h.calls, "route "+slot)
		return nil
	}
	res := Run(context.Background(), h, spec, Hooks{Route: route})
	assert.Equal(t, Result{Changed: true, New: slotGreen}, res)
	assert.Equal(t, []string{"start", "exec " + slotGreen, "route " + slotGreen, "finish " + slotGreen}, h.calls)

	h = &fakeHost{previous: []string{slotBlue}}
	res = Run(context.Background(), h, spec, Hooks{Route: func(context.Context, string, func(string)) error {
		return errors.New("router paas-aaaaaaaa-0 is disabled")
	}})
	assert.Equal(t, StepSwitch, res.Step)
	assert.True(t, res.PreviousServes)
	assert.Equal(t, "abort "+slotGreen, h.calls[len(h.calls)-1])
}

// The previous slot drains only where it serves beside the new one through
// its labels: not on a first deploy, nor when the routing changed (Traefik
// serves neither), nor where the route moves in a file (D-113).
func TestRunDrainsWhereBothSlotsServe(t *testing.T) {
	spec := Spec{Slot: Slot{ServiceID: slotSvcID, Image: "app:2"}}
	for _, c := range []struct {
		name  string
		host  *fakeHost
		hooks Hooks
		want  []bool
	}{
		{"redeploy", &fakeHost{previous: []string{slotBlue}}, Hooks{}, []bool{true}},
		{"first deploy", &fakeHost{}, Hooks{}, []bool{false}},
		{"routing changed", &fakeHost{previous: []string{slotBlue}, routingChanged: true}, Hooks{}, []bool{false}},
		{"file routes", &fakeHost{previous: []string{slotBlue}}, Hooks{Route: func(context.Context, string, func(string)) error { return nil }}, []bool{false}},
	} {
		res := Run(context.Background(), c.host, spec, c.hooks)
		assert.Equal(t, Result{Changed: true, New: slotGreen}, res, c.name)
		assert.Equal(t, c.want, c.host.drains, c.name)
	}
}

// A new slot Docker never reports healthy fails the deploy before anything
// is retired: Traefik would not route to it.
func TestRunFailsANewSlotTraefikWouldNotRoute(t *testing.T) {
	h := &fakeHost{previous: []string{slotBlue}, notHealthy: ErrNewSlotNotHealthy}
	res := Run(context.Background(), h, Spec{Slot: Slot{ServiceID: slotSvcID, Image: "app:2"}}, Hooks{})
	assert.Equal(t, StepHealth, res.Step)
	assert.True(t, res.PreviousServes)
	assert.Equal(t, []string{"start", "await healthy", "abort " + slotGreen}, h.calls)
}
