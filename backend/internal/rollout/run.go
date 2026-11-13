package rollout

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
)

// Spec is one rollout of a single-container service: what the panel hands
// the host that runs it (D-111). The panel keeps the policy, the network
// plan and builds; the host pulls, migrates, creates the new slot, checks
// it and switches.
type Spec struct {
	DeploymentID string
	Slot         Slot
	Plan         netplan.Plan
	// Port is the port the new slot listens on: what the ready wait and the
	// health check ask (0: none).
	Port int
	// Pull pulls Slot.Image first (an image deploy; a build's image is
	// already on the host).
	Pull bool
	// PullMissing pulls Slot.Image only when the host doesn't have it (a
	// rollback to an image a disk clean removed, D-135).
	PullMissing bool `json:",omitempty"`
	Auth        *RegistryAuth
	// Migrate is the migrate command, run once in a throwaway container
	// from Slot.Image before the new slot starts (migrateArgv); "": none.
	Migrate string
	// Recreate stops the previous slot before the new one starts (D-42).
	Recreate bool
	// ActiveDeployment is the service's active deployment when the deploy
	// started (SlotSwitch.ActiveDeployment).
	ActiveDeployment string
	Health           *HealthCheck
	PostDeploy       []PostDeploy
}

// RegistryAuth is a registry's credentials for a pull.
type RegistryAuth struct {
	Server   string
	Username string
	Password string
}

// HealthCheck is the request the new slot must answer before it serves.
type HealthCheck struct {
	Path       string
	TimeoutSec int
	Retries    int
}

// PostDeploy is a command run in one of the service's containers once the
// new slot is healthy: Container, or the new slot when it is "".
type PostDeploy struct {
	Container string
	Command   string
}

// Hooks are how Run reports while it runs.
type Hooks struct {
	// Log receives the deploy log's lines.
	Log func(string)
	// Abandon, when set, is asked when a step fails, before Run cleans up:
	// true leaves the new slot, and a previous one it stopped, as they are
	// for the panel's restart recovery (the panel is shutting down).
	Abandon func() bool
	// HandOff, when set, records that the rollout goes to the server's
	// agent (D-111), before the request is sent: a panel restarted from then
	// on leaves it to the agent. An error keeps the rollout from the agent.
	HandOff func() error
	// Gone, when set, is asked while a handed-off rollout's agent can't be
	// reached: true when the server no longer connects through it (moved to
	// SSH, or the agent revoked), and following stops with ErrAgentGone.
	Gone func() bool
	// Route, when set, points the service's route at the new slot and
	// returns once Traefik serves it there, before the previous slot is
	// retired (D-113: on a server whose agent writes Traefik's routes). On
	// an error the route is left as it was. Unset, the containers' labels
	// route them.
	Route func(ctx context.Context, newSlot string, logFn func(string)) error
	// Save, when set, records p durably before it returns: before each step
	// starts, once Start has chosen the slots, and when the rollout ends.
	// The agent's journal is what Resume continues from.
	Save func(p Progress)
	// TimeLeft, when set, is the deploy's time left (D-125), 0 for no
	// limit: a handed-off rollout takes it to an agent that keeps one.
	TimeLeft func() time.Duration
	// NoLimit, when set, lifts the deploy's time limit: a handed-off
	// rollout's agent can't keep it (before v0.26.0), and the panel can't
	// stop what the agent runs.
	NoLimit func()
}

// Progress is how far a rollout got.
type Progress struct {
	// Step is the step that started last ("" before the first).
	Step   string
	Switch SlotSwitch
	// Result is set once the rollout ended.
	Result *Result
}

// The steps of a rollout, as Result.Step names the one that failed.
const (
	StepConnect    = "connect" // the host's Docker could not be reached
	StepPull       = "pull"
	StepMigrate    = "migrate"
	StepStart      = "start"
	StepReady      = "ready"
	StepHealth     = "health"
	StepPostDeploy = "post-deploy"
	StepSwitch     = "switch"
)

// Result is how a rollout ended.
type Result struct {
	// Step is the step that failed, "" when the rollout succeeded.
	Step string
	Err  string
	// Changed is true once the rollout changed containers (it got as far as
	// creating the new slot).
	Changed bool
	// PreviousServes is true when, after a failure, what served the service
	// before the rollout serves it again: the new slot was removed, and a
	// previous slot it stopped was started again.
	PreviousServes bool
	// New is the slot the rollout created, "" when it created none.
	New string
}

