package code

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type expiredKeys struct{}

func (expiredKeys) ValidateAPIKey(context.Context, string) (middleware.APIKeyIdentity, error) {
	return middleware.APIKeyIdentity{}, &middleware.APIKeyExpiredError{At: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
}

// git over HTTP says a key expired, and when, as the API does (K4).
func TestGitHTTPSaysAKeyExpired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := NewService(nil, &gitstore.Client{}, nil, func() string { return "" }, 0, zap.NewNop())
	NewGitHTTP(svc, expiredKeys{}, zap.NewNop()).RegisterRoutes(r, func(c *gin.Context) { c.Next() })
	req := httptest.NewRequest(http.MethodGet, "/acme/app.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("ci", "paas_old")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "this API key expired on 2026-10-10")
	// git shows the reason to its user only in text/plain.
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")
}
