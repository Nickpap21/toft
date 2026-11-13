package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"
)

func newRouter(t *testing.T, repo *fakeAuthRepo, gate auth.InstanceGate) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := auth.NewHandler(newSvc(t, repo, gate), zap.NewNop())
	h.RegisterRoutes(r.Group("/api/v1/auth"))
	return r
}

func do(t *testing.T, r *gin.Engine, path string, body any, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	return w, out
}

var registerBody = map[string]string{"email": "ada@example.com", "name": "Ada", "password": "password-1234"}

func withField(k, v string) map[string]string {
	m := map[string]string{}
	for key, val := range registerBody {
		m[key] = val
	}
	m[k] = v
	return m
}

func TestHandleRegister_NoInviteUsersExist_403(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true}
	r := newRouter(t, repo, &fakeGate{})

	w, body := do(t, r, "/api/v1/auth/register", registerBody, nil)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, body["error"], "invite-only")
	assert.Equal(t, "registration_closed", body["data"].(map[string]any)["code"], "the setup screen sends the visitor to sign in")
	assert.Zero(t, repo.firstUserCalls)
}

// A new password needs 12 characters (7g), counted as characters, not bytes.
func TestHandleRegister_PasswordTwelveCharacters(t *testing.T) {
	for pw, want := range map[string]int{"elevenchars": http.StatusBadRequest, "twelve-chars": http.StatusCreated, "ééééééééééé": http.StatusBadRequest} {
		repo := &fakeAuthRepo{users: newFakeUserRepo()}
		w, body := do(t, newRouter(t, repo, &fakeGate{}), "/api/v1/auth/register", withField("password", pw), nil)
		assert.Equal(t, want, w.Code, "%q: %v", pw, body)
	}
}

func TestHandleRegister_ExpiredInvite_400(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true, inviteUserErr: org.ErrInviteExpired}
	r := newRouter(t, repo, &fakeGate{})

	w, body := do(t, r, "/api/v1/auth/register", withField("invite_token", "tok"), nil)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	msg, _ := org.InviteErrorMessage(org.ErrInviteExpired)
	assert.Equal(t, msg, body["error"])
}

func TestHandleRegister_DuplicateEmail_409(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true, inviteUserErr: auth.ErrUserExists}
	r := newRouter(t, repo, &fakeGate{})

	w, _ := do(t, r, "/api/v1/auth/register", withField("invite_token", "tok"), nil)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleRegister_FirstRun_201WithProfile(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	r := newRouter(t, repo, &fakeGate{})

	w, body := do(t, r, "/api/v1/auth/register", registerBody, nil)

	require.Equal(t, http.StatusCreated, w.Code, body)
	data := body["data"].(map[string]any)
	assert.NotContains(t, data, "warning", "the invite fallback warning is gone")
	assert.NotEmpty(t, data["token"])
	u := data["user"].(map[string]any)
	assert.Equal(t, true, u["is_operator"])
	assert.Equal(t, "ada@example.com", u["email"])
	assert.NotContains(t, u, "password_hash")
}

func TestHandleRegister_SetupToken(t *testing.T) {
	t.Run("missing → 403", func(t *testing.T) {
		repo := &fakeAuthRepo{users: newFakeUserRepo()}
		r := newRouter(t, repo, &fakeGate{setupToken: "s3cret"})
		w, body := do(t, r, "/api/v1/auth/register", registerBody, nil)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, body["error"], "setup code")
		assert.Zero(t, repo.firstUserCalls)
	})
	t.Run("expired → 403 setup_token_expired", func(t *testing.T) {
		repo := &fakeAuthRepo{users: newFakeUserRepo()}
		r := newRouter(t, repo, &fakeGate{setupToken: "s3cret", expired: true})
		w, body := do(t, r, "/api/v1/auth/register", withField("setup_token", "s3cret"), nil)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Equal(t, "setup_token_expired", body["data"].(map[string]any)["code"])
		assert.Zero(t, repo.firstUserCalls)
	})
	t.Run("body field", func(t *testing.T) {
		repo := &fakeAuthRepo{users: newFakeUserRepo()}
		r := newRouter(t, repo, &fakeGate{setupToken: "s3cret"})
		w, _ := do(t, r, "/api/v1/auth/register", withField("setup_token", "s3cret"), nil)
		assert.Equal(t, http.StatusCreated, w.Code)
	})
	t.Run("header", func(t *testing.T) {
		repo := &fakeAuthRepo{users: newFakeUserRepo()}
		r := newRouter(t, repo, &fakeGate{setupToken: "s3cret"})
		w, _ := do(t, r, "/api/v1/auth/register", registerBody, map[string]string{auth.SetupTokenHeader: "s3cret"})
		assert.Equal(t, http.StatusCreated, w.Code)
	})
	t.Run("the reference's header", func(t *testing.T) {
		repo := &fakeAuthRepo{users: newFakeUserRepo()}
		r := newRouter(t, repo, &fakeGate{setupToken: "s3cret"})
		w, _ := do(t, r, "/api/v1/auth/register", registerBody, map[string]string{auth.SetupTokenHeaderToft: "s3cret"})
		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

func TestHandleLogin_DeactivatedUser_403(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, false)
	r := newRouter(t, repo, &fakeGate{})

	w, body := do(t, r, "/api/v1/auth/login", map[string]string{"email": "ada@example.com", "password": "password123"}, nil)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, body["error"], "deactivated")
}

