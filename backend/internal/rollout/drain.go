package rollout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Draining (D-132). Where Traefik reads the containers' labels, both slots
// serve during a switch, and Traefik drops a container only on a Docker
// event: a container that stops is dialled for up to its provider's 2 s
// throttle after its address is gone, and those requests hang. Traefik also
// drops a container that Docker reports starting or unhealthy, so every
// routed slot gets a Docker health check that never asks the app and fails
// while drainFlag is fresh. The rollout waits until Docker reports the new
// slot healthy (AwaitHealthy, within the deploy's time limit); the switch
// sets the flag in the previous one, waits until Docker reports it unhealthy
// and Traefik has let it go, and only then stops it, with slotStopTimeout
// for the requests it still has.
//
// The flag counts for a minute only, so a previous slot that a failed or
// interrupted switch leaves running (the new one removed by AbortSwitch, or
// by recovery after a restart) serves again by itself; AbortSwitch also
// clears it at once.
//
// An image's own HEALTHCHECK is kept, on its own interval, timeout, retries
// and shell: drainScript runs it at most once an interval and reports as
// Docker would have, so the flag is still seen within drainInterval.

const (
	drainDir  = "/.toft-drain"
	drainFlag = drainDir + "/now"
	// The check's own cadence: a drained container leaves Traefik within
	// drainInterval × drainRetries of the flag, and two failed checks in a
	// row (an exec Docker couldn't start) are needed to drop one by mistake.
	// Each check costs Docker about 4% of a core-second: at 5 s about 0.8%
	// of one core per routed slot, measured on a test server (2 s: 1.8%,
	// 10 s: 0.5%), for a switch a few seconds longer than at 2 s.
	drainInterval = 5 * time.Second
	drainRetries  = 2
	drainTimeout  = 5 * time.Second
	// A new slot is checked this often until it first passes.
	drainStartInterval = 500 * time.Millisecond
	drainStartPeriod   = 30 * time.Second
	// How long a drained slot gets to be reported unhealthy.
	drainLeaveLimit = drainInterval*(drainRetries+2) + drainTimeout
)

// Docker's defaults for a HEALTHCHECK that leaves them unset.
const (
	dockerInterval      = 30 * time.Second
	dockerTimeout       = 30 * time.Second
	dockerRetries       = 3
	dockerStartInterval = 5 * time.Second
)

// traefikSettle is how long Traefik takes to act on a Docker event: its
// provider applies changes at most every 2 s (providersThrottleDuration,
// which Toft leaves at its default), plus a second. A variable so tests can
// shorten it.
var traefikSettle = 3 * time.Second

// drainPoll is how often a switch reads a slot's health while it waits.
var drainPoll = 250 * time.Millisecond

const busybox = ProbeMountPath + "/bin/busybox"

// drained is the shell test that the flag was set less than a minute ago.
const drained = `[ -n "$(` + busybox + ` find ` + drainFlag + ` -mmin -1 2>/dev/null)" ]`

// drainScript runs in busybox's sh from the probe mount. Its arguments are
// the gap between runs of the image's check until it first passes, the gap
// after, the image's retries and timeout (seconds), then the image's check
// as argv. Its state lives in drainDir, a tmpfs: when the check last ran,
// whether it ever passed, and its consecutive failures since. The check
// runs as this script's child, timed by the clock and killed as Docker
// would (busybox timeout leaves a watcher for PID 1 to reap, which many
// apps' PID 1 never does), and the flag is still read while it runs. Its
// output is dropped: the tmpfs is small, and nothing reads it.
const drainScript = `B=` + busybox + ` D=` + drainDir + `
if ` + drained + `; then echo "drained: leaving Traefik's rotation before it stops"; exit 1; fi
gap=$1 iv=$2 re=$3 to=$4
shift 4
[ -e $D/passed ] && gap=$iv
now=$($B date +%s)
last=$($B cat $D/last 2>/dev/null) || last=0
if [ $((now - last)) -ge "$gap" ]; then
	echo "$now" >$D/last
	"$@" >/dev/null 2>&1 &
	p=$!
	while kill -0 $p 2>/dev/null; do
		if ` + drained + `; then
			kill -9 $p
			wait $p
			echo "drained: leaving Traefik's rotation before it stops"
			exit 1
		fi
		[ "$($B date +%s)" -lt $((now + to)) ] || { kill -9 $p; break; }
		$B sleep 0.1
	done
	if wait $p; then
		: >$D/passed
		echo 0 >$D/fails
	else
		echo $(($($B cat $D/fails 2>/dev/null || echo 0) + 1)) >$D/fails
	fi
fi
[ -e $D/passed ] && [ "$($B cat $D/fails)" -lt "$re" ]`

