package rollout

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestReuseBasicAuth(t *testing.T) {
	ba := BasicAuth{User: "admin", Password: "s3cret"}
	key := BasicAuthUsersLabel(slotSvcID)
	serving := []container.Summary{
		{Labels: map[string]string{"paas.service_id": slotSvcID}},
		{Labels: map[string]string{key: HtpasswdEntry("admin", "s3cret")}},
	}

	t.Run("same user and password reuse the serving entry", func(t *testing.T) {
		labels := freshLabels(ba)
		require.NotEqual(t, serving[1].Labels[key], labels[key], "bcrypt salts every entry")
		reuseBasicAuth(labels, slotSvcID, serving, ba)
		assert.Equal(t, serving[1].Labels[key], labels[key])
	})

	t.Run("a new password gets a new entry", func(t *testing.T) {
		changed := BasicAuth{User: "admin", Password: "other"}
		labels := freshLabels(changed)
		want := labels[key]
		reuseBasicAuth(labels, slotSvcID, serving, changed)
		assert.Equal(t, want, labels[key])
		user, hash, _ := strings.Cut(labels[key], ":")
		assert.Equal(t, "admin", user)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("other")))
	})

	t.Run("a new user gets a new entry", func(t *testing.T) {
		changed := BasicAuth{User: "root", Password: "s3cret"}
		labels := freshLabels(changed)
		want := labels[key]
		reuseBasicAuth(labels, slotSvcID, serving, changed)
		assert.Equal(t, want, labels[key])
	})

	t.Run("no basic auth adds none", func(t *testing.T) {
		labels := freshLabels(BasicAuth{})
		reuseBasicAuth(labels, slotSvcID, serving, BasicAuth{})
		_, ok := labels[key]
		assert.False(t, ok)
	})
}

// freshLabels are the basic-auth label the panel gives a new slot: a fresh
// entry, or none without basic auth.
func freshLabels(ba BasicAuth) map[string]string {
	if ba.User == "" {
		return map[string]string{}
	}
	return map[string]string{BasicAuthUsersLabel(slotSvcID): HtpasswdEntry(ba.User, ba.Password)}
}

func TestFinishSwitch(t *testing.T) {
	legacy := ContainerNameForService(slotSvcID)

	t.Run("retires everything of the service but the new slot", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
		f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		f.Add(BlueContainerName(slotSvcID, "old-name", ""), container.StateExited, svcLabels(slotSvcID))
		f.Add(legacy, container.StateRunning, nil)
		f.Add("paas-other-9999ffff-blue", container.StateRunning, svcLabels(otherSvc))
		f.Add(legacy+"-unrelated", container.StateRunning, nil) // a name filter would match it

		require.NoError(t, FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, true))
		assert.ElementsMatch(t, []string{slotGreen, "paas-other-9999ffff-blue", legacy + "-unrelated"}, f.Names())
	})

	t.Run("removes nothing when the new slot is not running", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
		f.Add(slotGreen, container.StateExited, svcLabels(slotSvcID))

		err := FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not running")
		assert.Empty(t, f.RecordedOps())
	})

	t.Run("a service with no name keeps its slot", func(t *testing.T) {
		// paas-svc-<id> is a prefix of the slot names of a service with no name.
		f := fakedocker.New(t)
		keep := GreenContainerName(slotSvcID, "", "")
		f.Add(BlueContainerName(slotSvcID, "", ""), container.StateRunning, svcLabels(slotSvcID))
		f.Add(keep, container.StateRunning, svcLabels(slotSvcID))

		require.NoError(t, FinishSwitch(context.Background(), f.Client(), slotSvcID, keep, true))
		assert.Equal(t, []string{keep}, f.Names())
	})
}

