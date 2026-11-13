package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
)

// Startup recovery of deploys interrupted by a restart (D-19).
//
// A deploy runs in a goroutine of this process, so a restart abandons it
// part-way: its deployment stays pending or building, and its containers
// may be half switched. Recovery runs at startup in two phases:
//
//  1. MarkInterrupted, before the API serves requests, marks every such
//     deployment failed with an "interrupted:" summary. It also returns the
//     deployments an earlier start marked whose phase 2 never finished.
//  2. ReconcileAfterRestart, in the background, checks the containers of
//     each service whose newest deployment was interrupted (planRecovery):
//     it sets the service's runtime status and active deployment to what
//     runs, removes the interrupted deploy's new slot when it is positively
//     identified next to the previous one (a restart mid-switch), and
//     records what it found on the deployment. Until that record is
//     written the deployment's summary is exactly interruptedSummary, so a
//     check that a shutdown or crash cuts short is made again on the next
//     start.
//
// Interrupted deploys are never re-run: that would repeat migrations and
// post-deploy commands. Recovery assumes a single backend instance: another
// instance's startup would mark this one's deploys.

// interruptedSummary is the error summary MarkInterrupted gives a
// deployment. Every recovery summary starts with "interrupted:", and every
// finished check appends "; " and what it found, so a deployment whose
// summary is exactly this was never checked.
const interruptedSummary = "interrupted: the control plane restarted during this deploy"

const (
	// recoveryConcurrency bounds how many services recovery works on at once.
	recoveryConcurrency = 4
	// recoveryCheckTimeout bounds listing a service's containers.
	recoveryCheckTimeout = 20 * time.Second
	// recoveryRemoveTimeout bounds removing a container.
	recoveryRemoveTimeout = 30 * time.Second
	// recoveryWriteTimeout bounds recording a check's outcome, which is not
	// cancelled with recovery's context: what was done is recorded even
	// during a shutdown.
	recoveryWriteTimeout = 10 * time.Second
	// recoveryClockSkew is how far a server's clock may lag the panel's: a
	// container created up to this long before the deploy started still
	// counts as the deploy's.
	recoveryClockSkew = 5 * time.Second
)

// recoveryRetryDelays are the waits before checking a service's containers
// again when its server could not be reached (a variable so tests can
// shorten it).
var recoveryRetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// errCannotInspect means a service's containers cannot be checked at all,
// so trying again would not help.
var errCannotInspect = errors.New("not supported")

// ContainerState is one of a service's containers, as recovery sees it.
type ContainerState struct {
	ID      string
	Name    string
	Image   string
	State   string // "running", "exited", ...
	Created time.Time
	// DeploymentID is the paas.deployment_id label: the deployment that
	// created the container (empty for containers created before it was
	// added).
	DeploymentID string
	// ServiceID is the paas.service_id label (blue/green slots).
	ServiceID string
	// Slot: a blue/green slot the platform created for the service, told by
	// its reserved name as well as its paas.service_id label. Only these
	// count as the service's slots: a container inherits every label of its
	// image, and tenants choose their images, so the label alone proves
	// nothing.
	Slot bool
	// ComposeProject is the com.docker.compose.project label.
	ComposeProject string
}

// Running reports whether the container is running.
func (c ContainerState) Running() bool { return c.State == "running" }

// ServiceContainerInspector is an optional Orchestrator capability, used by
// recovery. docker.LocalOrchestrator and docker.RemoteOrchestrator
// implement it.
type ServiceContainerInspector interface {
	// InspectServiceContainers lists a service's containers, running or
	// not: its blue/green slots (Slot) and its compose project's
	// containers, never a container that only carries the service's
	// paas.service_id label (an image can set it).
	InspectServiceContainers(ctx context.Context, serviceID, serviceName string) ([]ContainerState, error)
	// RemoveContainer stops and removes a container.
	RemoveContainer(ctx context.Context, id string) error
	// StartContainer starts a stopped container.
	StartContainer(ctx context.Context, id string) error
}

// ServerReader loads a server. Satisfied by *server.Service.
type ServerReader interface {
	GetServer(ctx context.Context, id string) (*server.Server, error)
}

// WithServerReader lets recovery reach the servers of remote services.
func (s *Service) WithServerReader(r ServerReader) { s.serverReader = r }

