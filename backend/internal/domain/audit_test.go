package domain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// recordAudit keeps each entry as "actor action resource detail".
type recordAudit struct{ entries []string }

func (a *recordAudit) WriteAuditLogDetail(_ context.Context, _, actorID, action, resource string, detail map[string]any) {
	a.entries = append(a.entries, fmt.Sprint(actorID, " ", action, " ", resource, " ", detail))
}

// Adding and removing a service's domain are in the audit log, by hostname;
// a removal that removes nothing is not.
func TestDomainChangesAreAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &memRepo{domains: []Domain{{ID: "d1", ServiceID: "s1", Hostname: "shop.example.com"}}}
	audit := &recordAudit{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-a")
		c.Set("userID", "u1")
		c.Set("memberRole", "developer")
		c.Next()
	})
	NewHandler(NewService(repo, routing{port: 80}, zap.NewNop()), nil, routing{}, zap.NewNop()).WithAudit(audit).RegisterRoutes(g)
	do := func(method, path, body string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code
	}

	assert.Equal(t, http.StatusCreated, do(http.MethodPost, "/api/v1/services/s1/domains", `{"hostname":"App.Example.com"}`))
	assert.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/v1/services/s1/domains/d1", ""))
	assert.Equal(t, http.StatusNotFound, do(http.MethodDelete, "/api/v1/services/s1/domains/d9", ""))
	assert.Equal(t, []string{
		"u1 service.domain_added service:s1 map[hostname:app.example.com]",
		"u1 service.domain_removed service:s1 map[hostname:shop.example.com]",
	}, audit.entries)
}
