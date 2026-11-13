package rollout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"golang.org/x/crypto/bcrypt"
)

// RunningSlots returns the running containers of serviceID's blue and green
// slots under their current names, keyed "blue" and "green".
func RunningSlots(ctx context.Context, dc *client.Client, serviceID, serviceName, envName string) map[string]container.Summary {
	slots := []struct{ slot, name string }{
		{"blue", BlueContainerName(serviceID, serviceName, envName)},
		{"green", GreenContainerName(serviceID, serviceName, envName)},
	}
	out := make(map[string]container.Summary, 2)
	for _, s := range slots {
		list, err := dc.ContainerList(ctx, container.ListOptions{
			All:     false,
			Filters: filters.NewArgs(filters.Arg("name", "/"+s.name)),
		})
		if err != nil {
			continue
		}
		for _, c := range list {
			if SummaryName(c) == s.name { // the name filter also matches longer names
				out[s.slot] = c
			}
		}
	}
	return out
}

// PickActiveSlot returns which running slot serves the service: the only one
// running or, when both run, the one activeDeployment created
// (paas.deployment_id), else the newer one. Both run after a switch whose
// previous slot could not be retired (the newer one is the active
// deployment's) or whose new slot could not be removed (the older one is).
func PickActiveSlot(running map[string]container.Summary, activeDeployment string) string {
	blue, hasBlue := running["blue"]
	green, hasGreen := running["green"]
	switch {
	case hasBlue && hasGreen:
		if activeDeployment != "" {
			switch activeDeployment {
			case blue.Labels[LabelDeploymentID]:
				return "blue"
			case green.Labels[LabelDeploymentID]:
				return "green"
			}
		}
		if green.Created > blue.Created {
			return "green"
		}
		return "blue"
	case hasBlue:
		return "blue"
	case hasGreen:
		return "green"
	}
	return ""
}

// routingConflicts reports whether labels define a Traefik router,
// middleware or service that one of serving defines differently. Traefik's
// Docker provider drops such a definition while both containers run, so the
// routes that use it stop working until one of them is gone.
func routingConflicts(labels map[string]string, serving []container.Summary) bool {
	mine := traefikObjects(labels)
	for _, c := range serving {
		for name, def := range traefikObjects(c.Labels) {
			if other, ok := mine[name]; ok && !maps.Equal(def, other) {
				return true
			}
		}
	}
	return false
}

// traefikObjects groups Traefik labels by the object they define
// (traefik.http.routers.<name>, traefik.http.middlewares.<name>, ...): each
// object maps to its settings. Labels such as traefik.enable define none.
func traefikObjects(labels map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string)
	for k, v := range labels {
		parts := strings.SplitN(k, ".", 5)
		if len(parts) < 5 || parts[0] != "traefik" {
			continue
		}
		object := strings.Join(parts[:4], ".")
		if out[object] == nil {
			out[object] = make(map[string]string)
		}
		out[object][parts[4]] = v
	}
	return out
}

// slotStopTimeout is how long a retired or aborted slot gets to stop
// before it is killed, in seconds.
const slotStopTimeout = 10

// recreateStopTimeout is how long a stop-then-start deploy gives the
// previous slot to stop before it is killed, in seconds: a database flushes
// its buffers on a clean stop, and one that is killed recovers from its log
// at the next start, which takes longer. Kubernetes' default grace period.
const recreateStopTimeout = 30