// With drain, a previous slot that drains leaves Traefik's rotation before
// it stops; a slot created before draining stops as before. A switch that
// is called off clears the flag in the slots that stay.
func TestFinishSwitchDrains(t *testing.T) {
	settle, poll := traefikSettle, drainPoll
	traefikSettle, drainPoll = time.Millisecond, time.Millisecond
	t.Cleanup(func() { traefikSettle, drainPoll = settle, poll })
	hc, _ := drainHealthcheck(nil, nil)
	add := func(f *fakedocker.Docker, name string) {
		c := f.Add(name, container.StateRunning, svcLabels(slotSvcID))
		c.Healthcheck, c.Tmpfs, c.Health = hc, map[string]string{drainDir: ""}, container.Healthy
	}

	t.Run("drains, then stops", func(t *testing.T) {
		f := fakedocker.New(t)
		add(f, slotBlue)
		add(f, slotGreen)
		require.NoError(t, FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, true))
		assert.Equal(t, []string{"drain " + slotBlue, "stop " + slotBlue, "remove " + slotBlue}, f.RecordedOps())
	})

	t.Run("without drain, stops at once", func(t *testing.T) {
		f := fakedocker.New(t)
		add(f, slotBlue)
		add(f, slotGreen)
		require.NoError(t, FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, false))
		assert.Equal(t, []string{"stop " + slotBlue, "remove " + slotBlue}, f.RecordedOps())
	})

	t.Run("a slot from before draining stops as before", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
		add(f, slotGreen)
		require.NoError(t, FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, true))
		assert.Equal(t, []string{"stop " + slotBlue, "remove " + slotBlue}, f.RecordedOps())
	})

	t.Run("an image's own check of the same shape is not ours", func(t *testing.T) {
		f := fakedocker.New(t)
		add(f, slotBlue)
		f.ByName(slotBlue).Tmpfs = nil
		add(f, slotGreen)
		require.NoError(t, FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, true))
		assert.Equal(t, []string{"stop " + slotBlue, "remove " + slotBlue}, f.RecordedOps())
	})

	t.Run("AbortSwitch clears the flag in the slots that stay", func(t *testing.T) {
		f := fakedocker.New(t)
		add(f, slotBlue)
		add(f, slotGreen)
		require.NoError(t, AbortSwitch(context.Background(), f.Client(), slotSvcID, slotGreen))
		assert.Equal(t, []string{"undrain " + slotBlue, "stop " + slotGreen, "remove " + slotGreen}, f.RecordedOps())
	})
}

func TestAbortSwitch(t *testing.T) {
	t.Run("removes the new slot and nothing else", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
		f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))

		require.NoError(t, AbortSwitch(context.Background(), f.Client(), slotSvcID, slotGreen))
		assert.Equal(t, []string{"stop " + slotGreen, "remove " + slotGreen}, f.RecordedOps())
		assert.Equal(t, []string{slotBlue}, f.Names())
	})

	t.Run("a slot that is gone is not an error", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
		require.NoError(t, AbortSwitch(context.Background(), f.Client(), slotSvcID, slotGreen))
		assert.Empty(t, f.RecordedOps())
	})

	t.Run("never another service's container", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add("paas-other-9999ffff-blue", container.StateRunning, svcLabels(otherSvc))
		require.NoError(t, AbortSwitch(context.Background(), f.Client(), slotSvcID, "paas-other-9999ffff-blue"))
		assert.Empty(t, f.RecordedOps())
	})
}

func TestPickActiveSlot(t *testing.T) {
	blue := container.Summary{Created: 100, Labels: map[string]string{LabelDeploymentID: "dep-1"}}
	green := container.Summary{Created: 200, Labels: map[string]string{LabelDeploymentID: "dep-2"}}
	both := map[string]container.Summary{"blue": blue, "green": green}

	assert.Equal(t, "", PickActiveSlot(nil, ""))
	assert.Equal(t, "blue", PickActiveSlot(map[string]container.Summary{"blue": blue}, "dep-2"))
	assert.Equal(t, "green", PickActiveSlot(map[string]container.Summary{"green": green}, ""))
	assert.Equal(t, "green", PickActiveSlot(both, ""), "the newer one")
	assert.Equal(t, "green", PickActiveSlot(both, "dep-unknown"), "the newer one")
	assert.Equal(t, "blue", PickActiveSlot(both, "dep-1"), "the active deployment's")
	green.Created = blue.Created
	assert.Equal(t, "blue", PickActiveSlot(map[string]container.Summary{"blue": blue, "green": green}, ""), "a tie is settled the same way every time")
}

func TestReuseBasicAuthSkipsCostlyHashes(t *testing.T) {
	ba := BasicAuth{User: "admin", Password: "s3cret"}
	key := BasicAuthUsersLabel(slotSvcID)
	costly, err := bcrypt.GenerateFromPassword([]byte("s3cret"), htpasswdCost+1)
	require.NoError(t, err)
	serving := []container.Summary{{Labels: map[string]string{key: "admin:" + string(costly)}}}

	labels := freshLabels(ba)
	want := labels[key]
	reuseBasicAuth(labels, slotSvcID, serving, ba)
	assert.Equal(t, want, labels[key], "a hash the platform would not write is not checked, so not reused")
}