// Host is the Docker a rollout runs on. DockerHost is the real one; the
// pipeline's tests fake it.
type Host interface {
	Pull(ctx context.Context, image string, auth *RegistryAuth, logFn func(string)) error
	// RunOnce runs argv in a throwaway container from spec's image on its
	// home network, and fails when it exits non-zero. Resumed, it only
	// waits for the one an earlier attempt started (ErrOnceGone: none).
	RunOnce(ctx context.Context, spec Spec, argv []string, resume bool, logFn func(string)) error
	// Start creates and starts the new slot, filling sw in (StartSlot).
	Start(ctx context.Context, spec Spec, sw *SlotSwitch) error
	WaitReady(ctx context.Context, name string, port int, logFn func(string)) error
	// Health asks the health check once from inside name's network
	// namespace.
	Health(ctx context.Context, name string, port int, h HealthCheck) error
	// Slots are the names of the service's slot containers.
	Slots(ctx context.Context, serviceID string) ([]string, error)
	Exec(ctx context.Context, name, command string, logFn func(string)) error
	// AwaitHealthy returns once Docker reports name healthy, when it has
	// the drain check (drain.go), and with settle once Traefik routes to it.
	AwaitHealthy(ctx context.Context, name string, settle bool) error
	// FinishSwitch retires the slots other than keep; with drain, the ones
	// serving leave Traefik's rotation before they stop (drain.go).
	FinishSwitch(ctx context.Context, serviceID, keep string, drain bool) error
	AbortSwitch(ctx context.Context, serviceID, name string) error
	RestorePrevious(ctx context.Context, serviceID string, names []string) error
}

// imageChecker is a Host that can tell whether it has an image; one that
// can't is taken to have it.
type imageChecker interface {
	HasImage(ctx context.Context, image string) bool
}

func (r *run) missing(ctx context.Context, image string) bool {
	c, ok := r.h.(imageChecker)
	return ok && !c.HasImage(ctx, image)
}

// HealthInterval is the wait between health check attempts (a variable so
// tests can shorten it).
var HealthInterval = 2 * time.Second

// Run runs spec on h and reports how it ended.
//
// The new slot starts next to the previous one (Start), is checked, and the
// post-deploy commands run in it; only then is the previous slot retired
// (FinishSwitch). If any of that fails, the new slot is removed and the
// previous one keeps serving, or starts again after a stop-then-start.
//
// Until the switch completes both slots run with the same Traefik labels, so
// Traefik merges them into one service and balances requests across both. A
// deploy that changes the routing (a domain, the port or the access
// control) gives the two slots conflicting router, middleware or service
// definitions instead, which Traefik drops while both run
// (SlotSwitch.RoutingChanged). The previous slot is then retired as soon as
// the new one passes its health check, so the service's routes are down for
// the health check only, not for the post-deploy commands too; the price is
// that a failing post-deploy command can no longer bring the previous slot
// back.
//
// Where the agent writes Traefik's routes (Hooks.Route, D-113), none of
// that applies: the previous slot keeps the route until the new one passed
// everything, and the route moves in one step before the previous is retired.
func Run(ctx context.Context, h Host, spec Spec, hooks Hooks) Result {
	return Resume(ctx, h, spec, Progress{}, hooks)
}

// Resume continues a rollout from p, as Hooks.Save last recorded it, after
// the host restarted (D-111). Pull, health and the switch run again; the
// migrate command is waited for, never run twice; Start reuses the slot it
// chose and the container it created. A restart during the post-deploy
// commands fails the rollout: they never run twice.
func Resume(ctx context.Context, h Host, spec Spec, p Progress, hooks Hooks) Result {
	sw := p.Switch
	sw.ActiveDeployment, sw.Recreate, sw.FileRoutes = spec.ActiveDeployment, spec.Recreate, hooks.Route != nil
	r := &run{h: h, spec: spec, hooks: hooks, sw: &sw, from: stepOrder[p.Step], step: p.Step}
	res := r.run(ctx)
	r.save(&res)
	return res
}

// stepOrder is the order steps run in: a resumed rollout skips those before
// the one it was in.
var stepOrder = map[string]int{"": 0, StepPull: 1, StepMigrate: 2, StepStart: 3, StepReady: 4, StepHealth: 5, StepPostDeploy: 6, StepSwitch: 7}

type run struct {
	h     Host
	spec  Spec
	hooks Hooks
	sw    *SlotSwitch
	from  int    // stepOrder of the step a resumed rollout was in
	step  string // the step running
}