// FinishSwitch retires every container of serviceID except keep: the
// previous slot, containers left by a rename of the service, and the
// pre-blue/green container (paas-svc-<id>). It removes nothing unless keep
// is running, so completing a switch never leaves the service with no
// container. With drain, the ones still serving first leave Traefik's
// rotation (drainPrevious).
func FinishSwitch(ctx context.Context, dc *client.Client, serviceID, keep string, drain bool) error {
	list, err := ServiceContainers(ctx, dc, serviceID)
	if err != nil {
		return err
	}
	keepRunning := false
	for _, c := range list {
		if SummaryName(c) == keep && c.State == container.StateRunning {
			keepRunning = true
		}
	}
	if !keepRunning {
		return fmt.Errorf("%w (%s), so the previous one was left in place", ErrNewSlotNotRunning, keep)
	}

	// The pre-blue/green container, matched by its exact name: a name filter
	// matches substrings, and paas-svc-<id> is a prefix of the slot names of
	// a service with no name.
	legacy := ContainerNameForService(serviceID)
	legacyList, err := dc.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", "/"+legacy)),
	})
	if err != nil {
		return fmt.Errorf("ContainerList: %w", err)
	}
	for _, c := range legacyList {
		if SummaryName(c) == legacy {
			list = append(list, c)
		}
	}

	var retire []container.Summary
	seen := make(map[string]bool, len(list))
	for _, c := range list {
		if SummaryName(c) == keep || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		retire = append(retire, c)
	}
	if drain {
		names := make([]string, 0, len(retire))
		for _, c := range retire {
			names = append(names, SummaryName(c))
		}
		drainPrevious(ctx, dc, names)
	}
	var errs []error
	for _, c := range retire {
		if err := RemoveContainer(ctx, dc, c.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", SummaryName(c), err))
		}
	}
	return errors.Join(errs...)
}

// AbortSwitch removes the container named name when it is one of serviceID's
// containers, and clears the drain flag in the others, which serve again. A
// container that is not there is not an error.
func AbortSwitch(ctx context.Context, dc *client.Client, serviceID, name string) error {
	list, err := ServiceContainers(ctx, dc, serviceID)
	if err != nil {
		return err
	}
	var others []string
	for _, c := range list {
		if SummaryName(c) != name {
			others = append(others, SummaryName(c))
		}
	}
	undrain(ctx, dc, others)
	for _, c := range list {
		if SummaryName(c) == name {
			return RemoveContainer(ctx, dc, c.ID)
		}
	}
	return nil
}

// stopPrevious stops the serving slots before the new one is created, for a
// stop-then-start deploy (SlotSwitch.Recreate); StartSlot listed them in
// sw.Stopped, and one already gone leaves the list. They are kept, so the pipeline can start them again when the
// new slot fails. A stopped container defines no route, so the routing
// cannot conflict.
func stopPrevious(ctx context.Context, dc *client.Client, sw *SlotSwitch, serving []container.Summary) error {
	sw.RoutingChanged = false
	for _, c := range serving {
		timeout := recreateStopTimeout
		if err := dc.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil {
			if cerrdefs.IsNotFound(err) {
				sw.Stopped = slices.DeleteFunc(sw.Stopped, func(n string) bool { return n == SummaryName(c) })
				continue
			}
			return fmt.Errorf("stop the previous container %s: %w", SummaryName(c), err)
		}
	}
	return nil
}

// appendNew appends v to list unless it is there.
func appendNew(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// RestorePrevious starts the containers of serviceID named in names that are
// not running. Only the service's own slots (ServiceContainers) are
// started; a name that is not one of them is an error, as is a start that
// fails.
func RestorePrevious(ctx context.Context, dc *client.Client, serviceID string, names []string) error {
	list, err := ServiceContainers(ctx, dc, serviceID)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var errs []error
	for _, c := range list {
		name := SummaryName(c)
		if !want[name] {
			continue
		}
		delete(want, name)
		if c.State == container.StateRunning {
			continue
		}
		if err := dc.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("%s: ContainerStart: %w", name, err))
		}
	}
	for _, n := range names {
		if want[n] {
			errs = append(errs, fmt.Errorf("%s: the container is gone", n))
		}
	}
	return errors.Join(errs...)
}

// The readiness wait of a stop-then-start deploy (WaitReady). Variables so
// tests can shorten them.
var (
	// recreateReadyTimeout is how long the new slot gets to accept
	// connections: a database replays its log or upgrades its data files
	// before it listens.
	// ponytail: fixed; make it a service setting when a database needs longer.
	recreateReadyTimeout = 5 * time.Minute
	// readySettle is how long the new slot must keep running, never
	// restarted, before it counts as ready, whether it accepts connections
	// sooner or cannot be asked (no port, or no probe mount): MySQL opens its
	// port before it reads all its options, and aborts a second later on a
	// bad one. Nomad's min_healthy_time.
	readySettle = 10 * time.Second
	// readyPoll is the pause between two checks.
	readyPoll = time.Second
)