// The 12-character minimum is for passwords set from 7g on: one set before,
// shorter, still signs in, with no reset forced.
func TestHandleLogin_ShortPasswordSetBeforeStillSignsIn(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter22"), bcrypt.MinCost)
	require.NoError(t, err)
	repo.users.add(&user.User{ID: "user-9", Email: "ada@example.com", Name: "Ada", PasswordHash: string(hash), IsActive: true})

	w, body := do(t, newRouter(t, repo, &fakeGate{}), "/api/v1/auth/login", map[string]string{"email": "ada@example.com", "password": "hunter22"}, nil)

	require.Equal(t, http.StatusOK, w.Code, body)
	assert.NotEmpty(t, body["data"].(map[string]any)["token"])
}

func TestHandleLogin_ReturnsProfileDTO(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, true)
	r := newRouter(t, repo, &fakeGate{operators: map[string]bool{"user-9": true}})

	w, body := do(t, r, "/api/v1/auth/login", map[string]string{"email": "ada@example.com", "password": "password123"}, nil)

	require.Equal(t, http.StatusOK, w.Code, body)
	u := body["data"].(map[string]any)["user"].(map[string]any)
	assert.Equal(t, true, u["is_operator"])
	assert.Equal(t, "user-9", u["id"])
	assert.NotContains(t, u, "password_hash")
}

// A fresh sign-in counts as a password confirmation for five minutes on
// the session it starts, and no other (D-161).
func TestSignInConfirmsPasswordForFiveMinutes(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	r := newRouter(t, repo, &fakeGate{})

	check := func(w *httptest.ResponseRecorder, body map[string]any) {
		t.Helper()
		data := body["data"].(map[string]any)
		session := data["token"].(string)
		userID := data["user"].(map[string]any)["id"].(string)
		proof := data["reauth"].(map[string]any)
		assert.True(t, reauth.Valid([]byte("test-secret"), proof["token"].(string), userID, session))
		assert.False(t, reauth.Valid([]byte("test-secret"), proof["token"].(string), userID, session+"x"), "another session")
		assert.InDelta(t, reauth.SignInTTL.Seconds(), proof["expires_in"], 2)
	}

	w, body := do(t, r, "/api/v1/auth/register", registerBody, nil)
	require.Equal(t, http.StatusCreated, w.Code, body)
	check(w, body)

	repo = &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, true)
	r = newRouter(t, repo, &fakeGate{})
	w, body = do(t, r, "/api/v1/auth/login", map[string]string{"email": "ada@example.com", "password": "password123"}, nil)
	require.Equal(t, http.StatusOK, w.Code, body)
	check(w, body)
}

// Sign-in is also limited per account, so a client that changes its address
// (or forges X-Forwarded-For) still gets a fixed number of guesses per email.
func TestLoginLimitedPerAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := auth.NewHandler(newSvc(t, &fakeAuthRepo{users: &fakeUserRepo{}}, nil), zap.NewNop())
	h.RegisterRoutes(r.Group("/api/v1/auth"), middleware.KeyedRateLimiter(rate.Every(time.Hour), 2, auth.LoginAccountKey))

	for i, want := range []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusTooManyRequests} {
		w, _ := do(t, r, "/api/v1/auth/login", map[string]string{"email": "Ada@example.com", "password": "wrong"}, nil)
		assert.Equal(t, want, w.Code, "attempt %d", i+1)
	}
	w, _ := do(t, r, "/api/v1/auth/login", map[string]string{"email": "bob@example.com", "password": "wrong"}, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "another account has its own limit")
}
