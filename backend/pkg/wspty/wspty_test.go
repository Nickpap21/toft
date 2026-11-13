package wspty

import (
	"context"
	"testing"
)

// A request ended because the backend stops says so, through any context
// derived from it; one ended otherwise doesn't.
func TestStopping(t *testing.T) {
	base, end := context.WithCancelCause(context.Background())
	req, cancel := context.WithCancel(base)
	defer cancel()
	if Stopping(req) {
		t.Fatal("stopping before the backend stops")
	}
	end(ErrStopping)
	if !Stopping(req) {
		t.Fatal("not stopping after the backend stops")
	}

	closed, cancelClosed := context.WithCancel(context.Background())
	cancelClosed()
	if Stopping(closed) {
		t.Fatal("a closed tab is not a stop")
	}
}