func TestFinishSwitchReportsANewSlotThatIsNotRunning(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
	f.Add(slotGreen, container.StateExited, svcLabels(slotSvcID))

	err := FinishSwitch(context.Background(), f.Client(), slotSvcID, slotGreen, true)
	require.ErrorIs(t, err, ErrNewSlotNotRunning)
	assert.Contains(t, err.Error(), slotGreen)
}

func TestRestorePrevious(t *testing.T) {
	t.Run("starts the named stopped slots, after the new one is gone", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateExited, svcLabels(slotSvcID))
		f.Add(BlueContainerName(slotSvcID, "old-name", ""), container.StateExited, svcLabels(slotSvcID))

		require.NoError(t, RestorePrevious(context.Background(), f.Client(), slotSvcID, []string{slotBlue}))
		assert.Equal(t, []string{"start " + slotBlue}, f.RecordedOps())
	})

	t.Run("a running one is left alone", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, svcLabels(slotSvcID))
		require.NoError(t, RestorePrevious(context.Background(), f.Client(), slotSvcID, []string{slotBlue}))
		assert.Empty(t, f.RecordedOps())
	})

	t.Run("one that is gone is an error", func(t *testing.T) {
		f := fakedocker.New(t)
		err := RestorePrevious(context.Background(), f.Client(), slotSvcID, []string{slotBlue})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gone")
	})

	t.Run("never another service's container, nor one only labelled", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add("paas-other-9999ffff-blue", container.StateExited, svcLabels(otherSvc))
		f.Add("tenant-db", container.StateExited, svcLabels(slotSvcID))
		require.Error(t, RestorePrevious(context.Background(), f.Client(), slotSvcID, []string{"paas-other-9999ffff-blue", "tenant-db"}))
		assert.Empty(t, f.RecordedOps())
	})

	t.Run("a start that fails is an error", func(t *testing.T) {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateExited, svcLabels(slotSvcID))
		f.StartErr = true
		require.Error(t, RestorePrevious(context.Background(), f.Client(), slotSvcID, []string{slotBlue}))
	})
}

// fastReady shortens the readiness wait for a test.
func fastReady(t *testing.T) {
	oldTimeout, oldSettle, oldPoll := recreateReadyTimeout, readySettle, readyPoll
	recreateReadyTimeout, readySettle, readyPoll = 200*time.Millisecond, 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { recreateReadyTimeout, readySettle, readyPoll = oldTimeout, oldSettle, oldPoll })
}

