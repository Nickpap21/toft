package sharedvolume

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// recordAudit keeps each entry as "actor action resource detail", the
// detail as the audit log stores it (JSON).
type recordAudit struct{ entries []string }

func (a *recordAudit) WriteAuditLogDetail(_ context.Context, _, actorID, action, resource string, detail map[string]any) {
	raw, _ := json.Marshal(detail)
	a.entries = append(a.entries, fmt.Sprint(actorID, " ", action, " ", resource, " ", string(raw)))
}

// Changing a service's shared mounts is in the audit log with the mounts it
// has now; a save that changes nothing is not.
func TestMountChangesAreAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, _ := newSvc()
	_, err := s.Create(context.Background(), "org-a", CreateInput{Name: "images", ServerID: "srv-1"})
	require.NoError(t, err)
	audit := &recordAudit{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-a")
		c.Set("userID", "u1")
		c.Set("memberRole", "developer")
		c.Next()
	})
	NewHandler(s, zap.NewNop()).WithAudit(audit).RegisterRoutes(g)
	put := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/services/svc-a/shared-volumes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	mount := `{"version":"v","mounts":[{"shared_volume_id":"vol-images","target":"/data","read_only":true}]}`
	put(mount)
	put(mount)
	put(`{"version":"v","mounts":[]}`)
	assert.Equal(t, []string{
		`u1 service.shared_volumes_changed service:svc-a {"mounts":[{"shared_volume_id":"vol-images","name":"images","target":"/data","read_only":true}]}`,
		`u1 service.shared_volumes_changed service:svc-a {"mounts":[]}`,
	}, audit.entries)
}
