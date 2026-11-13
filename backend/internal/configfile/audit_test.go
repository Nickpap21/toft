package configfile

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

// fileRepo holds one service's files; Upsert and Delete change nothing.
type fileRepo struct{ files []ConfigFile }

func (r *fileRepo) List(context.Context, string) ([]ConfigFile, error) { return r.files, nil }
func (r *fileRepo) Upsert(_ context.Context, serviceID string, in UpsertInput) (ConfigFile, error) {
	return ConfigFile{ID: "f2", ServiceID: serviceID, Path: in.Path, Content: in.Content}, nil
}
func (r *fileRepo) Delete(_ context.Context, id, serviceID string) (string, error) {
	for _, f := range r.files {
		if f.ID == id && f.ServiceID == serviceID {
			return f.Path, nil
		}
	}
	return "", nil
}

type ownAll struct{}

func (ownAll) CheckOwnership(context.Context, string, string) (bool, error) { return true, nil }

// recordAudit keeps each entry as "actor action resource detail".
type recordAudit struct{ entries []string }

func (a *recordAudit) WriteAuditLogDetail(_ context.Context, _, actorID, action, resource string, detail map[string]any) {
	a.entries = append(a.entries, fmt.Sprint(actorID, " ", action, " ", resource, " ", detail))
}

// Saving and deleting a config file are in the audit log by its path, never
// its content (it may hold secrets); deleting a file that isn't there is not.
func TestConfigFileChangesAreAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fileRepo{files: []ConfigFile{{ID: "f1", ServiceID: "s1", Path: "/etc/app/old.conf", Content: "token=old-s3cret"}}}
	audit := &recordAudit{}
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set("orgID", "org-a")
		c.Set("userID", "u1")
		c.Set("memberRole", "developer")
		c.Next()
	})
	NewHandler(NewService(repo, zap.NewNop()), ownAll{}, zap.NewNop()).WithAudit(audit).RegisterRoutes(g)
	do := func(method, path, body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}

	assert.Equal(t, http.StatusOK, do(http.MethodPut, "/api/v1/services/s1/config-files", `{"path":"/etc/app/app.conf","content":"password=n3w-s3cret"}`))
	assert.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/v1/services/s1/config-files/f1", ""))
	assert.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/v1/services/s1/config-files/f9", ""))
	assert.Equal(t, []string{
		"u1 service.config_file_saved service:s1 map[path:/etc/app/app.conf]",
		"u1 service.config_file_deleted service:s1 map[path:/etc/app/old.conf]",
	}, audit.entries)
	assert.NotContains(t, strings.Join(audit.entries, "\n"), "s3cret")
}
