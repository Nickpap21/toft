package deployment_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A deployment's settings fingerprint (0072, D-159) is none until it is
// recorded, then reads back; the column takes only a SHA-256 in hex.
func TestSettingsFingerprintRoundTrip(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name) VALUES ('org', $1, 'web') RETURNING id::text`, orgID).Scan(&serviceID))
	repo := deployment.NewPostgresRepository(db)
	d, err := repo.Create(ctx, serviceID, "Manual · Ann", "", 0)
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Empty(t, got.SettingsFingerprint)

	fp := strings.Repeat("ab", 32)
	require.NoError(t, repo.SetSettingsFingerprint(ctx, d.ID, fp))
	got, err = repo.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, fp, got.SettingsFingerprint)
	assert.Error(t, repo.SetSettingsFingerprint(ctx, d.ID, "not-a-hash"), "the CHECK holds")
}

// Recovery's newest deployment of a service doesn't count one that failed
// before it started: a deploy interrupted while it waited for the one ahead
// of it doesn't hide the one that was running.
func TestLatestDeploymentSkipsOnesThatNeverStarted(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	var orgID, serviceID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (scope, scoped_org_id, name) VALUES ('org', $1, 'web') RETURNING id::text`, orgID).Scan(&serviceID))
	repo := deployment.NewPostgresRepository(db)
	ran, err := repo.Create(ctx, serviceID, "Manual · Ann", "", 0)
	require.NoError(t, err)
	_, err = repo.UpdateStatus(ctx, ran.ID, deployment.StatusBuilding, "", "", "")
	require.NoError(t, err)
	waited, err := repo.Create(ctx, serviceID, "Manual · Ann", "", 0)
	require.NoError(t, err)

	latest, err := repo.LatestIDForService(ctx, serviceID)
	require.NoError(t, err)
	assert.Equal(t, waited.ID, latest, "a pending deploy is the newest")

	const marker = "interrupted"
	marked, err := repo.MarkInterrupted(ctx, marker, nil)
	require.NoError(t, err)
	require.Len(t, marked, 2)
	latest, err = repo.LatestIDForService(ctx, serviceID)
	require.NoError(t, err)
	assert.Equal(t, ran.ID, latest, "once it failed without starting, the one that ran is")
	unchecked, err := repo.ListUnreconciledInterrupted(ctx, marker)
	require.NoError(t, err)
	require.Len(t, unchecked, 1)
	assert.Equal(t, ran.ID, unchecked[0].ID)

	// A deploy that waited reads its deployment before it runs: gone with
	// its service is told from a read that failed.
	_, err = db.Exec(ctx, `DELETE FROM services WHERE id = $1`, serviceID)
	require.NoError(t, err)
	_, err = repo.GetByID(ctx, waited.ID)
	assert.ErrorIs(t, err, deployment.ErrDeploymentNotFound)
}
