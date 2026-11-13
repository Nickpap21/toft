package user_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type fakeIssuer struct{}

func (fakeIssuer) IssueToken(userID string) (string, error) { return "fresh-" + userID, nil }

func TestSignOutOthers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeRepo{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("userID", "u-1"); c.Next() })
	user.NewHandler(user.NewService(repo, zap.NewNop()), zap.NewNop()).WithTokenIssuer(fakeIssuer{}).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/me/sessions/sign-out-others", nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"u-1"}, repo.revoked)
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "fresh-u-1", body.Data.Token, "this browser keeps a session")
}

func TestSignOut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeRepo{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("userID", "u-1")
		c.Set("session", middleware.Session{Hash: "h-1", Expires: time.Now().Add(time.Hour)})
		c.Next()
	})
	user.NewHandler(user.NewService(repo, zap.NewNop()), zap.NewNop()).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/me/sessions/sign-out", nil))
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, []string{"h-1"}, repo.signedOut)
}

// An API key never gets a session token back: it would outlive the key and
// pass every session_required check.
func TestSessionRoutesRefuseAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeRepo{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("userID", "u-1"); c.Set("apiKey", true); c.Next() })
	user.NewHandler(user.NewService(repo, zap.NewNop()), zap.NewNop()).WithTokenIssuer(fakeIssuer{}).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/me/sessions/sign-out-others", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/me/sessions/sign-out", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/me/password", strings.NewReader(`{"current_password":"old-password","new_password":"new-password"}`)),
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, req.URL.Path)
		assert.Contains(t, w.Body.String(), "session_required", req.URL.Path)
		assert.NotContains(t, w.Body.String(), "fresh-", req.URL.Path)
	}
	assert.Empty(t, repo.revoked)
}

// A new password needs 12 characters (7g); the current one, set before, may be
// shorter and still proves who is asking.
func TestPasswordChangeNeedsTwelveCharacters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter22"), bcrypt.MinCost)
	require.NoError(t, err)
	repo := &fakeRepo{users: map[string]*user.User{"u-1": {ID: "u-1", PasswordHash: string(hash), IsActive: true}}}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("userID", "u-1"); c.Next() })
	user.NewHandler(user.NewService(repo, zap.NewNop()), zap.NewNop()).WithTokenIssuer(fakeIssuer{}).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	for next, want := range map[string]int{"elevenchars": http.StatusBadRequest, "twelve-chars": http.StatusOK} {
		body, _ := json.Marshal(map[string]string{"current_password": "hunter22", "new_password": next})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/me/password", strings.NewReader(string(body))))
		assert.Equal(t, want, w.Code, "%q: %s", next, w.Body.String())
	}
}

// bcrypt reads 72 bytes of a password and refuses more: a longer one is the
// caller's mistake, said so, not a 500. 40 accented letters are 80 bytes but
// pass the binding's character count.
func TestPasswordOverSeventyTwoBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	require.NoError(t, err)
	repo := &fakeRepo{users: map[string]*user.User{"u-1": {ID: "u-1", PasswordHash: string(hash), IsActive: true}}}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("userID", "u-1"); c.Next() })
	user.NewHandler(user.NewService(repo, zap.NewNop()), zap.NewNop()).WithTokenIssuer(fakeIssuer{}).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	put := func(next string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"current_password": "old-password", "new_password": next})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/me/password", strings.NewReader(string(body))))
		var out any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		apispec.Check(t, http.MethodPut, "/api/v1/me/password", w.Code, out)
		return w
	}
	w := put(strings.Repeat("é", 40))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"password_too_long"`)
	assert.Contains(t, w.Body.String(), `"field":"new_password"`)

	w = put(strings.Repeat("a", 300))
	assert.Contains(t, w.Body.String(), `"code":"password_too_long"`, "past the old 256-character tag too")

	w = put(strings.Repeat("é", 36))
	assert.Equal(t, http.StatusOK, w.Code, "72 bytes exactly: %s", w.Body.String())
}

func TestSetTheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeRepo{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("userID", "u-1"); c.Next() })
	user.NewHandler(user.NewService(repo, zap.NewNop()), zap.NewNop()).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	put := func(body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/me/theme", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}
	assert.Equal(t, http.StatusNoContent, put(`{"theme":"system:mono-daylight"}`))
	assert.Equal(t, http.StatusBadRequest, put(`{"theme":"neon"}`))
	assert.Equal(t, http.StatusBadRequest, put(`{}`))
	assert.Equal(t, []string{"system:mono-daylight"}, repo.themes)
}
