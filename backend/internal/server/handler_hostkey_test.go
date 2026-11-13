package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshclient/sshtest"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type auditEntry struct{ org, actor, action, resource string }

type fakeAudit struct {
	mu      sync.Mutex
	entries []auditEntry
	details []map[string]any
}

func (a *fakeAudit) WriteAuditLog(_ context.Context, orgID, actorID, action, resource string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, auditEntry{orgID, actorID, action, resource})
}

func (a *fakeAudit) WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any) {
	a.WriteAuditLog(ctx, orgID, actorID, action, resource)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.details = append(a.details, detail)
}

// hostKeyRouter mounts the server routes behind a stand-in for
// middleware.AuthWithOrg (the literal context keys it sets), for a session
// that confirmed its password (D-53).
func hostKeyRouter(s *Service, audit *fakeAudit, userID, orgID string) *gin.Engine {
	return serverRouter(s, audit, userID, orgID, true)
}

// serverRouter is hostKeyRouter with the password confirmation chosen.
func serverRouter(s *Service, audit *fakeAudit, userID, orgID string, reauthenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("orgID", orgID)
		if reauthenticated {
			c.Set("reauthenticated", true)
		}
		c.Next()
	})
	NewHandler(s, audit, zap.NewNop()).RegisterRoutes(g)
	return r
}

func callJSON(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	}
	return w.Code, out
}

func TestHostKeyHandlers(t *testing.T) {
	pinned := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	presented := authorized(sshtest.NewEd25519Signer(t).PublicKey())
	presentedFP := fingerprint(t, presented)

	setup := func(scanErr error) (*Service, *fakeRepo, *fakeAudit) {
		repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", Name: "web-1", IPAddress: "203.0.113.7", Port: 22, HostKey: pinned})
		s := NewService(repo, "")
		s.WithOperatorChecker(fakeOperators{"op": true})
		s.scan = func(context.Context, sshclient.ScanConfig) (ssh.PublicKey, error) {
			if scanErr != nil {
				return nil, scanErr
			}
			return sshclient.ParseHostKey(presented)
		}
		return s, repo, &fakeAudit{}
	}
	const resetPath = "/api/v1/servers/" + testServerID + "/host-key"
	const scanPath = resetPath + "/scan"

	t.Run("scan reports both fingerprints to any member of the org", func(t *testing.T) {
		s, _, audit := setup(nil)
		code, body := callJSON(t, hostKeyRouter(s, audit, "dev", "org-a"), http.MethodPost, scanPath, nil)
		require.Equal(t, http.StatusOK, code, body)
		data := body["data"].(map[string]any)
		assert.Equal(t, presentedFP, data["presented_fingerprint"])
		assert.Equal(t, fingerprint(t, pinned), data["pinned_fingerprint"])
		assert.Equal(t, false, data["matches_pinned"])
		assert.Equal(t, "ssh-ed25519", data["key_type"])
	})

	t.Run("scan of another org's server is not found", func(t *testing.T) {
		s, _, audit := setup(nil)
		code, _ := callJSON(t, hostKeyRouter(s, audit, "op", "org-b"), http.MethodPost, scanPath, nil)
		assert.Equal(t, http.StatusNotFound, code)
	})

	t.Run("scan of an unreachable server is a 502", func(t *testing.T) {
		s, _, audit := setup(errors.New("connection refused"))
		code, body := callJSON(t, hostKeyRouter(s, audit, "op", "org-a"), http.MethodPost, scanPath, nil)
		assert.Equal(t, http.StatusBadGateway, code)
		assert.Contains(t, body["error"], "connection refused")
	})

	t.Run("reset is for instance operators only, even workspace owners", func(t *testing.T) {
		s, repo, audit := setup(nil)
		code, body := callJSON(t, hostKeyRouter(s, audit, "owner", "org-a"), http.MethodPut, resetPath,
			map[string]string{"fingerprint": presentedFP})
		assert.Equal(t, http.StatusForbidden, code)
		assert.Contains(t, body["error"], "instance operators")
		assert.Empty(t, repo.replaced)
		assert.Empty(t, audit.entries)
	})

	t.Run("reset needs the operator's password confirmed", func(t *testing.T) {
		s, repo, audit := setup(nil)
		code, body := callJSON(t, serverRouter(s, audit, "op", "org-a", false), http.MethodPut, resetPath,
			map[string]string{"fingerprint": presentedFP})
		assert.Equal(t, http.StatusForbidden, code)
		assert.Equal(t, "reauth_required", body["data"].(map[string]any)["code"])
		assert.Empty(t, repo.replaced)
		assert.Empty(t, audit.entries)
	})

	t.Run("reset needs a fingerprint", func(t *testing.T) {
		s, _, audit := setup(nil)
		code, _ := callJSON(t, hostKeyRouter(s, audit, "op", "org-a"), http.MethodPut, resetPath, map[string]string{})
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = callJSON(t, hostKeyRouter(s, audit, "op", "org-a"), http.MethodPut, resetPath,
			map[string]string{"fingerprint": "MD5:00:11"})
		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("reset to a key the server no longer presents is a 409 naming it", func(t *testing.T) {
		s, repo, audit := setup(nil)
		other := fingerprint(t, authorized(sshtest.NewEd25519Signer(t).PublicKey()))
		code, body := callJSON(t, hostKeyRouter(s, audit, "op", "org-a"), http.MethodPut, resetPath,
			map[string]string{"fingerprint": other})
		assert.Equal(t, http.StatusConflict, code)
		assert.Contains(t, body["error"], presentedFP)
		data := body["data"].(map[string]any)
		assert.Equal(t, presentedFP, data["presented_fingerprint"])
		assert.Equal(t, "ssh-ed25519", data["key_type"])
		assert.Empty(t, repo.replaced)
		assert.Empty(t, audit.entries)
	})

	t.Run("reset of an unreachable server is a 502", func(t *testing.T) {
		s, _, audit := setup(errors.New("no route to host"))
		code, _ := callJSON(t, hostKeyRouter(s, audit, "op", "org-a"), http.MethodPut, resetPath,
			map[string]string{"fingerprint": presentedFP})
		assert.Equal(t, http.StatusBadGateway, code)
	})

	t.Run("reset pins the confirmed key, audits and returns the server", func(t *testing.T) {
		s, repo, audit := setup(nil)
		code, body := callJSON(t, hostKeyRouter(s, audit, "op", "org-a"), http.MethodPut, resetPath,
			map[string]string{"fingerprint": presentedFP})
		require.Equal(t, http.StatusOK, code, body)
		data := body["data"].(map[string]any)
		assert.Equal(t, presentedFP, data["host_key_fingerprint"])
		assert.Equal(t, "ssh-ed25519", data["host_key_type"])
		assert.NotNil(t, data["host_key_pinned_at"])
		assert.NotContains(t, data, "host_key", "the raw key is never returned")
		assert.Equal(t, []string{presented}, repo.replaced)
		assert.Equal(t, []auditEntry{{"org-a", "op", "server.host_key_reset", "server:" + testServerID}}, audit.entries)
	})
}
