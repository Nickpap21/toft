package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	orgPkg "github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/service"
)

// dependencyCheck is guestService whose CheckDependencies a test sets.
type dependencyCheck struct {
	guestService
	err error
}

func (d dependencyCheck) CheckDependencies(context.Context, *service.Service) error { return d.err }

// A dependency that isn't running refuses the deploy with its name (409).
// The lookup failing is ours: the client gets a 500 that says only that,
// the same in a create's deploy_error, and the database's text goes to the
// log.
func TestTrigger_DependencyCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const dbText = `ERROR: relation "services" does not exist (SQLSTATE 42P01)`
	deploy := func(t *testing.T, checkErr error) (*httptest.ResponseRecorder, *service.DeployRefused, *observer.ObservedLogs) {
		t.Helper()
		core, logs := observer.New(zap.ErrorLevel)
		audit := &auditLog{}
		sm := dependencyCheck{err: checkErr}
		s := NewService(nil, nil, nil, zap.New(core))
		s.WithDeployUseCase(sm, nil, nil, nil, nil, nil, audit)
		h := NewHandler(s, sm, zap.NewNop())
		r := gin.New()
		g := r.Group("/api/v1")
		g.Use(func(c *gin.Context) {
			c.Set("orgID", "guest")
			c.Set("userID", "bob")
			c.Set("memberRole", orgPkg.RoleOwner)
			c.Next()
		})
		h.RegisterRoutes(g)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/svc-1/deployments", strings.NewReader(`{}`)))

		_, err := h.DeployCreated(context.Background(), service.DeployCaller{OrgID: "guest", UserID: "bob", Role: orgPkg.RoleOwner}, "svc-1", false)
		var refused *service.DeployRefused
		require.ErrorAs(t, err, &refused)
		assert.Empty(t, audit.actions, "nothing was deployed")
		return w, refused, logs
	}

	t.Run("a dependency is not running", func(t *testing.T) {
		const msg = "dependencies not ready: redis (stopped)"
		w, refused, logs := deploy(t, fmt.Errorf("%w: redis (stopped)", service.ErrDependenciesNotReady))
		assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), msg)
		assert.Equal(t, msg, refused.Message)
		assert.Zero(t, logs.Len(), "a refusal is not an error of ours")
	})

	t.Run("the lookup fails", func(t *testing.T) {
		const msg = "failed to check the service's dependencies"
		w, refused, logs := deploy(t, fmt.Errorf("check dependencies: %w", errors.New(dbText)))
		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, msg, body["error"])
		assert.NotContains(t, w.Body.String(), "SQLSTATE")
		assert.NotContains(t, w.Body.String(), "relation")
		apispec.Check(t, http.MethodPost, "/api/v1/services/svc-1/deployments", w.Code, body)

		assert.Equal(t, msg, refused.Message, "a create's deploy_error")
		assert.Empty(t, refused.Code)

		require.Equal(t, 2, logs.Len(), "once for the route, once for the create")
		for _, e := range logs.All() {
			assert.Contains(t, fmt.Sprint(e.ContextMap()["error"]), dbText, "the detail is in the log")
			assert.Equal(t, "svc-1", e.ContextMap()["service_id"])
		}
	})
}
