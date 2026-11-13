package deployment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultTimeoutMinutes is how long a deploy may run when its service sets
// no limit of its own (D-125).
const DefaultTimeoutMinutes = 60

// timeoutUnit is what a service's deploy_timeout_minutes counts in (a
// variable so tests can shorten it).
var timeoutUnit = time.Minute

// TimedOutError is the cause a deploy's context is cancelled with when the
// deploy runs past its time limit (D-125).
type TimedOutError struct{ Minutes int }

func (e *TimedOutError) Error() string {
	return fmt.Sprintf("timed out after %d minutes, the service's deploy time limit", e.Minutes)
}

// deployClock is a deploy's time limit (D-125). It cancels the pipeline's
// context with a *TimedOutError once the deploy has run for its limit, not
// counting the time it waited for a build slot (pause, resume).
type deployClock struct {
	mu      sync.Mutex
	timer   *time.Timer
	left    time.Duration // as of since, while running
	since   time.Time
	running bool
	step    string // the pipeline's step outside a rollout, for a timeout's summary
}

// startClock starts a limit of minutes on a context of parent's.
func startClock(parent context.Context, minutes int) (context.Context, *deployClock) {
	ctx, cancel := context.WithCancelCause(parent)
	limit := time.Duration(minutes) * timeoutUnit
	c := &deployClock{left: limit, since: time.Now(), running: true}
	c.timer = time.AfterFunc(limit, func() { cancel(&TimedOutError{Minutes: minutes}) })
	return ctx, c
}

// pause stops the clock until resume.
func (c *deployClock) pause() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	c.running = false
	if c.timer.Stop() {
		c.left -= time.Since(c.since)
	} else {
		c.left = 0 // it ran out
	}
}

// resume restarts a paused clock with the time it had left.
func (c *deployClock) resume() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running || c.left <= 0 {
		return
	}
	c.running, c.since = true, time.Now()
	c.timer.Reset(c.left)
}

// stop ends the clock: the deploy ended, or it has no limit from here on
// (a server's agent that can't keep one runs the rest).
func (c *deployClock) stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer.Stop()
	c.running, c.left = false, 0
}

// timeLeft is the time the deploy has left, 0 when it has no limit (any
// more).
func (c *deployClock) timeLeft() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return 0
	}
	return max(c.left-time.Since(c.since), time.Nanosecond)
}

// at records the step the pipeline is in, which a timeout names.
func (c *deployClock) at(step string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.step = step
}

// current is the step at last recorded.
func (c *deployClock) current() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.step
}

// timeLimit is the service's deploy time limit in minutes.
func (s *Service) timeLimit(ctx context.Context, serviceID string) int {
	if s.serviceFetcher == nil || serviceID == "" {
		return DefaultTimeoutMinutes
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil || svc == nil || svc.DeployTimeoutMinutes <= 0 {
		if err != nil {
			s.log.Warn("read the deploy time limit; using the default", zap.String("service_id", serviceID), zap.Error(err))
		}
		return DefaultTimeoutMinutes
	}
	return svc.DeployTimeoutMinutes
}

// settled is ctx for recording how a deploy ended, which its time limit may
// have cancelled: without the cancellation, as before the limit existed.
// When the limit was the cause and the deploy failed, errMsg says so (with
// the rollout's step, when there is one) and the deploy log says it once.
func (s *Service) settled(ctx context.Context, deploymentID, errMsg, step string) (context.Context, string) {
	var timedOut *TimedOutError
	if errMsg != "" && errors.As(context.Cause(ctx), &timedOut) {
		errMsg = timedOut.Error()
		if step != "" {
			errMsg += " (at the " + step + " step)"
		}
		ctx = context.WithoutCancel(ctx)
		if _, err := s.repo.InsertLog(ctx, deploymentID, "✗ The deploy "+errMsg+
			". A deploy that needs longer gets a higher limit in Settings → Resources → Deploy time limit"); err != nil {
			s.log.Warn("failed to insert deployment log", zap.Error(err))
		}
	}
	return context.WithoutCancel(ctx), errMsg // a nested call sees no cause
}