func (r *run) log(msg string) {
	if r.hooks.Log != nil {
		r.hooks.Log(msg)
	}
}

func (r *run) save(res *Result) {
	if r.hooks.Save != nil {
		r.hooks.Save(Progress{Step: r.step, Switch: *r.sw, Result: res})
	}
}

// begin records that step starts, and reports whether it runs: a resumed
// rollout skips the steps before the one it was in.
func (r *run) begin(step string) bool {
	if stepOrder[step] < r.from {
		return false
	}
	r.step = step
	r.save(nil)
	return true
}

func (r *run) run(ctx context.Context) Result {
	spec, sw := r.spec, r.sw
	if r.from > 0 {
		r.log("↻ The agent restarted during this rollout; resuming at " + r.step)
	}
	pulled := false
	if (spec.Pull || spec.PullMissing && r.missing(ctx, spec.Slot.Image)) && r.begin(StepPull) {
		r.log(fmt.Sprintf("[1/2] Pulling image %s...", spec.Slot.Image))
		if err := r.h.Pull(ctx, spec.Slot.Image, spec.Auth, r.log); err != nil {
			r.log("✗ Pull failed: " + err.Error())
			return Result{Step: StepPull, Err: err.Error()}
		}
		pulled = true
	}
	if spec.Migrate != "" && r.begin(StepMigrate) {
		resume := r.from == stepOrder[StepMigrate]
		if !resume {
			r.log("→ running migrate command: " + spec.Migrate)
		}
		err := r.migrate(ctx, pulled, resume)
		if err != nil {
			r.log("✗ Migrate command failed: " + err.Error())
			return Result{Step: StepMigrate, Err: "migrate command failed: " + err.Error()}
		}
		r.log("✓ Migrate command succeeded")
	}

	if r.begin(StepStart) {
		if sw.Recreate {
			r.log("→ Stop then start: the previous container stops before the new one starts, so two never run on the same data " +
				"(Settings → Deploy strategy)")
		}
		r.log(fmt.Sprintf("Starting container from image %s...", spec.Slot.Image))
		sw.Chosen = func() { r.save(nil) }
		err := r.h.Start(ctx, spec, sw)
		sw.Chosen = nil
		if len(sw.Stopped) > 0 {
			r.log("■ Stopped the previous container: " + strings.Join(sw.Stopped, ", "))
		}
		if err != nil {
			r.log("✗ Container start failed: " + err.Error())
			return r.fail(ctx, StepStart, err.Error())
		}
	}
	if r.hooks.Route != nil {
		// The new slot gets the route only at the switch, so the two slots
		// never conflict in Traefik.
		sw.RoutingChanged = false
	}
	switch r.from {
	case stepOrder[StepPostDeploy]:
		r.step = StepPostDeploy
		r.log("✗ The agent restarted during the post-deploy commands, which never run twice")
		return r.fail(ctx, StepPostDeploy, "the agent restarted during the post-deploy commands, which never run twice: check what they did, then deploy again")
	case stepOrder[StepSwitch]:
		r.step = StepSwitch
		// After a restart of the host both slots may be starting again.
		if res := r.awaitHealthy(ctx); res != nil {
			return *res
		}
		if _, res := r.retirePrevious(ctx); res != nil {
			return *res
		}
		return Result{Changed: true, New: sw.New}
	}
	if sw.Recreate && sw.New != "" && r.begin(StepReady) {
		if err := r.waitReady(ctx); err != nil {
			return r.fail(ctx, StepReady, err.Error())
		}
	}
	r.begin(StepHealth)
	if err := r.health(ctx); err != nil {
		return r.fail(ctx, StepHealth, "health check failed: "+err.Error())
	}
	if res := r.awaitHealthy(ctx); res != nil {
		return *res
	}
	retired := false
	if sw.New != "" && sw.RoutingChanged && len(sw.Previous) > 0 {
		r.log("⚠ The routing changed (domains, port or access control). Traefik drops routes that two running containers " +
			"define differently, so the previous container is retired now that the new one passed its health check, before the " +
			"post-deploy commands; a failing post-deploy command can no longer bring it back")
		var res *Result
		if retired, res = r.retirePrevious(ctx); res != nil {
			return *res
		}
	}
	r.begin(StepPostDeploy)
	if err := r.postDeploy(ctx); err != nil {
		if retired {
			r.log("✗ The previous container was already retired, so the new one keeps running; fix the command and redeploy, or roll back")
		}
		return r.fail(ctx, StepPostDeploy, err.Error())
	}
	if sw.New != "" && !retired {
		r.begin(StepSwitch)
		if _, res := r.retirePrevious(ctx); res != nil {
			return *res
		}
	}
	return Result{Changed: true, New: sw.New}
}