// gitImageTag is the image a git deploy builds.
func gitImageTag(serviceID, deploymentID string) string {
	return fmt.Sprintf("paas/%s:%s", serviceID, deploymentID)
}

// ─── Phase 1 ─────────────────────────────────────────────────────────────────

// MarkInterrupted is recovery's first phase, run at startup before the API
// serves requests. Every deployment a restart left pending, queued or
// building is marked failed with an "interrupted:" summary and a log line,
// except the deploys this process runs. It returns them for
// ReconcileAfterRestart, together with the deployments an earlier start
// marked but did not finish checking (still their service's newest).
func (s *Service) MarkInterrupted(ctx context.Context) ([]Deployment, error) {
	marked, err := s.repo.MarkInterrupted(ctx, interruptedSummary, s.inflightIDs())
	if err != nil {
		return nil, fmt.Errorf("mark interrupted deployments: %w", err)
	}
	ids := make([]string, 0, len(marked))
	for _, d := range marked {
		s.recoveryLog(ctx, d.ID, "✗ Interrupted: the control plane restarted during this deploy, so it is marked failed")
		ids = append(ids, d.ID)
	}
	if s.repos != nil {
		s.repos.FailDeployChecks(ctx, ids, "Interrupted: the panel restarted during this deploy")
	}
	unchecked, err := s.repo.ListUnreconciledInterrupted(ctx, interruptedSummary)
	if err != nil {
		s.log.Warn("recovery: could not list interrupted deployments an earlier start did not finish checking", zap.Error(err))
		return marked, nil
	}
	seen := make(map[string]bool, len(marked))
	for _, d := range marked {
		seen[d.ID] = true
	}
	for _, d := range unchecked {
		if !seen[d.ID] {
			seen[d.ID] = true
			marked = append(marked, d)
		}
	}
	return marked, nil
}

// ─── Phase 2 ─────────────────────────────────────────────────────────────────

// ReconcileAfterRestart is recovery's second phase. A deployment that had
// not started is recorded as that. For each service it takes the most
// recent of the others MarkInterrupted returned and, when that is still the
// service's newest deployment (isLatest), checks the service's containers
// and applies planRecovery. A server that cannot be
// reached is tried again after each of recoveryRetryDelays. A service whose
// server's agent still runs a deploy of it is left to that deploy, which
// FollowHandedOff records: reconciling mid-rollout could start the previous
// slot beside the new one. It returns when every service is done or ctx
// ends.
func (s *Service) ReconcileAfterRestart(ctx context.Context, marked []Deployment) {
	newest := make(map[string]Deployment)
	for _, d := range marked {
		if d.StartedAt == nil {
			// It waited for the deploy ahead of it (waitTurn) or never
			// began: nothing of it runs, and the one that had started is
			// the one to check.
			s.applyRecovery(ctx, d, nil, planRecovery(recoveryInput{Deployment: d, Latest: true}))
			continue
		}
		if cur, ok := newest[d.ServiceID]; !ok || d.CreatedAt.After(cur.CreatedAt) {
			newest[d.ServiceID] = d
		}
	}
	handed, err := s.repo.ListHandedOff(ctx)
	if err != nil {
		s.log.Error("could not list the deploys servers' agents run; nothing is reconciled", zap.Error(err))
		return
	}
	for _, h := range handed {
		if d, ok := newest[h.Deployment.ServiceID]; ok {
			s.recoveryLog(ctx, d.ID, "↩ Recovery: the server's agent is still running an earlier deploy of this service; it is left to finish")
			delete(newest, h.Deployment.ServiceID)
		}
	}
	sem := make(chan struct{}, recoveryConcurrency)
	var wg sync.WaitGroup
	for _, d := range newest {
		wg.Add(1)
		go func(d Deployment) {
			defer wg.Done()
			s.recoverDeployment(ctx, d, sem)
		}(d)
	}
	wg.Wait()
}

// recoveryTarget is a service whose containers were checked.
type recoveryTarget struct {
	svc        *service.Service
	inspector  ServiceContainerInspector
	cleanup    func()
	containers []ContainerState
}

