package disk_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Nickpap21/toft/backend/internal/disk"
	"github.com/Nickpap21/toft/backend/internal/testdb"
)

func TestPostgresRepository(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := disk.NewPostgresRepository(db)
	q := func(sql string, dest any, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dest))
	}
	exec := func(sql string, args ...any) error {
		_, err := db.Exec(ctx, sql, args...)
		return err
	}
	var userID, orgID, serverID, projectID, envID, onServer, onPanel string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('o@toft.test', 'O', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO servers (org_id, user_id, name, ip_address, ssh_key) VALUES ($1, $2, 'web', '203.0.113.10', 'k') RETURNING id::text`, &serverID, orgID, userID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, &projectID, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image, server_id) VALUES ($1, 'api', 'docker-image', 'a:1', $2) RETURNING id::text`, &onServer, envID, serverID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'panelapp', 'docker-image', 'b:1') RETURNING id::text`, &onPanel, envID)

	// Policies: the default, then set, then back to the default.
	h, err := repo.Server(ctx, serverID)
	require.NoError(t, err)
	require.Equal(t, orgID, h.OrgID)
	require.Nil(t, h.Policy.BuildCacheLimitBytes)
	require.Nil(t, h.Policy.AutoClean)
	require.Nil(t, h.Policy.AlertPercent)
	limit, alert := int64(2_000_000_000), 90
	require.NoError(t, repo.SetPolicy(ctx, serverID, userID, disk.Policy{BuildCacheLimitBytes: &limit, AutoClean: []disk.Kind{}, AlertPercent: &alert}))
	h, err = repo.Server(ctx, serverID)
	require.NoError(t, err)
	require.Equal(t, limit, *h.Policy.BuildCacheLimitBytes)
	require.NotNil(t, h.Policy.AutoClean)
	require.Empty(t, h.Policy.AutoClean, "empty is off, not the default")
	require.Equal(t, 90, *h.Policy.AlertPercent)
	require.NoError(t, repo.SetPolicy(ctx, serverID, userID, disk.Policy{}))
	h, err = repo.Server(ctx, serverID)
	require.NoError(t, err)
	require.Nil(t, h.Policy.AutoClean)
	require.Nil(t, h.Policy.AlertPercent)

	// The database refuses what the API would.
	for _, bad := range []string{
		`UPDATE servers SET disk_alert_percent = 40 WHERE id = $1`,
		`UPDATE servers SET build_cache_limit_bytes = 5 WHERE id = $1`,
		`UPDATE servers SET disk_auto_clean = ARRAY['old_panel_releases'] WHERE id = $1`,
		`UPDATE servers SET disk_last_clean = '[]' WHERE id = $1`,
	} {
		require.Error(t, exec(bad, serverID), bad)
	}
	require.Error(t, exec(`UPDATE services SET rollback_images_kept = 0 WHERE id = $1`, onServer))
	require.Error(t, exec(`UPDATE services SET rollback_images_kept = 101 WHERE id = $1`, onServer))

	// The panel's own policy and last clean, in instance_settings.
	require.NoError(t, repo.SetPolicy(ctx, "", userID, disk.Policy{AutoClean: []disk.Kind{disk.OldPanelReleases}, AlertPercent: &alert}))
	p, err := repo.Server(ctx, "")
	require.NoError(t, err)
	require.Equal(t, []disk.Kind{disk.OldPanelReleases}, p.Policy.AutoClean)
	require.Equal(t, 90, *p.Policy.AlertPercent)
	require.NoError(t, repo.SetPolicy(ctx, "", userID, disk.Policy{}))
	p, err = repo.Server(ctx, "")
	require.NoError(t, err)
	require.Nil(t, p.Policy.AlertPercent, "null goes back to the default")
	require.NoError(t, repo.SetLastClean(ctx, "", disk.Clean{ID: "c1", Outcome: disk.Succeeded, FreedBytes: 9}))
	require.NoError(t, repo.SetLastClean(ctx, serverID, disk.Clean{ID: "c2", Outcome: disk.Failed, Error: "x"}))
	p, _ = repo.Server(ctx, "")
	h, _ = repo.Server(ctx, serverID)
	require.Equal(t, int64(9), p.LastClean.FreedBytes)
	require.Equal(t, "x", h.LastClean.Error)

	// Services per host, and the deployments that went live, ranked.
	svcs, err := repo.HostServices(ctx, serverID)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, onServer, svcs[0].ID)
	require.Equal(t, orgID, svcs[0].OrgID, "the project's workspace")
	svcs, err = repo.HostServices(ctx, "")
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, onPanel, svcs[0].ID)
	prefixes, err := repo.ServicePrefixes(ctx)
	require.NoError(t, err)
	require.True(t, prefixes[onServer[:8]] && prefixes[onPanel[:8]])

	var d1, d2, failed string
	q(`INSERT INTO deployments (service_id, status, image_tag, created_at) VALUES ($1, 'running', 'a:1', NOW() - interval '2 hours') RETURNING id::text`, &d1, onServer)
	q(`INSERT INTO deployments (service_id, status, image_tag, created_at) VALUES ($1, 'running', 'a:2', NOW() - interval '1 hour') RETURNING id::text`, &d2, onServer)
	q(`INSERT INTO deployments (service_id, status, image_tag) VALUES ($1, 'failed', 'a:3') RETURNING id::text`, &failed, onServer)
	deps, err := repo.LiveDeployments(ctx, []string{onServer})
	require.NoError(t, err)
	require.Len(t, deps, 2, "a failed deploy never went live")
	require.Equal(t, d2, deps[0].ID)
	require.Equal(t, 1, deps[0].Rank)
	require.Equal(t, 2, deps[1].Rank)
	require.NoError(t, repo.MarkImagesRemoved(ctx, []string{d1}))
	deps, err = repo.LiveDeployments(ctx, []string{onServer})
	require.NoError(t, err)
	require.True(t, deps[1].Removed)
	require.False(t, deps[0].Removed)

	// An automatic clean writes the workspace's log with no person.
	require.NoError(t, exec(`INSERT INTO audit_log (org_id, actor_id, action) VALUES ($1, NULL, 'server.disk_cleaned')`, orgID))
}
