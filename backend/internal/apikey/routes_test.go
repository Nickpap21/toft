package apikey

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/doc"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// Every route a scoped key may call is a real route: a typo would not open
// anything (unlisted routes are refused) but would silently break CI.
func TestScopedRoutesExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	log := zap.NewNop()
	deployment.NewHandler(nil, nil, log).RegisterRoutes(v1)
	service.NewHandler(nil, nil, nil, nil, nil, log).RegisterRoutes(v1)
	domain.NewHandler(nil, nil, nil, log).RegisterRoutes(v1)
	envvar.NewHandler(envvar.NewService(nil, log), nil, nil, nil, log).RegisterRoutes(v1)
	code.NewHandler(nil, nil, nil, log).RegisterRoutes(v1)
	issue.NewHandler(nil, nil, log).RegisterRoutes(v1)
	doc.NewHandler(nil, nil, nil, log).RegisterRoutes(v1)
	preview.NewHandler(nil, nil, nil, log).RegisterRoutes(v1)

	real := map[string]bool{}
	for _, rt := range r.Routes() {
		real[rt.Method+" "+rt.Path] = true
	}
	for route := range routeScopes {
		assert.True(t, real[route], "%s is not a route", route)
	}
}
