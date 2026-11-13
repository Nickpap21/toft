package middleware_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secret = "test-secret"

type fakeKeys map[string]string // raw key → user ID

func (f fakeKeys) ValidateAPIKey(ctx context.Context, raw string) (middleware.APIKeyIdentity, error) {
	if id, ok := f[raw]; ok {
		return middleware.APIKeyIdentity{UserID: id}, nil
	}
	if raw == "paas_ci" { // project-scoped, project p-1 in workspace org-2
		return middleware.APIKeyIdentity{UserID: "u-ada", ProjectID: "p-1", OrgID: "org-2", Scopes: []string{"deploy"}}, nil
	}
	return middleware.APIKeyIdentity{}, errors.New("invalid api key")
}

type fakeStatus struct {
	active map[string]bool
	err    error
	// validFrom, when set, refuses tokens issued before it.
	validFrom time.Time
	// orgID and role, when set, are what the identity query found.
	orgID, role string
	// signedOut is the sha256 of a session token that was signed out.
	signedOut string
}

func (f fakeStatus) IsActive(ctx context.Context, userID string) (bool, error) {
	return f.active[userID], f.err
}

func (f fakeStatus) RequestIdentity(ctx context.Context, userID, orgID, sessionHash string) (middleware.Identity, error) {
	return middleware.Identity{Active: f.active[userID], TokensValidFrom: f.validFrom, OrgID: f.orgID, Role: f.role,
		SessionRevoked: sessionHash != "" && sessionHash == f.signedOut}, f.err
}

// fakeOrgs: every user has org-1 open, as owner; u-ada is also a developer
// in org-2.
type fakeOrgs struct{}

func (fakeOrgs) GetForUser(ctx context.Context, userID string) (string, string, error) {
	return "org-1", "owner", nil
}

func (fakeOrgs) RoleIn(ctx context.Context, userID, orgID string) (string, error) {
	if userID == "u-ada" && orgID == "org-2" {
		return "developer", nil
	}
	return "", errors.New("not a member")
}

func jwtFor(t *testing.T, userID string) string {
	return jwtIssuedAt(t, userID, time.Now())
}

func jwtIssuedAt(t *testing.T, userID string, iat time.Time) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID,
		"iat": iat.Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(secret))
	require.NoError(t, err)
	return tok
}

func serve(t *testing.T, status middleware.UserStatusChecker, bearer string) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, fakeKeys{"paas_ada": "u-ada", "paas_bob": "u-bob"}, fakeOrgs{}, status))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, middleware.UserIDFrom(c)) })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestAuthWithOrg_EnforcesIsActive(t *testing.T) {
	status := fakeStatus{active: map[string]bool{"u-ada": true, "u-bob": false}}

	code, body := serve(t, status, jwtFor(t, "u-ada"))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "u-ada", body)

	code, body = serve(t, status, jwtFor(t, "u-bob"))
	assert.Equal(t, http.StatusUnauthorized, code, "deactivated user with a valid JWT")
	assert.Contains(t, body, "deactivated")

	code, _ = serve(t, status, "paas_ada")
	assert.Equal(t, http.StatusOK, code)

	code, _ = serve(t, status, "paas_bob")
	assert.Equal(t, http.StatusUnauthorized, code, "deactivated user with a valid API key")

	code, _ = serve(t, status, jwtFor(t, "u-deleted"))
	assert.Equal(t, http.StatusUnauthorized, code, "a JWT for a user that no longer exists")
}

