package configfile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// denyAll counts ownership checks and owns nothing.
type denyAll struct{ checks int }

func (d *denyAll) CheckOwnership(context.Context, string, string) (bool, error) {
	d.checks++
	return false, nil
}

func TestViewersCannotChangeConfigFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := []struct{ method, path string }{
		{http.MethodPut, "/api/v1/services/s1/config-files"},
		{http.MethodDelete, "/api/v1/services/s1/config-files/f1"},
	}
	for _, role := range []string{"viewer", "developer"} {
		for _, rt := range routes {
			owner := &denyAll{}
			r := gin.New()
			g := r.Group("/api/v1")
			g.Use(func(c *gin.Context) { c.Set("orgID", "org-1"); c.Set("memberRole", role); c.Next() })
			NewHandler(nil, owner, zap.NewNop()).RegisterRoutes(g)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`)))
			want := http.StatusNotFound // another workspace's, as an unknown one
			if role == "viewer" {
				want = http.StatusForbidden
			}
			assert.Equal(t, want, w.Code, "%s %s %s", role, rt.method, rt.path)
			if role == "viewer" {
				assert.Zero(t, owner.checks, "a viewer is refused before anything is looked up: %s %s", rt.method, rt.path)
			} else {
				assert.Equal(t, 1, owner.checks, "a developer gets past the role check: %s %s", rt.method, rt.path)
			}
		}
	}
}
