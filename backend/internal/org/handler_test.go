package org_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newOrgRouter stands in for middleware.AuthWithOrg, setting the literal
// context keys it uses ("userID", "orgID", "memberRole").
func newOrgRouter(svc *org.Service, userID, orgID, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("orgID", orgID)
		c.Set("memberRole", role)
		c.Next()
	})
	org.NewHandler(svc, zap.NewNop()).RegisterRoutes(g)
	return r
}

func send(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	}
	if w.Body.Len() > 0 {
		apispec.Check(t, method, path, w.Code, out)
	} else {
		apispec.Check(t, method, path, w.Code, nil)
	}
	return w.Code, out
}

func TestCreateOrgHandler(t *testing.T) {
	checker := &fakeChecker{operators: map[string]bool{"op-1": true}}

	repo := &fakeRepository{}
	r := newOrgRouter(org.NewService(repo, zap.NewNop()).WithOperatorChecker(checker), "user-1", "org-1", "owner")
	code, body := send(t, r, http.MethodPost, "/api/v1/org", map[string]string{"name": "Mine"})
	assert.Equal(t, http.StatusForbidden, code, "a workspace owner who is not an operator")
	assert.Equal(t, "only instance operators can create workspaces", body["error"])
	assert.Empty(t, repo.createdNames)

	repo = &fakeRepository{}
	r = newOrgRouter(org.NewService(repo, zap.NewNop()).WithOperatorChecker(checker), "op-1", "", "")
	code, body = send(t, r, http.MethodPost, "/api/v1/org", map[string]string{"name": "Mine"})
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, "org-created", body["data"].(map[string]any)["id"])
}

func TestListInvitesHandler_OwnerOnly(t *testing.T) {
	for _, role := range []string{org.RoleDeveloper, org.RoleViewer, ""} {
		r := newOrgRouter(org.NewService(&fakeRepository{}, zap.NewNop()), "user-1", "org-1", role)
		code, _ := send(t, r, http.MethodGet, "/api/v1/org/invites", nil)
		assert.Equal(t, http.StatusForbidden, code, "role %q must not see invite tokens", role)
	}

	r := newOrgRouter(org.NewService(&fakeRepository{}, zap.NewNop()), "user-1", "org-1", org.RoleOwner)
	code, _ := send(t, r, http.MethodGet, "/api/v1/org/invites", nil)
	assert.Equal(t, http.StatusOK, code)
}

func TestCreateInviteHandler_InvalidRole_400(t *testing.T) {
	repo := &fakeRepository{}
	r := newOrgRouter(org.NewService(repo, zap.NewNop()), "user-1", "org-1", org.RoleOwner)

	code, _ := send(t, r, http.MethodPost, "/api/v1/org/invites", map[string]string{"role": "admin"})

	assert.Equal(t, http.StatusBadRequest, code)
	assert.Empty(t, repo.inviteRoles)
}

