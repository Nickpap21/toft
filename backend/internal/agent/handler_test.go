package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

type lookup struct{}

func (lookup) GetForUser(context.Context, string) (string, string, error) {
	return "org1", "owner", nil
}
func (lookup) RoleIn(context.Context, string, string) (string, error) { return "owner", nil }

// The Agents page's and OAuth's answers match the spec.
func TestHandlersMatchSpec(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _, client := setup(t)
	h := agent.NewHandler(svc, func() string { return "panel.example" }, nil, zap.NewNop())
	r := gin.New()
	v1 := r.Group("/api/v1")
	h.RegisterPublicRoutes(r, v1, func(c *gin.Context) { c.Next() })
	p := v1.Group("")
	p.Use(middleware.AuthWithOrg("secret", nil, lookup{}, nil))
	h.RegisterRoutes(p)
	session, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "owner", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("secret"))

	do := func(method, path, contentType string, body []byte, auth bool) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if auth {
			req.Header.Set("Authorization", "Bearer "+session)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], w.Code, out)
		return w.Code, out
	}

	if st, m := do("GET", "/.well-known/oauth-authorization-server", "", nil, false); st != 200 || m["issuer"] != "https://panel.example" ||
		m["client_id_metadata_document_supported"] != true {
		t.Errorf("metadata %d %v", st, m)
	}
	if st, m := do("GET", "/.well-known/oauth-protected-resource/mcp", "", nil, false); st != 200 || m["resource"] != "https://panel.example/mcp" {
		t.Errorf("resource metadata %d %v", st, m)
	}
	if st, _ := do("POST", "/api/v1/oauth/register", "application/json", []byte(`{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}`), false); st != 201 {
		t.Errorf("register %d", st)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"nope"}, "client_id": {client}}.Encode()
	if st, m := do("POST", "/api/v1/oauth/token", "application/x-www-form-urlencoded", []byte(form), false); st != 400 || m["error"] != "invalid_grant" {
		t.Errorf("token %d %v", st, m)
	}
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {"http://localhost:9/callback"}, "code_challenge": {"c"}, "code_challenge_method": {"S256"}}
	if st, m := do("GET", "/api/v1/oauth/authorize/request?"+q.Encode(), "", nil, true); st != 200 {
		t.Errorf("authorize request %d %v", st, m)
	}
	if st, _ := do("GET", "/api/v1/agents/policy", "", nil, true); st != 200 {
		t.Errorf("policy %d", st)
	}
	if st, _ := do("PUT", "/api/v1/agents/policy", "application/json", []byte(`{"cells":[{"action":"deploy","env_class":"other","decision":"ask"}]}`), true); st != 200 {
		t.Errorf("tighten %d", st)
	}
	if st, m := do("PUT", "/api/v1/agents/policy", "application/json", []byte(`{"cells":[{"action":"deploy","env_class":"other","decision":"allow"}]}`), true); st != 403 || m["code"] != "reauth_required" {
		t.Errorf("loosen without the password %d %v", st, m)
	}
	if st, _ := do("GET", "/api/v1/agents/pause", "", nil, true); st != 200 {
		t.Errorf("pause %d", st)
	}
	if st, _ := do("POST", "/api/v1/agents/pause", "", nil, true); st != 200 {
		t.Errorf("pausing %d", st)
	}
	if st, m := do("DELETE", "/api/v1/agents/pause", "", nil, true); st != 403 || m["code"] != "reauth_required" {
		t.Errorf("resume without the password %d %v", st, m)
	}
	if st, _ := do("GET", "/api/v1/agents/connections", "", nil, true); st != 200 {
		t.Errorf("connections %d", st)
	}
	if st, _ := do("GET", "/api/v1/agents/approvals", "", nil, true); st != 200 {
		t.Errorf("approvals %d", st)
	}
	if st, _ := do("GET", "/api/v1/agents/approvals/00000000-0000-0000-0000-000000000000", "", nil, true); st != 404 {
		t.Errorf("an unknown approval %d", st)
	}
}

// A full instance refuses registration with 429 and when to come back.
func TestRegistrationWhenFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, repo, _ := setup(t)
	for i := range agent.MaxUnusedDCR - 1 {
		id := fmt.Sprint("toft_client_fill", i)
		repo.Clients[id] = &agent.Client{ID: id, ClientID: id, Kind: "dcr"}
	}
	r := gin.New()
	agent.NewHandler(svc, func() string { return "panel.example" }, nil, zap.NewNop()).
		RegisterPublicRoutes(r, r.Group("/api/v1"), func(c *gin.Context) { c.Next() })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/oauth/register", strings.NewReader(`{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	apispec.Check(t, "POST", "/api/v1/oauth/register", w.Code, out)
	if w.Code != 429 || w.Header().Get("Retry-After") != "3600" || out["error"] != "temporarily_unavailable" {
		t.Errorf("full: %d %q %v", w.Code, w.Header().Get("Retry-After"), out)
	}
}
