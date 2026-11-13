package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"go.uber.org/zap"
)

// followRetry is how long FollowRollout waits before asking the agent again
// (a variable so tests can shorten it).
var followRetry = 5 * time.Second

// handOff hands req to the server's agent, which runs it to its end even if
// the panel restarts (D-111), and follows its log to the result.
func (o *RemoteOrchestrator) handOff(ctx context.Context, req rollout.Request, hooks rollout.Hooks) rollout.Result {
	logFn := hooks.Log
	if logFn == nil {
		logFn = func(string) {}
	}
	if req.Spec.Slot.Check != (rollout.ContainerCheck{}) && !o.checks {
		// An older agent would drop the check and keep the image's.
		msg := "the server's agent can't apply the service's container check until it upgrades (it does when it next connects); " +
			"deploy again then, or set the container check back to the image's"
		logFn("✗ " + msg)
		return rollout.Result{Step: rollout.StepConnect, Err: msg}
	}
	if err := hooks.HandOff(); err != nil {
		msg := "record the hand-off to the server's agent: " + err.Error()
		logFn("✗ " + msg)
		return rollout.Result{Step: rollout.StepConnect, Err: msg}
	}
	req.Epoch = o.epoch
	follow := ctx
	switch {
	case hooks.TimeLeft == nil:
	case o.deadlines:
		// The agent ends the rollout at the limit and cleans up after it;
		// the panel follows it to that end rather than stopping at its own.
		req.TimeLeft = hooks.TimeLeft()
		follow = context.WithoutCancel(ctx)
	default:
		if hooks.NoLimit != nil {
			hooks.NoLimit()
		}
		logFn("→ The server's agent can't keep the deploy time limit until it upgrades, so the limit ends here: the agent runs the rest without one")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return rollout.Result{Step: rollout.StepConnect, Err: "encode the rollout: " + err.Error()}
	}
	lines := 0
	var res *rollout.Result
	refused := ""
	sendErr := o.host.SubsystemStream(follow, agentproto.SubsystemRollout, bytes.NewReader(append(raw, '\n')),
		eventSink(&lines, &res, &refused, logFn))
	if res != nil {
		return *res
	}
	if refused != "" {
		// The agent turned the request down: nothing ran.
		logFn("✗ The server's agent refused the rollout: " + refused)
		return rollout.Result{Step: rollout.StepConnect, Err: "the server's agent refused the rollout: " + refused}
	}
	r, err := o.FollowRollout(follow, req.ID(), lines, hooks)
	switch {
	case err == nil:
		return r
	case errors.Is(err, rollout.ErrUnknownRollout):
		msg := fmt.Sprintf("the server's agent never received the rollout (%v)", sendErr)
		logFn("✗ " + msg)
		return rollout.Result{Step: rollout.StepConnect, Err: msg}
	case errors.Is(err, rollout.ErrAgentGone):
		msg := "the server no longer connects through its agent, so how the rollout ended is unknown"
		logFn("✗ " + msg)
		return rollout.Result{Step: rollout.StepConnect, Err: msg, Changed: true}
	default:
		// The panel stops following (it is shutting down, or the deploy's
		// context ended); the agent carries on, and the next start picks
		// the rollout up again.
		return rollout.Result{Step: rollout.StepConnect, Err: err.Error(), Changed: true}
	}
}

// FollowRollout follows the rollout the server's agent runs for deploymentID
// from line offset of its log to its result, through lost connections: the
// agent carries on meanwhile (D-111). It returns rollout.ErrUnknownRollout
// when the agent holds no such rollout, rollout.ErrAgentGone when hooks.Gone
// says the server no longer connects through it, and stops with an error
// when ctx ends or hooks.Abandon says the panel is shutting down.
func (o *RemoteOrchestrator) FollowRollout(ctx context.Context, deploymentID string, offset int, hooks rollout.Hooks) (rollout.Result, error) {
	logFn := hooks.Log
	if logFn == nil {
		logFn = func(string) {}
	}
	attach, err := json.Marshal(rollout.Attach{ID: deploymentID, Offset: offset})
	if err != nil {
		return rollout.Result{}, fmt.Errorf("encode the attach: %w", err)
	}
	waiting := false
	for {
		var res *rollout.Result
		refused := ""
		lines := 0
		err := o.host.SubsystemStream(ctx, agentproto.SubsystemRolloutAttach, bytes.NewReader(append(attach, '\n')),
			eventSink(&lines, &res, &refused, logFn))
		if res != nil {
			return *res, nil
		}
		if strings.HasPrefix(refused, rollout.UnknownRolloutPrefix) {
			return rollout.Result{}, rollout.ErrUnknownRollout
		}
		if offset += lines; lines > 0 {
			if attach, err = json.Marshal(rollout.Attach{ID: deploymentID, Offset: offset}); err != nil {
				return rollout.Result{}, fmt.Errorf("encode the attach: %w", err)
			}
		}
		if hooks.Abandon != nil && hooks.Abandon() {
			return rollout.Result{}, errors.New("the panel is shutting down; the server's agent carries on with the rollout")
		}
		if hooks.Gone != nil && hooks.Gone() {
			return rollout.Result{}, rollout.ErrAgentGone
		}
		if !waiting {
			waiting = true
			o.log.Info("waiting for the server's agent to follow its rollout", zap.String("deployment_id", deploymentID), zap.Error(err))
			logFn(rollout.PanelLinePrefix + "Lost touch with the server's agent, which carries on with the rollout; waiting for it to answer again")
		}
		select {
		case <-ctx.Done():
			return rollout.Result{}, ctx.Err()
		case <-time.After(followRetry):
		}
	}
}

// eventSink reads an agent's answer about a rollout: its log lines go to
// logFn and are counted, the result to res, and a refusal ("error: ...",
// not an event) to refused.
func eventSink(lines *int, res **rollout.Result, refused *string, logFn func(string)) func(string) {
	return func(line string) {
		if msg, ok := strings.CutPrefix(line, "error: "); ok {
			*refused = msg
			return
		}
		var ev rollout.Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		if ev.Result != nil {
			*res = ev.Result
			return
		}
		*lines++
		logFn(ev.Log)
	}
}
