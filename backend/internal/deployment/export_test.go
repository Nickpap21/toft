package deployment

import (
	"testing"
	"time"
)

// HoldMove puts a move of serviceID in its line, as a deploy that moves the
// service does, until the func it returns is called.
func (s *Service) HoldMove(serviceID string) (ended func()) {
	_, done, _ := s.takeTurn(serviceID, true)
	return done
}

// SetTimeoutUnit makes deploy_timeout_minutes count in d for one test.
func SetTimeoutUnit(t *testing.T, d time.Duration) {
	old := timeoutUnit
	timeoutUnit = d
	t.Cleanup(func() { timeoutUnit = old })
}
