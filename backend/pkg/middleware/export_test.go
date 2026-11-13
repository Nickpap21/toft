package middleware

import "time"

// SetActiveRecheckInterval overrides the is_active re-check interval for
// middleware built after the call, and returns a function that restores it.
func SetActiveRecheckInterval(d time.Duration) (restore func()) {
	old := activeRecheckInterval
	activeRecheckInterval = d
	return func() { activeRecheckInterval = old }
}
