package rollout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainHealthcheck(t *testing.T) {
	for _, image := range []*container.HealthConfig{nil, {Test: []string{"NONE"}}, {Test: []string{"CMD-SHELL"}}} {
		hc, ok := drainHealthcheck(image, nil)
		require.True(t, ok)
		assert.Equal(t, []string{"CMD", busybox, "sh", "-c", "! " + drained}, hc.Test, "no check of the image's own: the flag alone")
		assert.Equal(t, drainInterval, hc.Interval)
	}

	hc, ok := drainHealthcheck(&container.HealthConfig{Test: []string{"CMD", "curl", "-f", "http://localhost/"}}, nil)
	require.True(t, ok)
	assert.Equal(t, []string{"CMD", busybox, "sh", "-c", drainScript, "toft-drain", "5", "30", "3", "30", "curl", "-f", "http://localhost/"}, hc.Test,
		"Docker's defaults; the start interval until the check first passes, start period or not")
	assert.Equal(t, 30*time.Second+drainTimeout, hc.Timeout)
	assert.Equal(t, 90*time.Second, hc.StartPeriod, "starting until the image's retries would have run out")

	hc, _ = drainHealthcheck(&container.HealthConfig{Test: []string{"CMD-SHELL", "pg_isready || exit 1"},
		Interval: 10 * time.Second, Timeout: 1500 * time.Millisecond, Retries: 5, StartPeriod: time.Minute, StartInterval: time.Second}, nil)
	assert.Equal(t, []string{"1", "10", "5", "2", "/bin/sh", "-c", "pg_isready || exit 1"}, hc.Test[6:],
		"the shell form in Docker's default shell; seconds rounded up")
	assert.Equal(t, time.Minute+50*time.Second, hc.StartPeriod)

	hc, _ = drainHealthcheck(&container.HealthConfig{Test: []string{"CMD-SHELL", "set -o pipefail; x | y"}}, []string{"/bin/bash", "-o", "pipefail", "-c"})
	assert.Equal(t, []string{"/bin/bash", "-o", "pipefail", "-c", "set -o pipefail; x | y"}, hc.Test[10:], "the image's SHELL, as Docker runs it")

	_, ok = drainHealthcheck(&container.HealthConfig{Test: []string{"CMD-NEW", "x"}}, nil)
	assert.False(t, ok, "a form this doesn't know keeps the image's check")
}

// drainScript, run by the host's sh with its own date, cat, head, find and
// sleep in place of busybox's, against a state directory of its own.
func TestDrainScript(t *testing.T) {
	dir := t.TempDir()
	script := strings.NewReplacer(busybox+" ", "", "B="+busybox, "B=", "D="+drainDir, "D="+dir, drainFlag, filepath.Join(dir, "now")).Replace(drainScript)
	run := func(gap, iv, re, to string, argv ...string) int {
		cmd := exec.Command("sh", append([]string{"-c", script, "toft-drain", gap, iv, re, to}, argv...)...)
		if err := cmd.Run(); err != nil {
			return cmd.ProcessState.ExitCode()
		}
		return 0
	}
	again := func() { require.NoError(t, os.Remove(filepath.Join(dir, "last"))) } // the image's interval has passed

	assert.Equal(t, 1, run("30", "30", "2", "5", "false"), "never passed yet: starting")
	assert.Equal(t, 1, run("30", "30", "2", "5", "true"), "within the interval, the image's check doesn't run again")
	again()
	assert.Equal(t, 0, run("30", "30", "2", "5", "true"))
	again()
	assert.Equal(t, 0, run("30", "30", "2", "5", "false"), "one failure is below the image's retries")
	again()
	assert.Equal(t, 1, run("30", "30", "2", "5", "false"), "the second in a row is not")
	again()
	assert.Equal(t, 0, run("30", "30", "2", "5", "true"), "one pass is healthy again")
	again()
	start := time.Now()
	assert.Equal(t, 1, run("30", "30", "1", "1", "sleep", "5"), "a check past its timeout is a failure")
	assert.Less(t, time.Since(start), 3*time.Second, "and is stopped at its timeout")
	again()
	assert.Equal(t, 0, run("30", "30", "2", "5", "true"))

	flag := filepath.Join(dir, "now")
	again()
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.WriteFile(flag, nil, 0o600)
	}()
	start = time.Now()
	assert.Equal(t, 1, run("30", "30", "9", "20", "sleep", "10"), "drained while the image's check runs")
	assert.Less(t, time.Since(start), 3*time.Second, "seen without waiting for the check")
	assert.Equal(t, 1, run("30", "30", "9", "5", "true"), "drained")
	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(flag, old, old))
	assert.Equal(t, 0, run("30", "30", "9", "5", "true"), "a flag over a minute old no longer counts")
}

// A new slot with the drain check is awaited until Docker reports it
// healthy; one that never is fails, and one without the check isn't waited for.
func TestAwaitHealthy(t *testing.T) {
	settle, poll := traefikSettle, drainPoll
	traefikSettle, drainPoll = time.Millisecond, time.Millisecond
	t.Cleanup(func() { traefikSettle, drainPoll = settle, poll })
	f := fakedocker.New(t)
	hc, _ := drainHealthcheck(nil, nil)
	for name, health := range map[string]container.HealthStatus{"ok": container.Healthy, "never": container.Unhealthy} {
		c := f.Add(name, container.StateRunning, nil)
		c.Healthcheck, c.Tmpfs, c.Health = hc, map[string]string{drainDir: ""}, health
	}
	f.ByName("never").Healthcheck = &container.HealthConfig{Test: hc.Test, Interval: time.Millisecond, Timeout: time.Millisecond, Retries: 1}
	f.Add("plain", container.StateRunning, nil)

	require.NoError(t, AwaitHealthy(context.Background(), f.Client(), "ok", true))
	require.NoError(t, AwaitHealthy(context.Background(), f.Client(), "plain", true))
	assert.ErrorIs(t, AwaitHealthy(context.Background(), f.Client(), "never", true), ErrNewSlotNotHealthy)
}