// readyTailLines is how many of a failed new slot's last log lines go to the
// deploy log.
const readyTailLines = 10

// WaitReady waits until the container name accepts TCP connections on port,
// asked from inside its network namespace with the probe's busybox (nc -z),
// so the image needs no tool, and has kept running for readySettle. A
// container that stops, or that its restart policy restarted, fails it at
// once with a NotReadyError carrying its last log lines: the
// pipeline removes it next, and its logs with it. With port 0, or without
// the probe mount, keeping running is all it checks.
func WaitReady(ctx context.Context, dc *client.Client, name string, port int, logFn func(string)) error {
	waitCtx, cancel := context.WithTimeout(ctx, recreateReadyTimeout)
	defer cancel()
	started := time.Now()
	accepting := false
	for {
		info, err := dc.ContainerInspect(waitCtx, name)
		switch {
		case err != nil && waitCtx.Err() == nil:
			return fmt.Errorf("inspect the new container: %w", err)
		case err != nil:
			// The wait timed out; reported below.
		case info.State == nil || !info.State.Running || info.RestartCount > 0:
			reason := "the new container exited and was restarted before it was ready"
			if info.State != nil && !info.State.Running {
				reason = fmt.Sprintf("the new container exited (code %d) before it was ready", info.State.ExitCode)
			}
			return &NotReadyError{Reason: reason, LastLines: logTail(ctx, dc, name)}
		case port > 0:
			switch portOpen(waitCtx, dc, name, port) {
			case portListening:
				if !accepting {
					accepting = true
					logFn(fmt.Sprintf("  Accepting connections %.1fs after the start; checking it keeps running", time.Since(started).Seconds()))
				}
				if time.Since(started) >= readySettle {
					return nil
				}
			case portUnknown:
				logFn("⚠ The new container has no probe tool, so the deploy only checks that it keeps running")
				port = 0
			}
		case time.Since(started) >= readySettle:
			return nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			reason := fmt.Sprintf("the new container was not ready within %s", recreateReadyTimeout)
			if port > 0 {
				reason = fmt.Sprintf("the new container did not accept connections on port %d within %s", port, recreateReadyTimeout)
			}
			return &NotReadyError{Reason: reason, LastLines: logTail(ctx, dc, name)}
		case <-time.After(readyPoll):
		}
	}
}

type portState int

const (
	portClosed portState = iota
	portListening
	portUnknown // the probe did not run
)

// portOpen asks, from inside name's network namespace, whether something
// accepts connections on port: the probe's busybox nc exits 0 when it
// connects and 1 when it cannot; 126 or 127 mean it is not there. A
// container between two restarts refuses the exec: closed, and the next
// inspect sees the restart.
func portOpen(ctx context.Context, dc *client.Client, name string, port int) portState {
	ex, err := dc.ContainerExecCreate(ctx, name, container.ExecOptions{
		Cmd:          []string{ProbeMountPath + "/bin/busybox", "nc", "-z", "-w", "1", "127.0.0.1", strconv.Itoa(port)},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return portClosed
	}
	resp, err := dc.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return portClosed
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Reader, 4096))
	resp.Close()
	in, err := dc.ContainerExecInspect(ctx, ex.ID)
	switch {
	case err != nil || in.Running:
		return portClosed
	case in.ExitCode == 0:
		return portListening
	case in.ExitCode == 126 || in.ExitCode == 127:
		return portUnknown
	default:
		return portClosed
	}
}

// logTail returns the last readyTailLines lines name wrote, or none when
// they cannot be read.
func logTail(ctx context.Context, dc *client.Client, name string) []string {
	rc, err := dc.ContainerLogs(ctx, name, container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(readyTailLines)})
	if err != nil {
		return nil
	}
	defer rc.Close()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, io.LimitReader(rc, 64<<10)); err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// RemoveContainer stops a container (slotStopTimeout, then killed) and
