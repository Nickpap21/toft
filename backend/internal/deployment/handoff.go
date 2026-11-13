package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/server"
	"go.uber.org/zap"
)

// Deploys a server's agent runs (D-111).
//
// When a server's agent runs rollouts, the panel hands each one to it (the
// orchestrator decides, hooks.HandOff records it first). A panel restart
// then leaves the deploy in progress, since MarkInterrupted skips it, and
// FollowHandedOff follows it again from the log lines the panel has to the
// agent's result, recording that as the pipeline would have. A deploy the
// agent never received is interrupted and recovered like any other.

// RolloutFollower is an Orchestrator whose server's agent can be asked
// about a rollout it runs. Satisfied by *docker.RemoteOrchestrator.
type RolloutFollower interface {
	FollowRollout(ctx context.Context, deploymentID string, offset int, hooks rollout.Hooks) (rollout.Result, error)
}

// followRetryDelay is how long FollowHandedOff waits before trying a server
// it couldn't reach again (a variable so tests can shorten it).
var followRetryDelay = 30 * time.Second

// handOffInput is what the panel needs to record a handed-off deploy's
// outcome after a restart: the pipeline's input less what only the agent
// needs (the environment, credentials, the container itself).
type handOffInput struct {
	OrgID              string
	ProjectID          string
	ServiceName        string
	EnvironmentName    string
	AddonSlug          string
	RepositoryID       string
	Preview            bool
	RollbackImageTag   string
	CommitSHA          string
	Compose            bool
	PostDeployCommands []PostDeployCommand
	NetPlan            netplan.Plan
}

func newHandOffInput(input TriggerInput, compose bool) handOffInput {
	h := handOffInput{
		OrgID: input.OrgID, ProjectID: input.ProjectID, ServiceName: input.ServiceName,
		EnvironmentName: input.EnvironmentName, AddonSlug: input.AddonSlug, RepositoryID: input.RepositoryID,
		Preview: input.Preview, RollbackImageTag: input.RollbackImageTag, CommitSHA: input.CommitSHA,
		Compose: compose, NetPlan: input.netPlan,
	}
	if compose {
		// A single-container deploy's post-deploy commands run on the agent.
		h.PostDeployCommands = input.PostDeployCommands
	}
	return h
}

func (h handOffInput) trigger(serviceID string) TriggerInput {
	return TriggerInput{
		ServiceID: serviceID, OrgID: h.OrgID, ProjectID: h.ProjectID, ServiceName: h.ServiceName,
		EnvironmentName: h.EnvironmentName, AddonSlug: h.AddonSlug, RepositoryID: h.RepositoryID,
		Preview: h.Preview, RollbackImageTag: h.RollbackImageTag, CommitSHA: h.CommitSHA,
		PostDeployCommands: h.PostDeployCommands, netPlan: h.NetPlan,
	}
}

// handOffHook is hooks.HandOff for deploymentID's rollout of imageTag. A
// move stays with the panel: completing it changes two servers.
func (s *Service) handOffHook(ctx context.Context, deploymentID string, input TriggerInput, imageTag string, compose bool) func() error {
	if input.MoveFrom != nil {
		return nil
	}
	return func() error {
		raw, err := json.Marshal(newHandOffInput(input, compose))
		if err != nil {
			return fmt.Errorf("encode the hand-off: %w", err)
		}
		return s.repo.MarkHandedOff(ctx, deploymentID, imageTag, raw)
	}
}

// FollowHandedOff is run at startup, after MarkInterrupted and before
// anything can ask for a deploy: every deploy a server's agent was running
// when the panel stopped keeps its service's turn (takeTurn) from here, so
// a deploy asked for meanwhile waits for it, as it did before the restart.
// The follow it returns follows each one, in its own goroutine since each
// waits on its own server, and returns once all are recorded or ctx ends.
func (s *Service) FollowHandedOff(ctx context.Context) (follow func()) {
	// The API waits for this read, as it does for MarkInterrupted's.
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	deps, err := s.repo.ListHandedOff(listCtx)
	cancel()
	if err != nil {
		s.log.Error("could not list the deploys servers' agents run", zap.Error(err))
		return func() {}
	}
	var each []func()
	for _, h := range deps {
		if _, running := s.inflight.Load(h.Deployment.ID); running {
			continue
		}
		var in handOffInput
		if err := json.Unmarshal(h.Input, &in); err != nil {
			s.log.Error("a handed-off deploy's input can't be read", zap.String("deployment_id", h.Deployment.ID), zap.Error(err))
			each = append(each, func() { s.interruptHandedOff(ctx, h.Deployment) })
			continue
		}
		_, done, err := s.takeTurn(h.Deployment.ServiceID, false)
		if err != nil {
			done = func() {}
		}
		each = append(each, func() {
			defer done()
			s.followHandedOff(ctx, h.Deployment, in)
		})
	}
	return func() {
		var wg sync.WaitGroup
		for _, f := range each {
			wg.Go(f)
		}
		wg.Wait()
	}
}