// Only what auth.Service signs passes: HS256 with an expiry, under the
// session secret (security close-out I4).
func TestAuthWithOrg_RefusesOtherAlgorithmsAndNoExpiry(t *testing.T) {
	status := fakeStatus{active: map[string]bool{"u-ada": true}}
	sign := func(m jwt.SigningMethod, claims jwt.MapClaims) string {
		tok, err := jwt.NewWithClaims(m, claims).SignedString([]byte(secret))
		require.NoError(t, err)
		return tok
	}
	exp := time.Now().Add(time.Hour).Unix()
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "u-ada", "exp": exp}).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	for name, tok := range map[string]string{
		"HS512":    sign(jwt.SigningMethodHS512, jwt.MapClaims{"sub": "u-ada", "exp": exp}),
		"HS384":    sign(jwt.SigningMethodHS384, jwt.MapClaims{"sub": "u-ada", "exp": exp}),
		"no exp":   sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u-ada", "iat": time.Now().Unix()}),
		"alg none": none,
		"expired":  sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u-ada", "exp": time.Now().Add(-time.Minute).Unix()}),
		"tampered": "x." + strings.SplitN(sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u-ada", "exp": exp}), ".", 2)[1],
		// The same signature spelt another way (its last character's unused
		// bits): a new session hash, so a signed-out session would come back.
		"respelt": respell(sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u-ada", "exp": exp})),
	} {
		code, _ := serve(t, status, tok)
		assert.Equal(t, http.StatusUnauthorized, code, name)
	}
	code, _ := serve(t, status, sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u-ada", "exp": exp}))
	assert.Equal(t, http.StatusOK, code, "HS256 with an expiry")
}

func TestAuthWithOrg_StatusCheckError_Denies(t *testing.T) {
	code, _ := serve(t, fakeStatus{err: errors.New("db down")}, jwtFor(t, "u-ada"))
	assert.Equal(t, http.StatusInternalServerError, code)
}

func TestAuthWithOrg_NilStatusChecker_SkipsCheck(t *testing.T) {
	code, _ := serve(t, nil, jwtFor(t, "u-bob"))
	assert.Equal(t, http.StatusOK, code)
}

// switchableStatus lets a test deactivate a user, or break the status check,
// while a request is running.
type switchableStatus struct {
	mu      sync.Mutex
	active  bool
	err     error
	revoked bool
}

func (s *switchableStatus) IsActive(ctx context.Context, userID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.err
}

func (s *switchableStatus) RequestIdentity(ctx context.Context, userID, orgID, _ string) (middleware.Identity, error) {
	active, err := s.IsActive(ctx, userID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return middleware.Identity{Active: active, SessionRevoked: s.revoked}, err
}

func (s *switchableStatus) set(active bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active, s.err = active, err
}

// openStream starts a request whose handler holds the connection open (as the
// PTY and SSE handlers do) until its context ends or the hold elapses. It
// returns once the handler is running, and a channel that reports whether the
// handler saw its context cancelled.
func openStream(t *testing.T, status middleware.UserStatusChecker, hold time.Duration) <-chan bool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, nil, fakeOrgs{}, status))
	started := make(chan struct{})
	cancelled := make(chan bool, 1)
	r.GET("/stream", func(c *gin.Context) {
		close(started)
		select {
		case <-c.Request.Context().Done():
			cancelled <- true
		case <-time.After(hold):
			cancelled <- false
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	req.Header.Set("Authorization", "Bearer "+jwtFor(t, "u-ada"))
	go r.ServeHTTP(httptest.NewRecorder(), req)
	<-started
	return cancelled
}

func TestAuthWithOrg_DeactivationEndsOpenRequests(t *testing.T) {
	defer middleware.SetActiveRecheckInterval(5 * time.Millisecond)()
	status := &switchableStatus{active: true}

	cancelled := openStream(t, status, 5*time.Second)
	status.set(false, nil)

	select {
	case got := <-cancelled:
		assert.True(t, got, "the open request must end once the user is deactivated")
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
}

func TestAuthWithOrg_SignOutEndsOpenRequests(t *testing.T) {
	defer middleware.SetActiveRecheckInterval(5 * time.Millisecond)()
	status := &switchableStatus{active: true}

	cancelled := openStream(t, status, 5*time.Second)
	status.mu.Lock()
	status.revoked = true
	status.mu.Unlock()

	select {
	case got := <-cancelled:
		assert.True(t, got, "the open request must end once its session is signed out")
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
}

func TestAuthWithOrg_OpenRequestsSurviveActiveUserAndFailedChecks(t *testing.T) {
	defer middleware.SetActiveRecheckInterval(5 * time.Millisecond)()

	active := &switchableStatus{active: true}
	assert.False(t, <-openStream(t, active, 100*time.Millisecond), "an active user keeps the stream")

	flaky := &switchableStatus{active: true}
	stream := openStream(t, flaky, 100*time.Millisecond)
	flaky.set(false, errors.New("db down"))
	assert.False(t, <-stream, "a failed re-check does not cut an authorised stream")
}

// A project-scoped key acts in its project's workspace with its owner's role
// there, not in the workspace the owner has open, and carries its scope.
func TestAuthWithOrg_ScopedKeyUsesTheProjectsWorkspace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, fakeKeys{"paas_ada": "u-ada"}, fakeOrgs{}, nil))
	r.GET("/x", func(c *gin.Context) {
		project, scopes, scoped := middleware.APIKeyScopeFrom(c)
		c.JSON(http.StatusOK, gin.H{
			"org": middleware.OrgIDFrom(c), "role": middleware.MemberRoleFrom(c),
			"project": project, "scopes": scopes, "scoped": scoped,
		})
	})
	get := func(bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := get("paas_ci")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"org":"org-2","role":"developer","project":"p-1","scopes":["deploy"],"scoped":true}`, w.Body.String())

	w = get("paas_ada")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"org":"org-1","role":"owner","project":"","scopes":null,"scoped":false}`, w.Body.String(), "unscoped keys are unchanged")
}

func TestAuthWithOrg_ScopedKeyOfAFormerMemberIsRefused(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	keys := scopedKeyFor{middleware.APIKeyIdentity{UserID: "u-bob", ProjectID: "p-1", OrgID: "org-2", Scopes: []string{"deploy"}}}
	r.Use(middleware.AuthWithOrg(secret, keys, fakeOrgs{}, nil))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer paas_any")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

type scopedKeyFor struct{ id middleware.APIKeyIdentity }

func (k scopedKeyFor) ValidateAPIKey(context.Context, string) (middleware.APIKeyIdentity, error) {
	return k.id, nil
}

func TestAuthWithOrg_TokenInURLOnlyOnAWebSocketHandshake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, fakeKeys{"paas_ada": "u-ada"}, fakeOrgs{}, nil))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, middleware.UserIDFrom(c)) })
	call := func(token string, ws bool) int {
		req := httptest.NewRequest(http.MethodGet, "/x?token="+token, nil)
		if ws {
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	for _, tok := range []string{jwtFor(t, "u-ada"), "paas_ada"} {
		assert.Equal(t, http.StatusUnauthorized, call(tok, false), "a token in an ordinary URL is ignored")
		assert.Equal(t, http.StatusOK, call(tok, true), "a WebSocket handshake cannot carry a header")
	}
}

// A password change moves tokens_valid_from: tokens from before it are
// refused, the one issued with (or after) the change passes, and an API key
// is not a session token and passes either way.
func TestAuthWithOrg_RefusesTokensFromBeforeRevocation(t *testing.T) {
	changed := time.Now().Add(-time.Minute)
	status := fakeStatus{active: map[string]bool{"u-ada": true}, validFrom: changed}

	code, body := serve(t, status, jwtIssuedAt(t, "u-ada", changed.Add(-time.Hour)))
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Contains(t, body, "signed out")

	code, _ = serve(t, status, jwtIssuedAt(t, "u-ada", changed))
	assert.Equal(t, http.StatusOK, code, "the token issued in the same second as the change")

	code, _ = serve(t, status, jwtIssuedAt(t, "u-ada", time.Now()))
	assert.Equal(t, http.StatusOK, code)

	code, _ = serve(t, status, "paas_ada")
	assert.Equal(t, http.StatusOK, code, "API keys are revoked by deleting them, not by a password change")
}

// A signed-out session token is refused; the account's other sessions
// carry on.
func TestAuthWithOrg_RefusesASignedOutSession(t *testing.T) {
	out := jwtIssuedAt(t, "u-ada", time.Now())
	sum := sha256.Sum256([]byte(out))
	status := fakeStatus{active: map[string]bool{"u-ada": true}, signedOut: hex.EncodeToString(sum[:])}

	code, body := serve(t, status, out)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Contains(t, body, "signed out")

	code, _ = serve(t, status, jwtIssuedAt(t, "u-ada", time.Now().Add(-time.Hour)))
	assert.Equal(t, http.StatusOK, code)
}

// The identity query carries the open workspace and the role; the org
// lookup is only the fallback.
func TestAuthWithOrg_UsesTheIdentityQuerysWorkspace(t *testing.T) {
	status := fakeStatus{active: map[string]bool{"u-ada": true}, orgID: "org-9", role: "viewer"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, nil, fakeOrgs{}, status))
	r.GET("/x", func(c *gin.Context) {
		c.String(http.StatusOK, middleware.OrgIDFrom(c)+" "+middleware.MemberRoleFrom(c))
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+jwtFor(t, "u-ada"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "org-9 viewer", w.Body.String(), "not fakeOrgs' org-1 owner")
}

// Workspaces for the named-workspace tests (D-52): u-ada belongs to
// orgA (her default, owner) and orgB (viewer), not to orgC.
const (
	orgA = "0a000000-0000-4000-8000-00000000000a"
	orgB = "0b000000-0000-4000-8000-00000000000b"
	orgC = "0c000000-0000-4000-8000-00000000000c"
)

type memberships map[string]string // org → u-ada's role

func (m memberships) IsActive(ctx context.Context, userID string) (bool, error) {
	return userID == "u-ada", nil
}

func (m memberships) RequestIdentity(ctx context.Context, userID, orgID, _ string) (middleware.Identity, error) {
	ident := middleware.Identity{Active: userID == "u-ada"}
	if orgID == "" {
		orgID = orgA
	}
	if role, ok := m[orgID]; ok {
		ident.OrgID, ident.Role = orgID, role
	}
	return ident, nil
}

// namedOrgs answers RoleIn from the same memberships, for the paths that
// have no status checker.
type namedOrgs memberships

func (namedOrgs) GetForUser(ctx context.Context, userID string) (string, string, error) {
	return orgA, "owner", nil
}

func (n namedOrgs) RoleIn(ctx context.Context, userID, orgID string) (string, error) {
	if role, ok := n[orgID]; ok {
		return role, nil
	}
	return "", errors.New("not a member")
}

var adaIn = memberships{orgA: "owner", orgB: "viewer"}

// named serves one request with the given credential and, when set, header
// value and URL query, and returns the status and the body: the workspace
// and role the handler saw, or the refusal.
func named(t *testing.T, keys middleware.APIKeyValidator, status middleware.UserStatusChecker, bearer, header, query string, ws bool) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, keys, namedOrgs(adaIn), status))
	r.GET("/x", func(c *gin.Context) {
		c.String(http.StatusOK, middleware.OrgIDFrom(c)+" "+middleware.MemberRoleFrom(c))
	})
	req := httptest.NewRequest(http.MethodGet, "/x"+query, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if header != "" {
		req.Header.Set(middleware.OrgHeader, header)
	}
	if ws {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestAuthWithOrg_WorksInTheNamedWorkspace(t *testing.T) {
	ada := jwtFor(t, "u-ada")

	code, body := named(t, nil, adaIn, ada, "", "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgA+" owner", body, "no header: the workspace the account last switched to")

	code, body = named(t, nil, adaIn, ada, orgB, "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body, "the named workspace and the role there")

	code, body = named(t, nil, adaIn, ada, strings.ToUpper(orgB), "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body, "compared in canonical form")

	code, body = named(t, nil, nil, ada, orgB, "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body, "without a status checker the org lookup answers")
}

func TestAuthWithOrg_NeverSwapsANamedWorkspace(t *testing.T) {
	ada := jwtFor(t, "u-ada")
	for _, status := range []middleware.UserStatusChecker{adaIn, nil} {
		code, body := named(t, nil, status, ada, orgC, "", false)
		assert.Equal(t, http.StatusForbidden, code, "not a member: refused, not her default")
		assert.Contains(t, body, `"code":"workspace_unavailable"`)
	}

	code, body := named(t, nil, adaIn, ada, "org-a", "", false)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, `"code":"invalid_workspace"`)

	code, _ = named(t, nil, adaIn, "", orgB, "", false)
	assert.Equal(t, http.StatusUnauthorized, code, "a header is no credential")
}

func TestAuthWithOrg_WorkspaceInURLOnlyOnAWebSocketHandshake(t *testing.T) {
	ada := jwtFor(t, "u-ada")

	code, body := named(t, nil, adaIn, ada, "", "?org="+orgB, true)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body)

	code, body = named(t, nil, adaIn, ada, "", "?org="+orgB, false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgA+" owner", body, "?org= on an ordinary request is ignored")

	code, _ = named(t, nil, adaIn, ada, "", "?org="+orgC, true)
	assert.Equal(t, http.StatusForbidden, code)
}

func TestAuthWithOrg_APIKeysAndTheNamedWorkspace(t *testing.T) {
	scoped := scopedKeyFor{middleware.APIKeyIdentity{UserID: "u-ada", ProjectID: "p-1", OrgID: orgB, Scopes: []string{"deploy"}}}

	code, body := named(t, scoped, adaIn, "paas_any", "", "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body, "a scoped key works in its project's workspace")

	code, body = named(t, scoped, adaIn, "paas_any", orgB, "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body, "naming that workspace changes nothing")

	code, body = named(t, scoped, adaIn, "paas_any", orgA, "", false)
	assert.Equal(t, http.StatusForbidden, code, "a scoped key cannot leave its project's workspace")
	assert.Contains(t, body, `"code":"workspace_unavailable"`)

	unscoped := fakeKeys{"paas_ada": "u-ada"}
	code, body = named(t, unscoped, adaIn, "paas_ada", orgB, "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgB+" viewer", body, "an unscoped key may name any of its owner's workspaces")

	code, body = named(t, unscoped, adaIn, "paas_ada", "", "", false)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, orgA+" owner", body, "and without a header works where it did before")
}

// A proof that the session confirmed its password (D-53) lets RequireReauth
// through; one for another session, an API key, or none does not.
func TestRequireReauth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.AuthWithOrg(secret, fakeKeys{"paas_ada": "u-ada"}, fakeOrgs{}, nil))
	r.GET("/x", func(c *gin.Context) {
		if middleware.RequireReauth(c) {
			c.String(http.StatusOK, "through")
		}
	})
	call := func(bearer, header, query string, ws bool) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/x"+query, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		if header != "" {
			req.Header.Set(reauth.Header, header)
		}
		if ws {
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	session := jwtFor(t, "u-ada")
	proof, _, err := reauth.Issue([]byte(secret), "u-ada", session, time.Now(), reauth.TTL)
	require.NoError(t, err)

	code, body := call(session, proof, "", false)
	assert.Equal(t, http.StatusOK, code, body)

	code, body = call(session, "", "", false)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, `"code":"reauth_required"`)

	other := jwtIssuedAt(t, "u-ada", time.Now().Add(-time.Minute))
	code, body = call(other, proof, "", false)
	assert.Equal(t, http.StatusForbidden, code, "the proof of another session")
	assert.Contains(t, body, `"code":"reauth_required"`)

	code, _ = call(session, "", "?reauth="+proof, true)
	assert.Equal(t, http.StatusOK, code, "a WebSocket handshake carries it in the URL")
	code, _ = call(session, "", "?reauth="+proof, false)
	assert.Equal(t, http.StatusForbidden, code, "an ordinary request does not")

	code, body = call("paas_ada", proof, "", false)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, `"code":"session_required"`, "an API key has no password to confirm")

	code, _ = call(proof, "", "", false)
	assert.Equal(t, http.StatusUnauthorized, code, "a proof is no session token")
}

// An expired key is refused with api_key_expired and the date (K4).
func TestAuthWithOrg_ExpiredKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	r.Use(middleware.AuthWithOrg(secret, expiredKey{at}, fakeOrgs{}, nil))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer paas_old")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body struct {
		Error string `json:"error"`
		Data  struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "api_key_expired", body.Data.Code)
	assert.Equal(t, "this API key expired on 2026-10-10; create a new one in Settings → API keys", body.Error)
}

type expiredKey struct{ at time.Time }

func (k expiredKey) ValidateAPIKey(context.Context, string) (middleware.APIKeyIdentity, error) {
	return middleware.APIKeyIdentity{}, fmt.Errorf("validate: %w", &middleware.APIKeyExpiredError{At: k.at})
}

// respell flips an unused bit of the signature's last base64url character:
// a lax decoder reads the same bytes.
func respell(tok string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	i := strings.IndexByte(alphabet, tok[len(tok)-1])
	return tok[:len(tok)-1] + string(alphabet[i^1])
}