// removes it. A container that is already gone is not an error.
func RemoveContainer(ctx context.Context, dc *client.Client, id string) error {
	timeout := slotStopTimeout
	// A container that fails to stop is killed by the forced remove.
	_ = dc.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	if err := dc.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("ContainerRemove: %w", err)
	}
	return nil
}

// reuseBasicAuth gives the new slot's basic-auth middleware the htpasswd
// entry of a serving container when that entry still matches the user and
// password. bcrypt salts every hash, so a fresh entry would differ on every
// deploy, and Traefik refuses a middleware that two running containers
// define differently: the service would be unreachable until the previous
// slot is retired. serving must be the service's own slots (SlotsOf). An
// entry hashed at a higher cost than the platform's (htpasswdCost) is never
// checked: the platform did not write it, and bcrypt accepts costs that
// take days to check. weak reports an entry reused at a lower cost.
func reuseBasicAuth(labels map[string]string, serviceID string, serving []container.Summary, ba BasicAuth) (weak bool) {
	key := BasicAuthUsersLabel(serviceID)
	if _, ok := labels[key]; !ok {
		return false
	}
	for _, c := range serving {
		entry, ok := c.Labels[key]
		if !ok {
			continue
		}
		user, hash, ok := strings.Cut(entry, ":")
		if !ok || user != ba.User {
			continue
		}
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil || cost > htpasswdCost {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(ba.Password)) == nil {
			labels[key] = entry
			return cost < htpasswdCost
		}
	}
	return false
}

// ErrNewSlotNotRunning is returned (wrapped) by FinishSwitch when the new
// slot is not running, so nothing was retired: the deploy failed even though
// its checks passed (the container exited right after starting).
var ErrNewSlotNotRunning = errors.New("the new container is not running")

// NotReadyError is WaitReady's error when the new container stopped,
// restarted or did not listen in time. LastLines are its last log lines,
// for the deploy log: the failed container is removed, and its logs with it.
type NotReadyError struct {
	Reason    string
	LastLines []string
}

// SlotSwitch is what StartSlot reports about the blue/green switch it started.
type SlotSwitch struct {
	// ActiveDeployment is set by the caller: the service's active
	// deployment when the deploy started. When both slots are running (a
	// switch whose previous slot could not be retired, or whose new slot
	// could not be removed), Run keeps the one that deployment created
	// serving and replaces the other.
	ActiveDeployment string
	// Recreate is set by the caller: stop then start (D-42). Run stops the
	// previous slots before it creates the new one, so two containers never
	// run on the service's data, and records them in Stopped.
	Recreate bool
	// FileRoutes is set by the caller: Traefik reads the agent's route
	// files (Hooks.Route), so the two slots' labels never meet in Traefik.
	FileRoutes bool `json:"-"`

	// New is the container Run created for this deploy, or "" when it
	// failed before creating one.
	New string
	// Previous are the service's containers that were running when Run
	// created New: what served the service before this deploy.
	Previous []string
	// RoutingChanged: New defines a Traefik router, middleware or service
	// differently from one of Previous (another domain, port or access
	// control). Traefik drops such a definition while both run, so the
	// service's routes do not work until Previous is retired.
	RoutingChanged bool
	// Stopped are the previous slots StartSlot stops (Recreate), to be
	// started again (RestorePrevious) when the deploy fails. They are listed
	// before they stop, and StartSlot may fail after stopping some of them.
	Stopped []string

	// Chosen, when set, is called once New, Previous and Stopped are known,
	// before StartSlot stops or creates anything: the agent's journal
	// records them, so a restart resumes with the same slots.
	Chosen func() `json:"-"`
}

func (e *NotReadyError) Error() string { return e.Reason }

// HtpasswdEntry generates a bcrypt htpasswd entry for use in Traefik BasicAuth middleware.
// Labels are passed to Docker SDK as raw strings — no $ escaping needed (that's only for
// Docker Compose YAML variable interpolation).
func HtpasswdEntry(user, password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), htpasswdCost)
	if err != nil {
		return ""
	}
	return user + ":" + string(hash)
}
