package org_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func ptr(s string) *string { return &s }

func TestTargetOf(t *testing.T) {
	svc := org.TargetOf("deployment.triggered", org.AuditRow{
		Kind: "service", Target: "s-1", ServiceID: ptr("s-1"), ServiceName: ptr("web"),
		ServiceProjectID: ptr("p-1"), ServiceProjectName: ptr("storefront"), ServiceEnvironment: ptr("production"),
	})
	assert.Equal(t, org.AuditTarget{Kind: "service", ID: "s-1", Name: "storefront / web", ServiceID: "s-1", ProjectID: "p-1", Environment: "production"}, svc)

	gone := org.TargetOf("service.deleted", org.AuditRow{Kind: "service", Target: "s-2", Detail: map[string]any{"name": "cron-legacy"}})
	assert.True(t, gone.Deleted)
	assert.Equal(t, "cron-legacy", gone.Name, "the name the entry recorded")

	older := org.TargetOf("service.deleted", org.AuditRow{Kind: "service", Target: "s-3"})
	assert.Equal(t, "s-3", older.Name, "an older entry with no recorded name shows the ID")

	share := org.TargetOf("server.shared", org.AuditRow{Kind: "server", Target: "srv", ServerName: ptr("fsn1"), OtherOrgName: ptr("acme")})
	assert.Equal(t, "fsn1", share.Name)
	assert.Equal(t, "acme", share.OtherWorkspace)

	assert.Equal(t, "Alert webhook", org.TargetOf("alerts.channel_updated", org.AuditRow{Kind: "org", Target: "o"}).Name)
	assert.Equal(t, "the workspace", org.TargetOf("org.renamed", org.AuditRow{Kind: "org", Target: "o"}).Name)
	assert.Equal(t, "Panel host", org.TargetOf("instance.backup_set", org.AuditRow{Kind: "instance", Target: "panel-host"}).Name)
	env := org.TargetOf("variables.saved", org.AuditRow{Kind: "environment", Target: "e", EnvironmentName: ptr("staging"),
		EnvironmentProjectName: ptr("storefront"), EnvironmentProjectID: ptr("p-1")})
	assert.Equal(t, "storefront / staging", env.Name)
	assert.Equal(t, "p-1", env.ProjectID)
}

func TestAccessEventsAreLogged(t *testing.T) {
	repo := &fakeRepository{}
	r := newOrgRouter(org.NewService(repo, zap.NewNop()), "u-1", "org-1", "owner")
	code, _ := send(t, r, http.MethodPut, "/api/v1/org/members/u-2/role", map[string]string{"role": "owner"})
	require.Equal(t, http.StatusNoContent, code)
	code, _ = send(t, r, http.MethodDelete, "/api/v1/org/members/u-3", nil)
	require.Equal(t, http.StatusNoContent, code)
	assert.Equal(t, []string{
		`member.role_changed user:u-2 {"from":"developer","to":"owner"}`,
		`member.removed user:u-3 {"role":"developer"}`,
	}, repo.audit)
}

func TestAuditLogFilters(t *testing.T) {
	repo := &fakeRepository{}
	r := newOrgRouter(org.NewService(repo, zap.NewNop()), "u-1", "org-1", "viewer")
	code, _ := send(t, r, http.MethodGet,
		"/api/v1/org/audit-log?actions=deployment.triggered,deployment.rollback&actor=u-9&since=2026-10-08T00:00:00Z&before=2026-10-15T09:00:00.123Z&before_id=e-1&limit=50", nil)
	require.Equal(t, http.StatusOK, code)
	f := repo.auditFilters[0]
	assert.Equal(t, []string{"deployment.triggered", "deployment.rollback"}, f.Actions)
	assert.Equal(t, "u-9", f.ActorID)
	assert.Equal(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), *f.Since)
	assert.Equal(t, "e-1", f.BeforeID)
	assert.Equal(t, 50, f.Limit)

	code, body := send(t, r, http.MethodGet, "/api/v1/org/audit-log/count?since=2026-10-08T00:00:00Z", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"total": float64(7)}, body["data"])
	assert.Nil(t, repo.auditFilters[1].Before, "the count ignores the cursor")

	code, _ = send(t, r, http.MethodGet, "/api/v1/org/audit-log?since=yesterday", nil)
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestAuditLogAPIKeyParam(t *testing.T) {
	for _, path := range []string{"/api/v1/org/audit-log", "/api/v1/org/audit-log/count"} {
		repo := &fakeRepository{}
		r := newOrgRouter(org.NewService(repo, zap.NewNop()), "u-1", "org-1", "viewer")
		for _, key := range []string{"any", "none"} {
			code, _ := send(t, r, http.MethodGet, path+"?api_key="+key, nil)
			require.Equal(t, http.StatusOK, code, path)
			assert.Equal(t, key, repo.auditFilters[len(repo.auditFilters)-1].APIKey, path)
		}
		code, _ := send(t, r, http.MethodGet, path+"?api_key=some", nil)
		assert.Equal(t, http.StatusBadRequest, code, path)
		assert.Len(t, repo.auditFilters, 2, "a bad api_key reaches no query")
	}
}

// A repository's and a docs page's entries name them with their project
// (D-104): an agent's push, a runbook step.
func TestTargetOfRepositoryAndPage(t *testing.T) {
	assert.Equal(t, "Grafana", org.TargetOf("observability.grafana_changed", org.AuditRow{Kind: "observability", Target: "grafana"}).Name)
	repo := org.TargetOf("agent.pushed", org.AuditRow{Kind: "repository", Target: "r1", RepositoryName: ptr("shop"),
		RepositoryProjectID: ptr("p-1"), RepositoryProjectName: ptr("storefront")})
	assert.Equal(t, org.AuditTarget{Kind: "repository", ID: "r1", Name: "storefront / shop", ProjectID: "p-1"}, repo)
	page := org.TargetOf("doc.step_run", org.AuditRow{Kind: "doc_page", Target: "pg", DocPageTitle: ptr("Clear a stuck queue"),
		DocPageProjectID: ptr("p-1"), DocPageProjectName: ptr("storefront")})
	assert.Equal(t, "storefront / Clear a stuck queue", page.Name)
	assert.True(t, org.TargetOf("doc.step_run", org.AuditRow{Kind: "doc_page", Target: "pg"}).Deleted)
}

// An SSH key's entries name it by the title they recorded, never its ID.
func TestTargetOfSSHKey(t *testing.T) {
	key := org.TargetOf("ssh_key.added", org.AuditRow{Kind: "ssh_key", Target: "k-1", Detail: map[string]any{"title": "laptop"}})
	assert.Equal(t, org.AuditTarget{Kind: "ssh_key", ID: "k-1", Name: "laptop"}, key)
	assert.Equal(t, "an SSH key", org.TargetOf("ssh_key.removed", org.AuditRow{Kind: "ssh_key", Target: "k-1"}).Name)
}