func TestAcceptInviteHandler(t *testing.T) {
	valid := &org.Invite{ID: "invite-1", OrgID: "org-2", Role: org.RoleOwner, ExpiresAt: time.Now().Add(time.Hour), CreatorActive: true}

	repo := &fakeRepository{invite: valid, acceptErr: org.ErrAlreadyMember}
	r := newOrgRouter(org.NewService(repo, zap.NewNop()), "user-1", "org-1", org.RoleViewer)
	code, body := send(t, r, http.MethodPost, "/api/v1/org/invites/accept", map[string]string{"token": "tok"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body["error"], "already a member")

	repo = &fakeRepository{invite: valid, acceptErr: org.ErrInviteUsed}
	r = newOrgRouter(org.NewService(repo, zap.NewNop()), "user-1", "org-1", org.RoleViewer)
	code, body = send(t, r, http.MethodPost, "/api/v1/org/invites/accept", map[string]string{"token": "tok"})
	assert.Equal(t, http.StatusBadRequest, code, "lost a concurrent redemption")
	assert.Equal(t, "this invite has already been used", body["error"])

	fromDeactivated := *valid
	fromDeactivated.CreatorActive = false
	repo = &fakeRepository{invite: &fromDeactivated}
	r = newOrgRouter(org.NewService(repo, zap.NewNop()), "user-1", "org-1", org.RoleViewer)
	code, body = send(t, r, http.MethodPost, "/api/v1/org/invites/accept", map[string]string{"token": "tok"})
	assert.Equal(t, http.StatusBadRequest, code, "invite from a deactivated user")
	assert.Contains(t, body["error"], "expired")
}

type addableOps map[string]bool

func (o addableOps) IsOperator(_ context.Context, id string) (bool, error) { return o[id], nil }

// Suggestions for "Has an account here" (D-58): owners only; every account
// only when the caller is an instance operator.
func TestListAddableAccounts(t *testing.T) {
	repo := &fakeRepository{}
	code, _ := send(t, newOrgRouter(org.NewService(repo, zap.NewNop()), "u-1", "org-1", "developer"), http.MethodGet, "/api/v1/org/members/addable", nil)
	assert.Equal(t, http.StatusForbidden, code)

	code, body := send(t, newOrgRouter(org.NewService(repo, zap.NewNop()), "u-1", "org-1", "owner"), http.MethodGet, "/api/v1/org/members/addable", nil)
	require.Equal(t, http.StatusOK, code)
	rows := body["data"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, "nine@example.com", rows[0].(map[string]any)["email"])
	assert.Equal(t, true, rows[0].(map[string]any)["former"])
	assert.Equal(t, []bool{false}, repo.addableCalls, "a plain owner never gets every account")

	svc := org.NewService(repo, zap.NewNop()).WithOperatorChecker(addableOps{"u-1": true})
	code, _ = send(t, newOrgRouter(svc, "u-1", "org-1", "owner"), http.MethodGet, "/api/v1/org/members/addable", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []bool{false, true}, repo.addableCalls)
}

func TestMembershipChanges(t *testing.T) {
	cases := []struct {
		name     string
		role     string // the caller's
		method   string
		path     string
		body     any
		repoErr  error
		wantCode int
		wantCall string
	}{
		{"owner changes a role", "owner", http.MethodPut, "/api/v1/org/members/u-2/role", map[string]string{"role": "viewer"}, nil, http.StatusNoContent, "org-1/u-2/viewer"},
		{"a developer can't", "developer", http.MethodPut, "/api/v1/org/members/u-2/role", map[string]string{"role": "viewer"}, nil, http.StatusForbidden, ""},
		{"unknown role", "owner", http.MethodPut, "/api/v1/org/members/u-2/role", map[string]string{"role": "admin"}, nil, http.StatusBadRequest, ""},
		{"demoting the last owner", "owner", http.MethodPut, "/api/v1/org/members/u-2/role", map[string]string{"role": "developer"}, org.ErrLastOwner, http.StatusConflict, "org-1/u-2/developer"},
		{"not a member", "owner", http.MethodPut, "/api/v1/org/members/u-9/role", map[string]string{"role": "viewer"}, org.ErrNotMember, http.StatusNotFound, "org-1/u-9/viewer"},
		{"owner removes a member", "owner", http.MethodDelete, "/api/v1/org/members/u-2", nil, nil, http.StatusNoContent, "org-1/u-2/"},
		{"owner adds an account by email", "owner", http.MethodPost, "/api/v1/org/members", map[string]string{"email": "bob@example.com", "role": "developer"}, nil, http.StatusCreated, "org-1/bob@example.com/developer"},
		{"a developer can't add", "developer", http.MethodPost, "/api/v1/org/members", map[string]string{"email": "bob@example.com", "role": "developer"}, nil, http.StatusForbidden, ""},
		{"adding needs a valid role", "owner", http.MethodPost, "/api/v1/org/members", map[string]string{"email": "bob@example.com", "role": "admin"}, nil, http.StatusBadRequest, ""},
		{"adding an unknown email", "owner", http.MethodPost, "/api/v1/org/members", map[string]string{"email": "zed@example.com", "role": "viewer"}, org.ErrNoSuchAccount, http.StatusNotFound, "org-1/zed@example.com/viewer"},
		{"adding a member again", "owner", http.MethodPost, "/api/v1/org/members", map[string]string{"email": "bob@example.com", "role": "viewer"}, org.ErrAlreadyMember, http.StatusConflict, "org-1/bob@example.com/viewer"},
		{"adding a deactivated account", "owner", http.MethodPost, "/api/v1/org/members", map[string]string{"email": "bob@example.com", "role": "viewer"}, org.ErrAccountInactive, http.StatusConflict, "org-1/bob@example.com/viewer"},
		{"removing yourself", "owner", http.MethodDelete, "/api/v1/org/members/u-1", nil, nil, http.StatusBadRequest, ""},
		{"a viewer leaves", "viewer", http.MethodPost, "/api/v1/org/leave", nil, nil, http.StatusNoContent, "org-1/u-1/"},
		{"the last owner can't leave", "owner", http.MethodPost, "/api/v1/org/leave", nil, org.ErrLastOwner, http.StatusConflict, "org-1/u-1/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{memberErr: tc.repoErr}
			r := newOrgRouter(org.NewService(repo, zap.NewNop()), "u-1", "org-1", tc.role)
			code, _ := send(t, r, tc.method, tc.path, tc.body)
			assert.Equal(t, tc.wantCode, code)
			if tc.wantCall == "" {
				assert.Empty(t, repo.memberChanges)
			} else {
				assert.Equal(t, []string{tc.wantCall}, repo.memberChanges)
			}
		})
	}
}

// A new workspace's name is bounded as a rename's is; the body stays
// optional.
func TestCreateOrgBoundsTheName(t *testing.T) {
	r := newOrgRouter(org.NewService(&fakeRepository{}, zap.NewNop()), "u-1", "org-1", "owner")
	post := func(body string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/org", bytes.NewBufferString(body)))
		return w.Code
	}
	assert.Equal(t, http.StatusBadRequest, post(`{"name":"`+string(bytes.Repeat([]byte("x"), 101))+`"}`))
	assert.Equal(t, http.StatusForbidden, post(""), "no body reaches the operator check")
}