// drainHealthcheck is the health check a routed slot is created with, from
// its image's own (nil: none) and the image's shell (nil: /bin/sh -c, as
// Docker's default). ok is false for an image check of a form Docker added
// after this was written: the slot then keeps the image's and is stopped
// without draining.
func drainHealthcheck(image *container.HealthConfig, shell []string) (hc *container.HealthConfig, ok bool) {
	var argv []string
	if image != nil && len(image.Test) > 0 {
		switch image.Test[0] {
		case "NONE":
		case "CMD":
			argv = image.Test[1:]
		case "CMD-SHELL":
			if len(image.Test) > 1 {
				if len(shell) == 0 {
					shell = []string{"/bin/sh", "-c"}
				}
				argv = append(append([]string{}, shell...), image.Test[1])
			}
		default:
			return nil, false
		}
	}
	if len(argv) == 0 {
		return &container.HealthConfig{
			Test:     []string{"CMD", busybox, "sh", "-c", "! " + drained},
			Interval: drainInterval, Timeout: drainTimeout, Retries: drainRetries,
			StartPeriod: drainStartPeriod, StartInterval: drainStartInterval,
		}, true
	}
	iv, to, re := orDefault(image.Interval, dockerInterval), orDefault(image.Timeout, dockerTimeout), image.Retries
	if re <= 0 {
		re = dockerRetries
	}
	// Until it first passes, the image's check runs at its start interval
	// whether or not it has a start period, so a new slot is routed soon
	// after it is ready, not an interval later.
	test := append([]string{"CMD", busybox, "sh", "-c", drainScript, "toft-drain",
		secs(orDefault(image.StartInterval, dockerStartInterval)), secs(iv), strconv.Itoa(re), secs(to)}, argv...)
	return &container.HealthConfig{
		Test:     test,
		Interval: drainInterval, Timeout: to + drainTimeout, Retries: drainRetries,
		// Until the image's check first passes, Docker reports starting, as
		// it would have within the image's start period and retries.
		StartPeriod: image.StartPeriod + iv*time.Duration(re), StartInterval: drainStartInterval,
	}, true
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// secs is d in whole seconds, rounded up, at least 1.
func secs(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}

// drains reports whether a container was created with drainHealthcheck: its
// check is busybox's from the probe mount and it has the tmpfs, which only
// the platform sets (an image can name any HEALTHCHECK).
func drains(info container.InspectResponse) bool {
	if info.Config == nil || info.ContainerJSONBase == nil || info.HostConfig == nil {
		return false
	}
	hc := info.Config.Healthcheck
	_, tmpfs := info.HostConfig.Tmpfs[drainDir]
	return tmpfs && hc != nil && len(hc.Test) > 1 && hc.Test[0] == "CMD" && hc.Test[1] == busybox
}

// ErrNewSlotNotHealthy is returned (wrapped) by AwaitHealthy when Docker
// never reported the new slot healthy, so Traefik would not route to it.
var ErrNewSlotNotHealthy = errors.New("Docker never reported the new container healthy, so Traefik would not route to it")

// AwaitHealthy returns once Docker reports name healthy, and with settle
// once Traefik has had time to route to it, when name was created with the
// drain check (other containers Traefik routes as soon as they run). Its
// limit is the check's own: its start period, then every retry.
func AwaitHealthy(ctx context.Context, dc *client.Client, name string, settle bool) error {
	info, err := dc.ContainerInspect(ctx, name)
	if err != nil {
		return fmt.Errorf("ContainerInspect %s: %w", name, err)
	}
	if !drains(info) {
		return nil
	}
	hc := info.Config.Healthcheck
	limit := hc.StartPeriod + (hc.Interval+hc.Timeout)*time.Duration(hc.Retries+1)
	status, err := waitHealth(ctx, dc, name, limit, container.Healthy)
	if err != nil {
		return fmt.Errorf("wait for Docker to report %s healthy: %w", name, err)
	}
	if status != container.Healthy {
		return fmt.Errorf("%w (%s)", ErrNewSlotNotHealthy, status)
	}
	if !settle {
		return nil
	}
	return sleep(ctx, traefikSettle)
}

// drainPrevious takes the running containers among names that drain out of
// Traefik's rotation, and returns once Traefik has let them go: then they
// only finish the requests they have. A container that doesn't drain is
// left to stop as before, and so is one Docker doesn't report unhealthy in
// time (the check reads the flag even while the image's own runs).
func drainPrevious(ctx context.Context, dc *client.Client, names []string) {
	var draining []string
	for _, name := range names {
		info, err := dc.ContainerInspect(ctx, name)
		if err != nil || info.State == nil || !info.State.Running || !drains(info) {
			continue
		}
		// A flag that can't be set leaves the container to stop as before.
		if execQuiet(ctx, dc, name, []string{busybox, "touch", drainFlag}) == nil {
			draining = append(draining, name)
		}
	}
	if len(draining) == 0 {
		return
	}
	for _, name := range draining {
		if _, err := waitHealth(ctx, dc, name, drainLeaveLimit, container.Unhealthy); err != nil {
			return
		}
	}
	_ = sleep(ctx, traefikSettle)
}

// undrain clears the flag in the running containers among names that drain,
// so they serve again at their next check.
func undrain(ctx context.Context, dc *client.Client, names []string) {
	for _, name := range names {
		if info, err := dc.ContainerInspect(ctx, name); err == nil && info.State != nil && info.State.Running && drains(info) {
			_ = execQuiet(ctx, dc, name, []string{busybox, "rm", "-f", drainFlag})
		}
	}
}

// waitHealth waits up to limit for Docker to report want for name, and
// returns the last status it read ("" when it read none; the container's
// state once it no longer runs). The error is ctx's.
func waitHealth(ctx context.Context, dc *client.Client, name string, limit time.Duration, want container.HealthStatus) (container.HealthStatus, error) {
	deadline := time.Now().Add(limit)
	var status container.HealthStatus
	for {
		if info, err := dc.ContainerInspect(ctx, name); err == nil && info.State != nil {
			if !info.State.Running {
				return container.HealthStatus(info.State.Status), nil
			}
			if info.State.Health != nil {
				status = info.State.Health.Status
			}
		}
		if status == want || !time.Now().Before(deadline) {
			return status, nil
		}
		if err := sleep(ctx, drainPoll); err != nil {
			return status, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// execQuiet runs argv in name and waits for it to end, discarding its output.
func execQuiet(ctx context.Context, dc *client.Client, name string, argv []string) error {
	ex, err := dc.ContainerExecCreate(ctx, name, container.ExecOptions{Cmd: argv, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return fmt.Errorf("ContainerExecCreate: %w", err)
	}
	resp, err := dc.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("ContainerExecAttach: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Reader)
	resp.Close()
	st, err := dc.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return fmt.Errorf("ContainerExecInspect: %w", err)
	}
	if st.ExitCode != 0 {
		return fmt.Errorf("%v exited %d", argv, st.ExitCode)
	}
	return nil
}