// recoverDeployment reconciles d's service. It holds a slot of sem while it
// works, but not while it waits to try a server again.
func (s *Service) recoverDeployment(ctx context.Context, d Deployment, sem chan struct{}) {
	log := s.log.With(zap.String("deployment_id", d.ID), zap.String("service_id", d.ServiceID))
	held := false
	take := func() bool {
		held = acquire(ctx, sem)
		return held
	}
	release := func() {
		if held {
			<-sem
			held = false
		}
	}
	defer release()
	if !take() {
		return
	}

	latest, err := s.isLatest(ctx, d)
	if err != nil {
		log.Warn("recovery: could not load the service's newest deployment", zap.Error(err))
		return
	}
	in := recoveryInput{Deployment: d, Latest: latest}
	if !latest {
		s.applyRecovery(ctx, d, nil, planRecovery(in))
		return
	}

	var target *recoveryTarget
	for attempt := 0; ; attempt++ {
		target, err = s.checkService(ctx, d.ServiceID)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errCannotInspect) || attempt == len(recoveryRetryDelays) {
			log.Warn("recovery: could not check the service's containers", zap.Error(err))
			s.applyRecovery(ctx, d, nil, uncheckedPlan(err))
			return
		}
		delay := recoveryRetryDelays[attempt]
		s.recoveryLog(ctx, d.ID, fmt.Sprintf("⚠ Recovery: could not check what runs (%v); trying again in %s", err, delay))
		release()
		if !sleepCtx(ctx, delay) || !take() {
			return
		}
	}
	defer target.cleanup()

	// A deploy started while the containers were checked owns the service now.
	if latest, err = s.isLatest(ctx, d); err != nil {
		log.Warn("recovery: could not load the service's newest deployment", zap.Error(err))
		return
	}
	if !latest {
		log.Info("recovery: a later deploy started; leaving the service to it")
		return
	}
	in.Compose = target.svc.SourceType == service.SourceTypeDockerCompose
	in.ActiveDeployment = target.svc.ActiveDeploymentID
	in.Containers = target.containers
	s.applyRecovery(ctx, d, target, planRecovery(in))
}

// isLatest reports whether d is its service's newest deployment, not
// counting those that failed before they started.
func (s *Service) isLatest(ctx context.Context, d Deployment) (bool, error) {
	id, err := s.repo.LatestIDForService(ctx, d.ServiceID)
	if err != nil {
		return false, err
	}
	return id == d.ID, nil
}

// checkService loads a service and lists its containers on its server.
func (s *Service) checkService(ctx context.Context, serviceID string) (*recoveryTarget, error) {
	if s.serviceFetcher == nil {
		return nil, fmt.Errorf("%w: no service reader", errCannotInspect)
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("load the service: %w", err)
	}
	var srv *server.Server
	if svc.ServerID != "" {
		if s.serverReader == nil {
			return nil, fmt.Errorf("%w: no server reader", errCannotInspect)
		}
		if srv, err = s.serverReader.GetServer(ctx, svc.ServerID); err != nil {
			return nil, fmt.Errorf("load its server: %w", err)
		}
		if srv == nil {
			return nil, fmt.Errorf("load its server: server %s not found", svc.ServerID)
		}
	}
	orch, cleanup, err := s.factory.ForServer(srv)
	if err != nil {
		return nil, err
	}
	inspector, ok := orch.(ServiceContainerInspector)
	if !ok {
		cleanup()
		return nil, fmt.Errorf("%w: the orchestrator cannot list containers", errCannotInspect)
	}
	cctx, cancel := context.WithTimeout(ctx, recoveryCheckTimeout)
	defer cancel()
	containers, err := inspector.InspectServiceContainers(cctx, svc.ID, svc.Name)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("list its containers: %w", err)
	}
	return &recoveryTarget{svc: svc, inspector: inspector, cleanup: cleanup, containers: containers}, nil
}