// A routed slot gets the drain check and its tmpfs where Traefik reads
// labels and the probe is mounted; an unrouted one, or one routed by a
// file, keeps the image's.
func TestRoutedSlotsDrain(t *testing.T) {
	f := fakedocker.New(t)
	dc, err := client.NewClientWithOpts(client.WithHost("tcp://"+f.Srv.Listener.Addr().String()), client.WithVersion("1.48"))
	require.NoError(t, err)
	f.SetImageHealthcheck("app:1", &container.HealthConfig{Test: []string{"CMD", "true"}})
	plan := netplan.LegacyPlan(slotSvcID, "web", "web", "cp-net")
	create := func(name string, labels map[string]string, labelRoutes bool) *fakedocker.Container {
		_, err := runContainerWithName(context.Background(), dc, NetConfig{}, plan, name, Slot{ServiceID: slotSvcID, Image: "app:1"}, labels, labelRoutes, nil)
		require.NoError(t, err)
		return f.ByName(name)
	}

	c := create("routed", map[string]string{"traefik.enable": "true"}, true)
	require.Equal(t, busybox, c.Healthcheck.Test[1])
	assert.Equal(t, []string{"true"}, c.Healthcheck.Test[len(c.Healthcheck.Test)-1:], "the image's own check runs inside it")
	assert.Equal(t, map[string]string{drainDir: "mode=1777,size=64k"}, c.Tmpfs)

	assert.Nil(t, create("worker", map[string]string{"traefik.enable": "false"}, true).Healthcheck)
	assert.Nil(t, create("file-routed", map[string]string{"traefik.enable": "true"}, false).Healthcheck)
}

// The service's container check replaces the image's: off is Docker's NONE,
// a command its CMD-SHELL at the image's timings; on a routed slot either
// goes inside the drain check in place of the image's. The zero value
// keeps the image's, as before.
func TestContainerCheck(t *testing.T) {
	f := fakedocker.New(t)
	dc, err := client.NewClientWithOpts(client.WithHost("tcp://"+f.Srv.Listener.Addr().String()), client.WithVersion("1.48"))
	require.NoError(t, err)
	f.SetImageHealthcheck("app:1", &container.HealthConfig{Test: []string{"CMD-SHELL", "curl -f http://127.0.0.1:7860/health"},
		Interval: 10 * time.Second, Timeout: 2 * time.Second, Retries: 4})
	plan := netplan.LegacyPlan(slotSvcID, "web", "web", "cp-net")
	create := func(name string, check ContainerCheck, routed bool) *container.HealthConfig {
		labels := map[string]string{"traefik.enable": strconv.FormatBool(routed)}
		_, err := runContainerWithName(context.Background(), dc, NetConfig{}, plan, name, Slot{ServiceID: slotSvcID, Image: "app:1", Check: check}, labels, true, nil)
		require.NoError(t, err)
		return f.ByName(name).Healthcheck
	}

	assert.Nil(t, create("worker-image", ContainerCheck{}, false), "the image's own, inherited")
	assert.Equal(t, &container.HealthConfig{Test: []string{"NONE"}}, create("worker-off", ContainerCheck{Off: true}, false))
	assert.Equal(t, &container.HealthConfig{Test: []string{"CMD-SHELL", "pgrep -f api.worker"},
		Interval: 10 * time.Second, Timeout: 2 * time.Second, Retries: 4},
		create("worker-command", ContainerCheck{Command: "pgrep -f api.worker"}, false), "the image's interval, timeout and retries")

	drainOnly, _ := drainHealthcheck(nil, nil)
	assert.Equal(t, drainOnly.Test, create("web-off", ContainerCheck{Off: true}, true).Test, "the drain flag alone")
	hc := create("web-command", ContainerCheck{Command: "test -e /tmp/ready"}, true)
	require.Equal(t, busybox, hc.Test[1])
	assert.Equal(t, []string{"/bin/sh", "-c", "test -e /tmp/ready"}, hc.Test[len(hc.Test)-3:], "the command runs inside the drain check")
	assert.Equal(t, []string{"10", "4", "2"}, hc.Test[7:10], "at the image's interval, retries and timeout")
	hc = create("web-image", ContainerCheck{}, true)
	assert.Equal(t, []string{"/bin/sh", "-c", "curl -f http://127.0.0.1:7860/health"}, hc.Test[len(hc.Test)-3:], "the image's own inside it")

	f.SetImageHealthcheck("bare:1", nil)
	_, err = runContainerWithName(context.Background(), dc, NetConfig{}, plan, "bare-command", Slot{ServiceID: slotSvcID, Image: "bare:1",
		Check: ContainerCheck{Command: "true"}}, map[string]string{}, true, nil)
	require.NoError(t, err)
	assert.Equal(t, &container.HealthConfig{Test: []string{"CMD-SHELL", "true"}}, f.ByName("bare-command").Healthcheck, "Docker's defaults without the image's")
}