// migrate runs the migrate command, pulling the image first with the
// registry's credentials when the rollout did not pull it. Resumed, it
// waits for the one started before the restart.
func (r *run) migrate(ctx context.Context, pulled, resume bool) error {
	if !resume && !pulled && r.spec.Auth != nil && r.spec.Auth.Password != "" {
		if err := r.h.Pull(ctx, r.spec.Slot.Image, r.spec.Auth, r.log); err != nil {
			return fmt.Errorf("pull image for ephemeral run: %w", err)
		}
	}
	return r.h.RunOnce(ctx, r.spec, migrateArgv(r.spec.Migrate), resume, r.log)
}

// waitReady waits until the new slot of a stop-then-start deploy accepts
// connections. A database has no health check, so without it a new
// container that crashes at start would replace the stopped one.
func (r *run) waitReady(ctx context.Context) error {
	if r.spec.Port > 0 {
		r.log(fmt.Sprintf("→ Waiting for the new container to accept connections on port %d", r.spec.Port))
	} else {
		r.log("→ Checking that the new container keeps running")
	}
	started := time.Now()
	err := r.h.WaitReady(ctx, r.sw.New, r.spec.Port, r.log)
	if err == nil {
		r.log(fmt.Sprintf("✓ Ready %.1fs after it started", time.Since(started).Seconds()))
		return nil
	}
	r.log("✗ " + err.Error())
	var notReady *NotReadyError
	if errors.As(err, &notReady) && len(notReady.LastLines) > 0 {
		r.log("  Its last log lines:")
		for _, l := range notReady.LastLines {
			r.log("  │ " + l)
		}
	}
	return err
}