// applyRecovery carries out plan for d. target is nil when nothing was
// checked; the plan then changes nothing on the service. A removal that
// recovery's context cut short (a shutdown) records nothing, so the next
// start checks the service again; once the containers are dealt with, the
// outcome is recorded even if the context ends.
func (s *Service) applyRecovery(ctx context.Context, d Deployment, target *recoveryTarget, plan recoveryPlan) {
	if target != nil {
		for _, id := range plan.RemoveContainerIDs {
			rctx, cancel := context.WithTimeout(ctx, recoveryRemoveTimeout)
			err := target.inspector.RemoveContainer(rctx, id)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					s.log.Info("recovery: stopped during a removal; the next start checks the service again",
						zap.String("deployment_id", d.ID), zap.Error(err))
					return
				}
				if len(plan.StartContainerIDs) > 0 {
					plan = restartFailedPlan(fmt.Errorf("the new container could not be removed: %w", err))
				} else {
					plan = removalFailedPlan(err)
				}
				break
			}
		}
		for _, id := range plan.StartContainerIDs {
			sctx, cancel := context.WithTimeout(ctx, recoveryRemoveTimeout)
			err := target.inspector.StartContainer(sctx, id)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					s.log.Info("recovery: stopped during a start; the next start checks the service again",
						zap.String("deployment_id", d.ID), zap.Error(err))
					return
				}
				plan = restartFailedPlan(err)
				break
			}
		}
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryWriteTimeout)
	defer cancel()
	// Once the outcome is recorded, what the next Deploy runs follows it.
	defer s.followLive(wctx, TriggerInput{ServiceID: d.ServiceID})
	if target != nil && plan.RuntimeStatus != nil {
		active := target.svc.ActiveDeploymentID
		if plan.ActiveDeploymentID != nil {
			active = *plan.ActiveDeploymentID
		}
		if err := s.serviceSvc.UpdateRuntimeStatus(wctx, d.ServiceID, *plan.RuntimeStatus, active); err != nil {
			s.log.Error("recovery: failed to update service runtime status", zap.String("service_id", d.ServiceID), zap.Error(err))
		}
	}
	if plan.Detail == "" {
		return
	}
	summary := interruptedSummary + "; " + plan.Detail
	if err := s.repo.RecordRecovery(wctx, d.ID, plan.ImageTag, summary); err != nil {
		s.log.Error("recovery: failed to record the outcome", zap.String("deployment_id", d.ID), zap.Error(err))
	}
	line := "↩ Recovery: " + plan.Detail
	if plan.NeedsAttention {
		line = "⚠ Recovery: " + plan.Detail
	}
	s.recoveryLog(wctx, d.ID, line)
	fields := []zap.Field{zap.String("deployment_id", d.ID), zap.String("service_id", d.ServiceID), zap.String("outcome", plan.Detail)}
	if plan.NeedsAttention {
		s.log.Warn("recovered an interrupted deploy; it needs attention", fields...)
	} else {
		s.log.Info("recovered an interrupted deploy", fields...)
	}
}

func (s *Service) recoveryLog(ctx context.Context, deploymentID, msg string) {
	if _, err := s.repo.InsertLog(ctx, deploymentID, msg); err != nil {
		s.log.Warn("failed to insert deployment log", zap.String("deployment_id", deploymentID), zap.Error(err))
	}
}

