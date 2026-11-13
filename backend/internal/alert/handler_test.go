package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type nopAudit struct{}

func (nopAudit) WriteAuditLog(_ context.Context, _, _, _, _ string)                         {}
func (nopAudit) WriteAuditLogDetail(_ context.Context, _, _, _, _ string, _ map[string]any) {}

func TestGetChannelWithoutAWebhookIsNull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) { c.Set("orgID", "org-9"); c.Set("memberRole", "owner"); c.Next() })
	NewHandler(NewService(newFakeRepo(), crypto.NewKeys(testKey, ""), zap.NewNop()), nopAudit{}, zap.NewNop()).RegisterRoutes(g)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/alerts/channel", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"data":null}`, w.Body.String())
}
