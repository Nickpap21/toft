package deployment

import (
	"context"
	"testing"
)

// onServer answers DeployRunningOnServer only.
type onServer struct {
	Repository
	running map[string]bool
}

func (r onServer) DeployRunningOnServer(_ context.Context, id string) (bool, error) {
	return r.running[id], nil
}

// A move counts as a deploy on the server it moves to, which
// services.server_id names only once it succeeds (D-135).
func TestDeployRunningSeesMoves(t *testing.T) {
	s := &Service{repo: onServer{running: map[string]bool{"a": true}}}
	ctx := context.Background()
	check := func(id string, want bool) {
		t.Helper()
		if got, err := s.DeployRunning(ctx, id); err != nil || got != want {
			t.Errorf("DeployRunning(%q) = %v, %v; want %v", id, got, err, want)
		}
	}
	check("a", true)
	check("b", false)
	s.moving.Store("dep-1", "b")
	check("b", true)
	check("", false)
	s.moving.Store("dep-2", "")
	check("", true)
	s.moving.Delete("dep-1")
	check("b", false)
}