// acquire takes a slot of sem, or returns false when ctx ends first.
func acquire(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// sleepCtx waits for d, or returns false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// ─── The plan ────────────────────────────────────────────────────────────────

// recoveryInput is what recovery knows about an interrupted deployment.
type recoveryInput struct {
	Deployment Deployment
	// Latest: it is its service's newest deployment.
	Latest bool
	// Compose: the service deploys with docker compose.
	Compose bool
	// ActiveDeployment is the service's active deployment as recovery found
	// it: what served before the interrupted deploy.
	ActiveDeployment string
	// Containers are the service's containers, running or not.
	Containers []ContainerState
}

// recoveryPlan is what recovery does about an interrupted deployment.
type recoveryPlan struct {
	// RuntimeStatus is the service's new runtime status; nil leaves it.
	RuntimeStatus *string
	// ActiveDeploymentID is the service's new active deployment ("" for
	// none); nil leaves it.
	ActiveDeploymentID *string
	// RemoveContainerIDs are stopped and removed first.
	RemoveContainerIDs []string
	// StartContainerIDs are started once those are removed.
	StartContainerIDs []string
	// ImageTag, when set, is recorded on the deployment so it can be rolled
	// back to.
	ImageTag string
	// Detail is what was found and done; it follows interruptedSummary in
	// the deployment's error summary and is logged on it. Empty keeps the
	// summary MarkInterrupted wrote.
	Detail string
	// NeedsAttention: someone should look at the service.
	NeedsAttention bool
}

// origin is whether a container was started by the interrupted deploy.
type origin int

const (
	originUnknown origin = iota
	originNew            // started by the interrupted deploy
	originOld            // there before it
)

// containerOrigin tells whether c was started by the interrupted deploy d.
// In order: the paas.deployment_id label decides; else an image built by
// d's git deploy (paas/<service>:<deployment>) means it is d's. Otherwise,
// only when byCreated (used to tell two running slots apart), the creation
// time decides: d's when created after d started (with recoveryClockSkew of
// tolerance), older otherwise. Anything else is unknown.
func containerOrigin(c ContainerState, d Deployment, byCreated bool) origin {
	if c.DeploymentID != "" {
		if c.DeploymentID == d.ID {
			return originNew
		}
		return originOld
	}
	if c.Image == gitImageTag(d.ServiceID, d.ID) {
		return originNew
	}
	if !byCreated || d.StartedAt == nil || c.Created.IsZero() {
		return originUnknown
	}
	if c.Created.Before(d.StartedAt.Add(-recoveryClockSkew)) {
		return originOld
	}
	return originNew
}

// planRecovery decides what to do about an interrupted deployment. It is
// pure; see the package comment above for the phases.
//
// A deployment that is not its service's newest is only marked: a later
// deploy decided what runs. One that had not started changed nothing. For a
// compose service, the runtime status follows whether any container of the
// stack runs, and nothing is removed. Otherwise, with R the service's
// running blue/green slots (Slot: a container that only carries its
// paas.service_id label is not one of them):
//
//	R = 0, the previous    the previous slot is started again, once this
//	  one stopped          deploy's own slots are removed (a stop-then-start
//	                       deploy stopped it); status running
//	R = 0                  status error, no active deployment
//	R = 1, the previous    status running; nothing changed
//	R = 1, the new one     status running, active = this deployment, image
//	                       tag recorded; its checks may not have completed
//	R = 1, origin unknown  status running; flagged
//	R = 2, new + previous  the new one is removed (the restart came
//	                       mid-switch); the previous one keeps serving
//	R = 2, otherwise       status running; flagged, nothing removed
//	R ≥ 3                  status running; flagged
//
// A lone slot is identified only by its label or d's git image, never by
// its creation time, so an image tag is recorded only for a container known
// to be d's. The creation time only tells two running slots apart. The
// active deployment is left as it is unless the table says otherwise.
func planRecovery(in recoveryInput) recoveryPlan {
	d := in.Deployment
	if !in.Latest {
		return recoveryPlan{}
	}
	if d.StartedAt == nil {
		return recoveryPlan{Detail: "the deploy had not started, so nothing was changed"}
	}

	if in.Compose {
		var running []string
		for _, c := range in.Containers {
			if c.Running() {
				running = append(running, c.Name)
			}
		}
		status := "running"
		if len(running) == 0 {
			status = "error"
		}
		return recoveryPlan{
			RuntimeStatus:  &status,
			NeedsAttention: true,
			Detail: fmt.Sprintf("the compose stack may be partially updated (%s running), so redeploy it",
				countNames(running, "no container", "container")),
		}
	}

	var running []ContainerState
	for _, c := range in.Containers {
		if c.Slot && c.ServiceID == d.ServiceID && c.Running() {
			running = append(running, c)
		}
	}
	runningStatus := "running"
	switch len(running) {
	case 0:
		if plan, ok := restartPrevious(in); ok {
			return plan
		}
		errorStatus, none := "error", ""
		return recoveryPlan{
			RuntimeStatus:      &errorStatus,
			ActiveDeploymentID: &none,
			Detail:             "no container of the service is running",
		}
	case 1:
		c := running[0]
		switch containerOrigin(c, d, false) {
		case originOld:
			return recoveryPlan{
				RuntimeStatus: &runningStatus,
				Detail:        "nothing changed on the server: the previous container " + c.Name + " is still serving",
			}
		case originNew:
			id := d.ID
			return recoveryPlan{
				RuntimeStatus:      &runningStatus,
				ActiveDeploymentID: &id,
				ImageTag:           c.Image,
				NeedsAttention:     true,
				Detail: "the new container " + c.Name + " is the only one running, so this deployment is now the active one; " +
					"its health check and post-deploy commands may not have completed, so check the service, then redeploy or roll back",
			}
		default:
			return recoveryPlan{
				RuntimeStatus:  &runningStatus,
				NeedsAttention: true,
				Detail:         "container " + c.Name + " is running, but which deploy started it is not known; check it, then redeploy",
			}
		}
	case 2:
		a, b := containerOrigin(running[0], d, true), containerOrigin(running[1], d, true)
		var newC, oldC ContainerState
		switch {
		case a == originNew && b == originOld:
			newC, oldC = running[0], running[1]
		case a == originOld && b == originNew:
			newC, oldC = running[1], running[0]
		default:
			return recoveryPlan{
				RuntimeStatus:  &runningStatus,
				NeedsAttention: true,
				Detail: "both blue/green slots are running (" + running[0].Name + ", " + running[1].Name +
					") and which one is new is not known, so neither was removed; redeploy to settle it",
			}
		}
		return recoveryPlan{
			RuntimeStatus:      &runningStatus,
			RemoveContainerIDs: []string{newC.ID},
			Detail: "the restart came mid-switch: the new container " + newC.Name + " was removed and the previous container " +
				oldC.Name + " keeps serving",
		}
	default:
		names := make([]string, len(running))
		for i, c := range running {
			names[i] = c.Name
		}
		return recoveryPlan{
			RuntimeStatus:  &runningStatus,
			NeedsAttention: true,
			Detail:         fmt.Sprintf("%s of the service are running; check them, then redeploy", countNames(names, "", "container")),
		}
	}
}

// restartPrevious is planRecovery's plan when no slot runs and the slot the
// active deployment created is stopped: a stop-then-start deploy (D-42) was
// interrupted between stopping it and its new slot serving. The deploy's own
// slots, none running, are removed first: one that is restarting could
// otherwise come up next to it, two containers on the same data.
func restartPrevious(in recoveryInput) (recoveryPlan, bool) {
	d := in.Deployment
	if in.ActiveDeployment == "" || in.ActiveDeployment == d.ID {
		return recoveryPlan{}, false
	}
	var prev *ContainerState
	var remove []string
	for i, c := range in.Containers {
		if !c.Slot || c.ServiceID != d.ServiceID {
			continue
		}
		switch {
		case prev == nil && c.DeploymentID == in.ActiveDeployment && c.State == "exited":
			prev = &in.Containers[i]
		case containerOrigin(c, d, false) == originNew:
			remove = append(remove, c.ID)
		}
	}
	if prev == nil {
		return recoveryPlan{}, false
	}
	running := "running"
	return recoveryPlan{
		RuntimeStatus:      &running,
		RemoveContainerIDs: remove,
		StartContainerIDs:  []string{prev.ID},
		Detail: "the restart came mid-deploy, after the previous container " + prev.Name +
			" was stopped for the new one (stop then start), so it was started again",
	}, true
}

// restartFailedPlan replaces a restartPrevious plan that could not be
// carried out: nothing of the service runs.
func restartFailedPlan(err error) recoveryPlan {
	errorStatus := "error"
	return recoveryPlan{
		RuntimeStatus:  &errorStatus,
		NeedsAttention: true,
		Detail: fmt.Sprintf("the restart came mid-deploy, after the previous container was stopped for the new one, "+
			"and it could not be started again (%v); redeploy or roll back", err),
	}
}

// removalFailedPlan replaces a mid-switch plan whose removal failed: both
// slots keep running.
func removalFailedPlan(err error) recoveryPlan {
	running := "running"
	return recoveryPlan{
		RuntimeStatus:  &running,
		NeedsAttention: true,
		Detail:         fmt.Sprintf("the restart came mid-switch, but the new container could not be removed (%v), so both slots keep running; redeploy to settle it", err),
	}
}

// uncheckedPlan is the plan when the service's containers could not be
// checked: nothing on the service changes.
func uncheckedPlan(err error) recoveryPlan {
	return recoveryPlan{
		NeedsAttention: true,
		Detail:         fmt.Sprintf("what runs could not be checked (%v), so the service's status was left as it was", err),
	}
}

// countNames renders "2 containers (a, b)", or none when names is empty.
func countNames(names []string, none, noun string) string {
	switch len(names) {
	case 0:
		return none
	case 1:
		return "1 " + noun + " (" + names[0] + ")"
	default:
		return fmt.Sprintf("%d %ss (%s)", len(names), noun, strings.Join(names, ", "))
	}
}
