package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakeOperators map[string]bool

func (f fakeOperators) IsOperator(_ context.Context, id string) (bool, error) { return f[id], nil }

type failingOperators struct{}

func (failingOperators) IsOperator(context.Context, string) (bool, error) {
	return false, errors.New("db down")
}

func TestAuthorizeHostShell(t *testing.T) {
	ctx := context.Background()
	s := NewService(nil, "")
	assert.ErrorIs(t, s.AuthorizeHostShell(ctx, "op"), ErrHostShellOperatorOnly, "no checker: refused")

	s.WithOperatorChecker(fakeOperators{"op": true})
	assert.NoError(t, s.AuthorizeHostShell(ctx, "op"))
	assert.ErrorIs(t, s.AuthorizeHostShell(ctx, "owner"), ErrHostShellOperatorOnly)
	assert.ErrorIs(t, s.AuthorizeHostShell(ctx, ""), ErrHostShellOperatorOnly)

	s.WithOperatorChecker(failingOperators{})
	err := s.AuthorizeHostShell(ctx, "op")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrHostShellOperatorOnly)
}

func TestExecOnServerAndPTYRequireOperator(t *testing.T) {
	s := NewService(nil, "")
	s.WithOperatorChecker(fakeOperators{})
	// The repository is nil: reaching it would panic, so these prove the
	// check runs first.
	assert.ErrorIs(t, s.ExecOnServer(context.Background(), "owner", "srv", "id", func(string) {}), ErrHostShellOperatorOnly)
	_, err := s.OpenPTY(context.Background(), "owner", "srv", 80, 24)
	assert.ErrorIs(t, err, ErrHostShellOperatorOnly)
}

// The host's shell needs an operator whose session confirmed its password
// (D-53), and a non-operator is refused as such, never asked for one.
func TestHostShellHandlersNeedTheConfirmedPassword(t *testing.T) {
	s := NewService(newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", Name: "web-1"}), "")
	s.WithOperatorChecker(fakeOperators{"op": true})
	exec := "/api/v1/servers/" + testServerID + "/exec"
	pty := "/api/v1/servers/" + testServerID + "/pty"

	for _, path := range []string{exec, pty} {
		method := http.MethodGet
		var body any
		if path == exec {
			method, body = http.MethodPost, map[string]string{"command": "id"}
		}
		code, out := callJSON(t, serverRouter(s, &fakeAudit{}, "op", "org-a", false), method, path, body)
		assert.Equal(t, http.StatusForbidden, code, path)
		assert.Equal(t, "reauth_required", out["data"].(map[string]any)["code"], path)

		code, out = callJSON(t, serverRouter(s, &fakeAudit{}, "owner", "org-a", false), method, path, body)
		assert.Equal(t, http.StatusForbidden, code, path)
		assert.Contains(t, out["error"], "instance operators", path)
		assert.Nil(t, out["data"], "a non-operator is not asked for a password")
	}
}

// A command run on a server's host is in its workspace's audit log, at most
// 500 characters of it, whether or not it then reaches the server.
func TestExecOnServerIsAudited(t *testing.T) {
	s := NewService(newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", Name: "web-1"}), "")
	s.WithOperatorChecker(fakeOperators{"op": true})
	exec := "/api/v1/servers/" + testServerID + "/exec"

	audit := &fakeAudit{}
	run := func(command string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, exec, strings.NewReader(`{"command":"`+command+`"}`))
		req.Header.Set("Content-Type", "application/json")
		serverRouter(s, audit, "op", "org-a", true).ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "SSH to managed servers is not configured")
	}
	run("systemctl restart docker")
	assert.Equal(t, []auditEntry{{"org-a", "op", "server.command_run", "server:" + testServerID}}, audit.entries)
	assert.Equal(t, "systemctl restart docker", audit.details[0]["command"])

	run(strings.Repeat("é", 600))
	assert.Equal(t, strings.Repeat("é", 500)+"…", audit.details[1]["command"])
}

// EndShells returns once the open shells have closed, or when its context
// ends.
func TestEndShells(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	h.EndShells(context.Background()) // none open

	h.shells.Add(1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.shells.Done()
	}()
	h.EndShells(context.Background())

	h = NewHandler(nil, nil, nil)
	h.shells.Add(1) // never closes
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	h.EndShells(ctx)
	assert.Error(t, ctx.Err())
}