// health checks the new slot, retrying until it answers or the retries run
// out. Without a health check it passes.
func (r *run) health(ctx context.Context) error {
	hc := r.spec.Health
	if hc == nil || hc.Retries == 0 {
		return nil
	}
	r.log(fmt.Sprintf("→ health check: %s (timeout: %ds, retries: %d)", hc.Path, hc.TimeoutSec, hc.Retries))
	var lastErr error
	for i := 0; i < hc.Retries; i++ {
		if i > 0 {
			t := time.NewTimer(HealthInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		if lastErr = r.h.Health(ctx, r.sw.New, r.spec.Port, *hc); lastErr == nil {
			r.log("✓ Health check passed")
			return nil
		}
	}
	err := fmt.Errorf("health check failed after %d retries: %w", hc.Retries, lastErr)
	r.log("✗ Health check failed: " + err.Error())
	return err
}

// postDeploy runs the post-deploy commands. A command with no container
// runs in the new slot; a named one must be one of the service's own
// slots: the name is user input and must not reach another service's
// container (or the control plane's). It returns the first failure, already
// logged.
func (r *run) postDeploy(ctx context.Context) error {
	cmds := r.spec.PostDeploy
	if len(cmds) == 0 {
		return nil
	}
	r.log(fmt.Sprintf("→ running %d post-deploy command(s)...", len(cmds)))
	for _, pd := range cmds {
		name := pd.Container
		if name == "" {
			name = r.sw.New
		} else if slots, _ := r.h.Slots(ctx, r.spec.Slot.ServiceID); !contains(slots, name) {
			msg := fmt.Sprintf("post-deploy command targets %q, which is not one of this service's containers", name)
			r.log("✗ " + msg)
			return errors.New(msg)
		}
		r.log(fmt.Sprintf("$ exec [%s] %s", name, pd.Command))
		if err := r.h.Exec(ctx, name, pd.Command, r.log); err != nil {
			r.log("✗ Post-deploy command failed: " + err.Error())
			return err
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// awaitHealthy waits until Traefik routes the new slot, where it serves
// beside a previous one through labels: Traefik routes a slot with the
// drain check once Docker reports it healthy, and until then the previous
// one serves alone. Where the routing changed, Traefik serves neither once
// the new one is healthy, so the switch goes on without waiting for Traefik.
// A non-nil res ends the rollout: the new slot never got healthy.
func (r *run) awaitHealthy(ctx context.Context) *Result {
	sw := r.sw
	if sw.New == "" || r.hooks.Route != nil || len(sw.Previous) == 0 || len(sw.Stopped) > 0 {
		return nil
	}
	if err := r.h.AwaitHealthy(ctx, sw.New, !sw.RoutingChanged); err != nil {
		r.log("✗ " + err.Error())
		failed := r.fail(ctx, r.step, err.Error())
		return &failed
	}
	return nil
}

// retirePrevious completes the switch to the new slot (FinishSwitch).
// retired reports that the previous slot is gone (sw.Previous is then
// cleared). A non-nil res ends the rollout instead: the new slot is not
// running (it exited right after starting), so it was removed and the
// previous one keeps serving. Any other error is only logged: the new slot
// passed its checks and serves, next to the previous one.
//
// It runs past the deploy's time limit (settle): the new slot passed every
// check, and a switch cut halfway leaves neither slot routed as it should be.
func (r *run) retirePrevious(ctx context.Context) (retired bool, res *Result) {
	ctx, cancel := settle(ctx)
	defer cancel()
	sw := r.sw
	if r.hooks.Route != nil {
		if err := r.hooks.Route(ctx, sw.New, r.log); err != nil {
			r.log("✗ Could not route the new container: " + err.Error())
			failed := r.fail(ctx, StepSwitch, "could not route the new container: "+err.Error())
			return false, &failed
		}
	}
	// Draining is for slots that serve beside the new one through their
	// labels: not when the route moved in a file, nor when conflicting
	// routes keep Traefik from serving either.
	drain := r.hooks.Route == nil && !sw.RoutingChanged && len(sw.Previous) > 0 && len(sw.Stopped) == 0
	if drain {
		r.log("→ Taking the previous container out of Traefik's rotation; it finishes the requests it has, then stops")
	}
	err := r.h.FinishSwitch(ctx, r.spec.Slot.ServiceID, sw.New, drain)
	switch {
	case errors.Is(err, ErrNewSlotNotRunning):
		r.log("✗ " + err.Error())
		failed := r.fail(ctx, StepSwitch, err.Error())
		return false, &failed
	case err != nil:
		r.log("⚠ Could not retire the previous container: " + err.Error() +
			". It keeps running alongside the new one until the next deploy")
		return false, nil
	}
	if len(sw.Previous) > 0 {
		r.log("→ retired the previous container: " + strings.Join(sw.Previous, ", "))
	}
	sw.Previous = nil
	return true, nil
}

// fail ends a rollout that failed once it started the new slot. When
// something was serving as the new slot was created, the new slot is
// removed and the previous one keeps serving (or starts again, after a
// stop-then-start). Otherwise (a first deploy) the new container is left
// in place so its logs can be read.
func (r *run) fail(ctx context.Context, step, errMsg string) Result {
	ctx, cancel := settle(ctx) // the deploy's time limit may be what failed it
	defer cancel()
	sw := r.sw
	res := Result{Step: step, Err: errMsg, Changed: true, New: sw.New}
	if r.hooks.Abandon != nil && r.hooks.Abandon() {
		return res // recovery removes the new slot when it can tell it apart
	}
	if len(sw.Previous) == 0 {
		return res
	}
	aborted := true
	if sw.New != "" {
		if err := r.h.AbortSwitch(ctx, r.spec.Slot.ServiceID, sw.New); err != nil {
			aborted = false
			r.log(fmt.Sprintf("⚠ Could not remove the new container %s, which may receive traffic alongside the previous one: %v", sw.New, err))
		} else {
			r.log("↩ Removed the new container " + sw.New)
		}
	}
	if len(sw.Stopped) > 0 {
		// Stop then start: the previous container serves again only once the
		// new one is gone, or both would run on the same data.
		if !aborted {
			r.log("✗ The previous container (" + strings.Join(sw.Stopped, ", ") + ") stays stopped while the new one may still run; " +
				"remove " + sw.New + " and redeploy")
			return res
		}
		if err := r.h.RestorePrevious(ctx, r.spec.Slot.ServiceID, sw.Stopped); err != nil {
			r.log("✗ Could not start the previous container again: " + err.Error())
			return res
		}
		r.log("↩ Started the previous container again (" + strings.Join(sw.Stopped, ", ") + "); the service's status is unchanged")
	} else {
		r.log("↩ The previous container keeps serving (" + strings.Join(sw.Previous, ", ") + "); the service's status is unchanged")
	}
	res.PreviousServes = true
	return res
}

// settleTimeout bounds what a rollout runs once its deploy's time limit may
// have passed (D-125): the clean-up of a failure, a switch already decided.
const settleTimeout = 2 * time.Minute

// settle is ctx without its cancellation, bounded by settleTimeout.
func settle(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
}

// shellSyntax marks a migrate command that needs a shell: variable
// expansion, pipes, redirects, quoting, globs, subshells, `KEY=v cmd`.
var shellSyntax = regexp.MustCompile("[$|&;<>()`\\\"'*?\\[\\]{}~#\n]|^\\S*=")

// migrateArgv turns the migrate command into the argv of the ephemeral
// container, which runs with the image's ENTRYPOINT cleared (see
// runEphemeral). Plain commands are split on whitespace and exec'd directly,
// so a distroless image with no shell works; anything with shell syntax goes
// through sh -c, so $VAR still expands against the container's environment.
func migrateArgv(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if shellSyntax.MatchString(cmd) {
		return []string{"sh", "-c", cmd}
	}
	return strings.Fields(cmd)
}

// Request is a rollout the panel hands to a server's agent (D-111): Spec,
// or a compose deploy's up when Compose is set.
type Request struct {
	Spec    Spec
	Net     NetConfig
	Compose *ComposeUp `json:",omitempty"`
	// Epoch is the panel's (D-113): an agent holding another one's state
	// refuses the request. "" from a panel before v0.19.0.
	Epoch string `json:",omitempty"`
	// TimeLeft is the deploy's time left when the panel sent it (D-125), 0
	// for no limit; the agent counts it from when it receives the request,
	// so the two clocks needn't agree.
	TimeLeft time.Duration `json:",omitempty"`
}

// ID is the request's deployment.
func (r Request) ID() string {
	if r.Compose != nil {
		return r.Compose.DeploymentID
	}
	return r.Spec.DeploymentID
}

// ServiceID is the service the request deploys.
func (r Request) ServiceID() string {
	if r.Compose != nil {
		return r.Compose.ServiceID
	}
	return r.Spec.Slot.ServiceID
}

// StepComposeUp is a compose deploy's docker compose up.
const StepComposeUp = "compose-up"

// ComposeUp is a compose deploy's up, which the agent runs: the panel has
// put the compose file (and the repository) in a release directory under
// ComposeDir. The agent writes DotEnv to ProjectDir/.env and the platform's
// override files, runs docker compose up in the project
// (ComposeProjectName), and removes both again. Then it checks the stack's
// routes (CheckStackRoutes, with RoutedService and AllowedLabels) and takes
// a stack that fails it down.
type ComposeUp struct {
	DeploymentID  string
	ServiceID     string
	ServiceName   string
	ProjectDir    string
	ComposeFile   string
	Overrides     []ComposeOverride
	DotEnv        []byte
	Auth          *RegistryAuth
	RoutedService string
	AllowedLabels map[string]bool
}

// ComposeOverride is one of the platform's override files.
type ComposeOverride struct {
	Name    string
	Content []byte
}

// ComposeRoot holds every compose service's releases on a host.
var ComposeRoot = "/var/paas/compose"

// ComposeDir is the directory on a host that holds a compose service's
// releases, one directory per deploy.
func ComposeDir(serviceID string) string {
	return ComposeRoot + "/" + serviceID
}

// PanelLinePrefix starts the lines the panel writes into a deploy's log
// while the server's agent runs it (D-111), so they are told apart from
// the agent's when the panel picks the log up again.
const PanelLinePrefix = "⇄ "

// ErrUnknownRollout: the server's agent holds no such rollout (it never
// received it).
var ErrUnknownRollout = errors.New("the server's agent has no such rollout")

// ErrAgentGone: the server no longer connects through the agent that runs
// the rollout, which can't be asked how it ended.
var ErrAgentGone = errors.New("the server no longer connects through its agent")

// UnknownRolloutPrefix starts an agent's refusal of an attach to a rollout
// it doesn't hold.
const UnknownRolloutPrefix = "no rollout "

// Attach asks an agent for a rollout's log from line Offset on.
type Attach struct {
	ID     string
	Offset int
}

// Event is a line of an agent's answer about a rollout: a log line, or,
// last, its result.
type Event struct {
	Log    string  `json:"log"`
	Result *Result `json:"result,omitempty"`
}

// Summary is a rollout an agent holds. Result is nil while it runs.
type Summary struct {
	ID        string
	ServiceID string
	Step      string
	Result    *Result
}
