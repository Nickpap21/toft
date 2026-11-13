package service

import (
	"encoding/json"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheAPIShowsDeployingWhileADeployRuns(t *testing.T) {
	for _, tc := range []struct {
		svc  Service
		want string
	}{
		{Service{ID: "s1", RuntimeStatus: "running", Deploying: true}, "deploying"},
		{Service{ID: "s1", RuntimeStatus: "error", Deploying: true}, "deploying"},
		{Service{ID: "s1", RuntimeStatus: "running"}, "running"},
	} {
		b, err := json.Marshal(tc.svc)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(b, &got))
		assert.Equal(t, tc.want, got["runtime_status"])
		assert.NotContains(t, got, "Deploying")
		assert.Equal(t, "s1", got["id"], "the other fields are as before")
		assert.Equal(t, tc.svc.RuntimeStatus == "running" && !tc.svc.Deploying, tc.svc.RuntimeStatus == got["runtime_status"])
	}

	// Lists and pointers marshal the same way.
	b, err := json.Marshal([]*Service{{RuntimeStatus: "running", Deploying: true}})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"runtime_status":"deploying"`)

	// A saved service keeps its policy warnings next to its fields.
	b, err = json.Marshal(&SavedService{Service: &Service{ID: "s1", Deploying: true}, PolicyViolations: []composespec.Violation{{Rule: "privileged"}}})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"runtime_status":"deploying"`)
	assert.Contains(t, string(b), `"policy_violations":[{`)
	assert.Contains(t, string(b), `"id":"s1"`)
}

// A running service shows what the alert monitor sees of it: error when
// nothing serves or it crash-loops, unhealthy when it serves but fails its
// health check. A deploy under way wins, and a stored status other than
// running is shown as stored.
func TestTheAPIShowsWhatTheMonitorSees(t *testing.T) {
	for _, tc := range []struct {
		stored                     string
		deploying, down, unhealthy bool
		want                       string
	}{
		{"running", false, true, false, "error"},
		{"running", false, false, true, "unhealthy"},
		{"running", false, true, true, "error"},
		{"running", true, true, false, "deploying"},
		{"stopped", false, true, true, "stopped"},
		{"error", false, false, true, "error"},
		{"running", false, false, false, "running"},
	} {
		assert.Equal(t, tc.want, PresentedStatus(tc.stored, tc.deploying, tc.down, tc.unhealthy), "%+v", tc)
	}
	b, err := json.Marshal(Service{ID: "s1", RuntimeStatus: "running", Unhealthy: true})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"runtime_status":"unhealthy"`)
	assert.NotContains(t, string(b), "Unhealthy")
}
