package deploypolicy_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An exception's values and a service's last deploy check (K3), against the
// real schema: fingerprints are kept, an exception from before keeps
// covering any value.
func TestPostgres_ExceptionValuesAndChecks(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := deploypolicy.NewPostgresRepository(db)

	var userID, orgID, projectID, envID, serviceID string
	q := func(sql string, dest *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dest))
	}
	q(`INSERT INTO users (email, name, password_hash) VALUES ('op@toft.test', 'Op', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'storefront') RETURNING id::text`, &projectID, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, 'agent', 'docker-image', 'agent:1') RETURNING id::text`, &serviceID, envID)

	stored, err := repo.GetStoredService(ctx, serviceID)
	require.NoError(t, err)
	assert.Equal(t, "agent:1", stored.DockerImage)

	value := deploypolicy.PinnedValue{Fingerprint: "f1", Rule: composespec.RuleHostBindMount, Field: "volumes[0]", Value: "/srv/a → /data", Image: "agent:1", Message: "bind mount"}
	_, err = repo.GrantException(ctx, deploypolicy.Grant{ServiceID: serviceID, AllowedRules: []deploypolicy.Rule{composespec.RuleHostBindMount},
		AllowedValues: []deploypolicy.PinnedValue{value}, Reason: "data", ActorID: userID, Version: deploypolicy.VersionOf(nil)})
	require.NoError(t, err)
	e, err := repo.GetException(ctx, serviceID)
	require.NoError(t, err)
	assert.False(t, e.AnyValue)
	assert.Equal(t, []deploypolicy.PinnedValue{value}, e.AllowedValues)
	all, err := repo.ListExceptions(ctx)
	require.NoError(t, err)
	assert.Equal(t, []deploypolicy.PinnedValue{value}, all[serviceID].AllowedValues)

	var audit string
	q(`SELECT metadata::text FROM audit_log WHERE action = $1`, &audit, deploypolicy.ActionExceptionGranted)
	assert.Contains(t, audit, `"allowed_values"`)
	assert.NotContains(t, audit, "f1", "fingerprints stay out of the audit log")

	// A deploy rewrites a plain pin to its keyed fingerprint (D-126): the
	// matched one only, in place, the rest of the exception as it was.
	second := value
	second.Fingerprint, second.Value = "f2", "/srv/b → /data"
	_, err = repo.GrantException(ctx, deploypolicy.Grant{ServiceID: serviceID, AllowedRules: []deploypolicy.Rule{composespec.RuleHostBindMount},
		AllowedValues: []deploypolicy.PinnedValue{value, second}, Reason: "data", ActorID: userID, Version: deploypolicy.VersionOf(e)})
	require.NoError(t, err)
	before, err := repo.GetException(ctx, serviceID)
	require.NoError(t, err)
	require.NoError(t, repo.RepinValues(ctx, serviceID, []string{"f1", "gone"}, []string{"k1", "k-gone"}))
	e, err = repo.GetException(ctx, serviceID)
	require.NoError(t, err)
	keyed := value
	keyed.Fingerprint = "k1"
	assert.Equal(t, []deploypolicy.PinnedValue{keyed, second}, e.AllowedValues)
	assert.Equal(t, before.UpdatedAt, e.UpdatedAt)
	assert.Equal(t, before.Reason, e.Reason)
	require.NoError(t, repo.RepinValues(ctx, serviceID, []string{"f1"}, []string{"other"}), "a pin rewritten already stays")
	e, err = repo.GetException(ctx, serviceID)
	require.NoError(t, err)
	assert.Equal(t, "k1", e.AllowedValues[0].Fingerprint)

	// Granted before v0.25.0: no values, any value.
	_, err = db.Exec(ctx, `UPDATE service_deploy_policy_exceptions SET allowed_values = NULL`)
	require.NoError(t, err)
	e, err = repo.GetException(ctx, serviceID)
	require.NoError(t, err)
	assert.True(t, e.AnyValue)
	assert.Nil(t, e.AllowedValues)
	require.NoError(t, repo.RepinValues(ctx, serviceID, []string{"k1"}, []string{"k2"}))
	e, err = repo.GetException(ctx, serviceID)
	require.NoError(t, err)
	assert.True(t, e.AnyValue, "an exception for any value stays one")

	_, err = repo.GetLastCheck(ctx, serviceID)
	assert.ErrorIs(t, err, deploypolicy.ErrNotFound)
	require.NoError(t, repo.SaveLastCheck(ctx, serviceID, "abc", []deploypolicy.PinnedValue{value}))
	require.NoError(t, repo.SaveLastCheck(ctx, serviceID, "def", nil))
	c, err := repo.GetLastCheck(ctx, serviceID)
	require.NoError(t, err)
	assert.Equal(t, "def", c.CommitSHA)
	assert.Equal(t, []deploypolicy.PinnedValue{}, c.Values)
	require.NoError(t, repo.SaveLastCheck(ctx, serviceID, "ghi", []deploypolicy.PinnedValue{value}))
	checks, err := repo.ListLastChecks(ctx)
	require.NoError(t, err)
	require.Contains(t, checks, serviceID)
	assert.Equal(t, []deploypolicy.PinnedValue{value}, checks[serviceID].Values)
}