func TestWaitReady(t *testing.T) {
	var logged []string
	logFn := func(l string) { logged = append(logged, l) }

	t.Run("ready once it accepts connections on the port and has kept running", func(t *testing.T) {
		fastReady(t)
		logged = nil
		f := fakedocker.New(t)
		c := f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		c.ListensFrom = 3
		started := time.Now()
		require.NoError(t, WaitReady(context.Background(), f.Client(), slotGreen, 3306, logFn))
		assert.GreaterOrEqual(t, time.Since(started), readySettle)
		assert.GreaterOrEqual(t, c.Probes, 3)
		require.Len(t, logged, 1)
		assert.Contains(t, logged[0], "Accepting connections")
	})

	t.Run("one that accepts connections, then exits, fails", func(t *testing.T) {
		// MySQL opens its port before it reads all its options.
		fastReady(t)
		readySettle = time.Minute
		f := fakedocker.New(t)
		c := f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		c.ListensFrom, c.ExitsAfter = 1, 2
		err := WaitReady(context.Background(), f.Client(), slotGreen, 3306, logFn)
		var nr *NotReadyError
		require.ErrorAs(t, err, &nr)
		assert.Equal(t, "the new container exited (code 1) before it was ready", nr.Reason)
	})

	t.Run("a container that exited fails at once, with its last log lines", func(t *testing.T) {
		fastReady(t)
		f := fakedocker.New(t)
		c := f.Add(slotGreen, container.StateExited, svcLabels(slotSvcID))
		c.ExitCode = 1
		err := WaitReady(context.Background(), f.Client(), slotGreen, 3306, logFn)
		var nr *NotReadyError
		require.ErrorAs(t, err, &nr)
		assert.Equal(t, "the new container exited (code 1) before it was ready", nr.Reason)
		assert.Equal(t, []string{"ran " + slotGreen}, nr.LastLines)
		assert.Zero(t, c.Probes)
	})

	t.Run("a container its restart policy brought back fails at once", func(t *testing.T) {
		fastReady(t)
		f := fakedocker.New(t)
		c := f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		c.RestartCount, c.ListensFrom = 1, 1
		err := WaitReady(context.Background(), f.Client(), slotGreen, 3306, logFn)
		var nr *NotReadyError
		require.ErrorAs(t, err, &nr)
		assert.Contains(t, nr.Reason, "restarted")
	})

	t.Run("nothing listening times out", func(t *testing.T) {
		fastReady(t)
		f := fakedocker.New(t)
		f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		err := WaitReady(context.Background(), f.Client(), slotGreen, 3306, logFn)
		var nr *NotReadyError
		require.ErrorAs(t, err, &nr)
		assert.Equal(t, "the new container did not accept connections on port 3306 within 200ms", nr.Reason)
		assert.NotEmpty(t, nr.LastLines)
	})

	t.Run("without a port it must keep running", func(t *testing.T) {
		fastReady(t)
		f := fakedocker.New(t)
		c := f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		require.NoError(t, WaitReady(context.Background(), f.Client(), slotGreen, 0, logFn))
		assert.Zero(t, c.Probes)
	})

	t.Run("without the probe tool it must keep running, and says so", func(t *testing.T) {
		fastReady(t)
		logged = nil
		f := fakedocker.New(t)
		f.NoProbe = true
		c := f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		require.NoError(t, WaitReady(context.Background(), f.Client(), slotGreen, 3306, logFn))
		assert.Equal(t, 1, c.Probes)
		require.Len(t, logged, 1)
		assert.Contains(t, logged[0], "no probe tool")
	})

	t.Run("a cancelled deploy stops waiting", func(t *testing.T) {
		fastReady(t)
		f := fakedocker.New(t)
		f.Add(slotGreen, container.StateRunning, svcLabels(slotSvcID))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, WaitReady(ctx, f.Client(), slotGreen, 3306, logFn), context.Canceled)
	})
}

func startSlot(f *fakedocker.Docker, deploymentID string, sw *SlotSwitch) error {
	labels := map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: deploymentID}
	return StartSlot(context.Background(), f.Client(), NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""),
		Slot{ServiceID: slotSvcID, ServiceName: "web", Image: "app:2", Labels: labels}, sw, nil)
}

// Stop then start lists the slots it stops, and chooses the new one, before
// it stops anything: a restart after the stop still knows what to restore.
func TestStartSlotRecordsTheSlotsBeforeItStopsThem(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateRunning, map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-1"})
	var atChoice SlotSwitch
	sw := &SlotSwitch{Recreate: true}
	sw.Chosen = func() {
		atChoice = *sw
		assert.Equal(t, container.StateRunning, f.ByName(slotBlue).State, "nothing stopped yet")
		assert.Nil(t, f.ByName(slotGreen), "nothing created yet")
	}
	require.NoError(t, startSlot(f, "dep-2", sw))
	assert.Equal(t, slotGreen, atChoice.New)
	assert.Equal(t, []string{slotBlue}, atChoice.Stopped)
	assert.Equal(t, container.StateExited, f.ByName(slotBlue).State)
}