// followHandedOff follows d on its server's agent until it is recorded,
// or ctx ends or the panel shuts down (the next start follows it again).
func (s *Service) followHandedOff(ctx context.Context, d Deployment, in handOffInput) {
	input := in.trigger(d.ServiceID)
	wctx := context.WithoutCancel(ctx)
	appendLog := func(line string) { s.recoveryLog(wctx, d.ID, line) }
	appendLog(rollout.PanelLinePrefix + "The panel restarted; following the rollout the server's agent runs")
	waiting := false
	for {
		orch, cleanup, err := s.orchestratorOf(ctx, d.ServiceID)
		if errors.Is(err, errNoAgent) {
			s.interruptHandedOff(ctx, d)
			return
		}
		if err == nil {
			follower, ok := orch.(RolloutFollower)
			if !ok {
				cleanup()
				s.interruptHandedOff(ctx, d)
				return
			}
			offset, err := s.repo.CountAgentLogLines(ctx, d.ID, rollout.PanelLinePrefix)
			if err != nil {
				cleanup()
				s.log.Error("could not count a handed-off deploy's log lines; trying again", zap.String("deployment_id", d.ID), zap.Error(err))
				if !sleepCtx(ctx, followRetryDelay) || s.shuttingDown.Load() {
					return
				}
				continue
			}
			res, err := follower.FollowRollout(ctx, d.ID, offset, rollout.Hooks{Log: appendLog, Abandon: s.shuttingDown.Load, Gone: s.agentGone(ctx, d.ServiceID)})
			switch {
			case err == nil:
				s.recordHandedOff(wctx, d, input, in.Compose, orch, res, appendLog)
			case errors.Is(err, rollout.ErrUnknownRollout), errors.Is(err, rollout.ErrAgentGone):
				s.interruptHandedOff(ctx, d)
			}
			cleanup()
			return
		}
		if !waiting {
			waiting = true
			appendLog(rollout.PanelLinePrefix + "Waiting to reach the server's agent: " + err.Error())
		}
		if !sleepCtx(ctx, followRetryDelay) || s.shuttingDown.Load() {
			return
		}
	}
}

// errNoAgent: the service's server no longer connects through an agent,
// which could be asked about the deploy.
var errNoAgent = errors.New("the service's server has no agent")

// agentServerOf returns serviceID's server, errNoAgent when it has no
// agent (none, moved to SSH, or revoked).
func (s *Service) agentServerOf(ctx context.Context, serviceID string) (*server.Server, error) {
	if s.serviceFetcher == nil || s.serverReader == nil {
		return nil, errors.New("no service or server reader")
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("load the service: %w", err)
	}
	if svc.ServerID == "" {
		return nil, errNoAgent
	}
	srv, err := s.serverReader.GetServer(ctx, svc.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load its server: %w", err)
	}
	if srv == nil || srv.Connection != server.ConnectionAgent || srv.AgentKey == "" {
		return nil, errNoAgent
	}
	return srv, nil
}

// agentGone is rollout.Hooks.Gone for serviceID: its server no longer
// connects through an agent. A failed read is not gone. It reads past the
// deploy's time limit, as the panel follows the agent's rollout past it.
func (s *Service) agentGone(ctx context.Context, serviceID string) func() bool {
	return func() bool {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryWriteTimeout)
		defer cancel()
		_, err := s.agentServerOf(ctx, serviceID)
		return errors.Is(err, errNoAgent)
	}
}

// orchestratorOf returns the orchestrator of serviceID's server, errNoAgent
// when that server has no agent.
func (s *Service) orchestratorOf(ctx context.Context, serviceID string) (Orchestrator, func(), error) {
	srv, err := s.agentServerOf(ctx, serviceID)
	if err != nil {
		return nil, nil, err
	}
	return s.factory.ForServer(srv)
}

// recordHandedOff records the agent's result for d: a compose deploy's up
// is followed by what the panel does after it (finishCompose).
func (s *Service) recordHandedOff(ctx context.Context, d Deployment, input TriggerInput, compose bool, orch Orchestrator, res rollout.Result, appendLog func(string)) {
	if compose {
		var upErr error
		if res.Step != "" {
			upErr = errors.New(res.Err)
		}
		s.finishCompose(ctx, d.ID, input, orch, d.ImageTag, upErr, appendLog)
		return
	}
	s.recordRollout(ctx, d.ID, input, d.ImageTag, res, appendLog)
}

// interruptHandedOff fails d as interrupted and reconciles its service
// with what runs, as recovery does for any deploy a restart cut short: its
// agent never received it, or no longer can be asked.
func (s *Service) interruptHandedOff(ctx context.Context, d Deployment) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryWriteTimeout)
	defer cancel()
	if err := s.repo.ClearHandOff(wctx, d.ID); err != nil {
		s.log.Error("could not take a handed-off deploy back", zap.String("deployment_id", d.ID), zap.Error(err))
	}
	if _, err := s.repo.UpdateStatus(wctx, d.ID, StatusFailed, d.ImageTag, d.CommitSHA, interruptedSummary); err != nil {
		s.log.Error("failed to mark a handed-off deploy interrupted", zap.String("deployment_id", d.ID), zap.Error(err))
		return
	}
	s.recoveryLog(wctx, d.ID, "✗ Interrupted: the panel restarted, and the server's agent doesn't have this deploy, so it is marked failed")
	if s.repos != nil {
		s.repos.FailDeployChecks(wctx, []string{d.ID}, "Interrupted: the panel restarted during this deploy")
	}
	s.ReconcileAfterRestart(ctx, []Deployment{d})
}
