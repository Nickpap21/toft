package user_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Sign out other browsers through the real middleware, handler and
// database: the token it answers with keeps working, the one that asked
// and the account's others don't.
func TestSignOutOthersKeepsThisBrowser(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	const secret = "test-secret"

	var userID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, tokens_valid_from) VALUES ('dev@toft.test', 'Dev', 'x', NOW() - interval '1 day') RETURNING id::text`,
	).Scan(&userID))

	userRepo := user.NewPostgresRepository(db)
	userSvc := user.NewService(userRepo, zap.NewNop())
	authSvc := auth.NewService(userRepo, auth.NewPostgresRepository(db), nil, zap.NewNop(), secret)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", middleware.AuthWithOrg(secret, nil, nil, userSvc))
	user.NewHandler(userSvc, zap.NewNop()).WithTokenIssuer(authSvc).RegisterRoutes(g, func(c *gin.Context) { c.Next() })

	call := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	// This browser and another signed in an hour ago.
	signedIn := func(sub string) string {
		at := time.Now().Add(-time.Hour)
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": sub, "iat": at.Unix(), "exp": at.Add(30 * 24 * time.Hour).Unix(),
		}).SignedString([]byte(secret))
		require.NoError(t, err)
		return tok
	}
	this, other := signedIn(userID), signedIn(userID)
	require.Equal(t, http.StatusOK, call(http.MethodGet, "/api/v1/me", this).Code)

	w := call(http.MethodPost, "/api/v1/me/sessions/sign-out-others", this)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotEmpty(t, body.Data.Token)

	w = call(http.MethodGet, "/api/v1/me", body.Data.Token)
	require.Equal(t, http.StatusOK, w.Code, "the fresh token is this browser's session: %s", w.Body.String())
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "/api/v1/me", other).Code, "the other browser is signed out")
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "/api/v1/me", this).Code, "the token that asked is replaced")
}