// Resumed after a restart, StartSlot keeps the slot it chose and reuses the
// container this deployment created; the stopped previous slot stays the
// one to restore.
func TestStartSlotResumesWithTheSlotItChose(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(slotBlue, container.StateExited, map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-1"})
	f.Add(slotGreen, container.StateCreated, map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-2"})
	sw := &SlotSwitch{Recreate: true, New: slotGreen, Previous: []string{slotBlue}, Stopped: []string{slotBlue}}
	require.NoError(t, startSlot(f, "dep-2", sw))
	assert.Equal(t, []string{"start " + slotGreen}, f.RecordedOps(), "neither removed nor created again")
	assert.Equal(t, []string{slotBlue}, sw.Stopped)
	assert.Equal(t, []string{slotBlue}, sw.Previous)

	// A container of another deployment under the chosen name is replaced.
	f = fakedocker.New(t)
	f.Add(slotGreen, container.StateExited, map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-1"})
	require.NoError(t, startSlot(f, "dep-2", &SlotSwitch{New: slotGreen}))
	assert.Equal(t, "dep-2", f.LabelsOf(slotGreen)[LabelDeploymentID])
}

// On an agent server (NetConfig.LocalLogs) a slot logs to Docker's local
// driver, capped, whatever its Loki; elsewhere a Loki URL keeps the Loki
// plugin with its labels, and no Loki keeps Docker's default (D-113).
func TestStartSlotLogDriver(t *testing.T) {
	start := func(cfg NetConfig, loki string) container.LogConfig {
		f := fakedocker.New(t)
		labels := map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-1"}
		require.NoError(t, StartSlot(context.Background(), f.Client(), cfg, netplan.LegacyPlan(slotSvcID, "web", "", ""),
			Slot{ServiceID: slotSvcID, ServiceName: "web", Image: "app:2", Labels: labels, LokiURL: loki}, &SlotSwitch{}, nil))
		return f.ByName(slotBlue).LogConfig
	}

	local := start(NetConfig{LocalLogs: true}, "https://loki.example.com")
	assert.Equal(t, container.LogConfig{Type: "local", Config: map[string]string{"max-size": "20m", "max-file": "5"}}, local)

	plugin := start(NetConfig{}, "https://loki.example.com")
	assert.Equal(t, "loki", plugin.Type)
	assert.Equal(t, "https://loki.example.com/loki/api/v1/push", plugin.Config["loki-url"])
	assert.Equal(t, "container_name="+slotBlue+",service_id="+slotSvcID, plugin.Config["loki-external-labels"])
	assert.Equal(t, "non-blocking", plugin.Config["mode"], "an app never waits on Loki")

	assert.Equal(t, container.LogConfig{}, start(NetConfig{}, ""))
}

// The catalog Loki is published on its server's 127.0.0.1 only, never on
// every address (D-148); other slots publish nothing.
func TestStartSlotLoopbackPort(t *testing.T) {
	start := func(lb Loopback) *fakedocker.Container {
		f := fakedocker.New(t)
		labels := map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-1"}
		require.NoError(t, StartSlot(context.Background(), f.Client(), NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""),
			Slot{ServiceID: slotSvcID, ServiceName: "web", Image: "app:2", Labels: labels, Loopback: lb}, &SlotSwitch{}, nil))
		return f.ByName(slotBlue)
	}
	c := start(Loopback{Port: 39100, Target: 3100})
	assert.Equal(t, nat.PortMap{"3100/tcp": {{HostIP: "127.0.0.1", HostPort: "39100"}}}, c.PortBindings)
	assert.Empty(t, start(Loopback{}).PortBindings)
}

// An entry at the old cost (4) gets the platform's where that costs no time
// without routes, and is kept where Traefik would drop the service's routes
// while both slots run (F7).
func TestStartSlotReplacesWeakBasicAuthWithoutAGap(t *testing.T) {
	ba := BasicAuth{User: "admin", Password: "s3cret"}
	key := BasicAuthUsersLabel(slotSvcID)
	rule := "traefik.http.routers.paas-1234abcd-0.rule"
	h, err := bcrypt.GenerateFromPassword([]byte("s3cret"), 4)
	require.NoError(t, err)
	weak := "admin:" + string(h)

	start := func(sw *SlotSwitch, servingRule string) string {
		f := fakedocker.New(t)
		f.Add(slotBlue, container.StateRunning, map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-1", key: weak, rule: servingRule})
		labels := map[string]string{LabelServiceID: slotSvcID, LabelDeploymentID: "dep-2", key: HtpasswdEntry("admin", "s3cret"), rule: "Host(`a.example.com`)"}
		require.NoError(t, StartSlot(context.Background(), f.Client(), NetConfig{}, netplan.LegacyPlan(slotSvcID, "web", "", ""),
			Slot{ServiceID: slotSvcID, ServiceName: "web", Image: "app:2", Labels: labels, BasicAuth: ba}, sw, nil))
		return f.LabelsOf(slotGreen)[key]
	}
	platformCost := func(entry string) bool {
		_, hash, _ := strings.Cut(entry, ":")
		cost, err := bcrypt.Cost([]byte(hash))
		return err == nil && cost == htpasswdCost && bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")) == nil
	}

	assert.Equal(t, weak, start(&SlotSwitch{}, "Host(`a.example.com`)"), "both slots serve: kept")
	assert.True(t, platformCost(start(&SlotSwitch{Recreate: true}, "Host(`a.example.com`)")), "stop then start")
	assert.True(t, platformCost(start(&SlotSwitch{FileRoutes: true}, "Host(`a.example.com`)")), "route files")
	assert.Equal(t, weak, start(&SlotSwitch{}, "Host(`b.example.com`)"), "beside another routing change: kept")
}
