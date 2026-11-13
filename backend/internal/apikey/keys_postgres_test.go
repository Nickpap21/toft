package apikey_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Keys that expire (K4) and keys limited to some services of their project
// (K5), against the real schema.
func TestKeyExpiryAndServices(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	svc := apikey.NewService(apikey.NewPostgresRepository(db), zap.NewNop())

	var userID, orgID, p1, p2, env, web, shared, other string
	q := func(sql string, dest *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dest))
	}
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'storefront') RETURNING id::text`, &p1, userID, orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'billing') RETURNING id::text`, &p2, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &env, p1)
	q(`INSERT INTO services (environment_id, name) VALUES ($1, 'web') RETURNING id::text`, &web, env)
	q(`INSERT INTO services (scope, project_id, name) VALUES ('project', $1, 'cache') RETURNING id::text`, &shared, p1)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &other, p2)
	q(`INSERT INTO services (environment_id, name) VALUES ($1, 'api') RETURNING id::text`, &other, other)

	create := func(name string, in apikey.CreateInput) (*apikey.CreatedResponse, error) {
		in.Name = name
		return svc.Create(ctx, userID, in)
	}
	deploy := []string{apikey.ScopeDeploy}

	// Expiry: left out is 90 days, null is never, the past is refused.
	k, err := create("default", apikey.CreateInput{})
	require.NoError(t, err)
	require.NotNil(t, k.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(apikey.DefaultExpiry), *k.ExpiresAt, time.Minute)
	k, err = create("never", apikey.CreateInput{ExpiresAt: apikey.Expiry{Set: true}})
	require.NoError(t, err)
	assert.Nil(t, k.ExpiresAt)
	past := time.Now().Add(-time.Minute)
	_, err = create("past", apikey.CreateInput{ExpiresAt: apikey.Expiry{Set: true, Time: &past}})
	require.ErrorIs(t, err, apikey.ErrInvalidExpiry)
	soon := time.Now().Add(30 * time.Second)
	_, err = create("soon", apikey.CreateInput{ExpiresAt: apikey.Expiry{Set: true, Time: &soon}})
	require.ErrorIs(t, err, apikey.ErrInvalidExpiry, "within a minute of now")

	// A key past its expiry is refused, saying when it expired.
	var expiring string
	k, err = create("expiring", apikey.CreateInput{})
	require.NoError(t, err)
	expiring = k.Key
	_, err = svc.ValidateAPIKey(ctx, expiring)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE api_keys SET created_at = '2026-01-01', expires_at = '2026-01-02' WHERE id = $1`, k.ID)
	require.NoError(t, err)
	_, err = svc.ValidateAPIKey(ctx, expiring)
	var expired *middleware.APIKeyExpiredError
	require.True(t, errors.As(err, &expired), "%v", err)
	assert.Contains(t, err.Error(), "expired on 2026-01-02")

	// Services: each a service of the key's project, once, with deploy or
	// variables only.
	k, err = create("web-ci", apikey.CreateInput{ProjectID: p1, Scopes: deploy, ServiceIDs: []string{web, shared}})
	require.NoError(t, err)
	assert.Equal(t, []string{web, shared}, k.ServiceIDs)
	require.Len(t, k.Services, 2)
	assert.ElementsMatch(t, []apikey.KeyService{
		{ID: web, Name: "web", EnvironmentName: "production", ProjectID: p1},
		{ID: shared, Name: "cache", ProjectID: p1},
	}, k.Services)
	id, err := svc.ValidateAPIKey(ctx, k.Key)
	require.NoError(t, err)
	assert.Equal(t, []string{web, shared}, id.ServiceIDs)

	for name, in := range map[string]apikey.CreateInput{
		"another project's": {ProjectID: p1, Scopes: deploy, ServiceIDs: []string{other}},
		"repeated":          {ProjectID: p1, Scopes: deploy, ServiceIDs: []string{web, web}},
		"unknown":           {ProjectID: p1, Scopes: deploy, ServiceIDs: []string{"00000000-0000-4000-8000-000000000009"}},
		"not a UUID":        {ProjectID: p1, Scopes: deploy, ServiceIDs: []string{"web"}},
		"with code_read":    {ProjectID: p1, Scopes: []string{apikey.ScopeDeploy, apikey.ScopeCodeRead}, ServiceIDs: []string{web}},
	} {
		_, err := create(name, in)
		require.ErrorIs(t, err, apikey.ErrInvalidServices, name)
	}

	// The database holds the rule too.
	_, err = db.Exec(ctx, `INSERT INTO api_keys (user_id, name, key_hash, prefix, project_id, scopes, service_ids)
		VALUES ($1, 'raw', 'h', 'paas_x', $2, ARRAY['code_read'], ARRAY[$3::uuid])`, userID, p1, web)
	require.ErrorContains(t, err, "api_keys_service_ids_check")
	_, err = db.Exec(ctx, `INSERT INTO api_keys (user_id, name, key_hash, prefix, service_ids)
		VALUES ($1, 'raw', 'h', 'paas_x', ARRAY[$2::uuid])`, userID, web)
	require.ErrorContains(t, err, "api_keys_service_ids_check")

	// A deleted service stays in the key, so it never widens; the list
	// names the rest.
	_, err = db.Exec(ctx, `DELETE FROM services WHERE id = $1`, web)
	require.NoError(t, err)
	id, err = svc.ValidateAPIKey(ctx, k.Key)
	require.NoError(t, err)
	assert.Equal(t, []string{web, shared}, id.ServiceIDs)
	keys, err := svc.List(ctx, userID)
	require.NoError(t, err)
	for _, listed := range keys {
		if listed.ID == k.ID {
			assert.Equal(t, []string{web, shared}, listed.ServiceIDs)
			assert.Equal(t, []apikey.KeyService{{ID: shared, Name: "cache", ProjectID: p1}}, listed.Services)
		} else {
			assert.Empty(t, listed.ServiceIDs)
			assert.NotNil(t, listed.Services)
		}
	}
}

// A project's keys (0077): an owner sees every member's, anyone else their
// own, newest first a page at a time; other projects' and full keys never.
func TestListForProject(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	svc := apikey.NewService(apikey.NewPostgresRepository(db), zap.NewNop())

	var alex, sam, orgID, p1, p2 string
	q := func(sql string, dest *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dest))
	}
	q(`INSERT INTO users (email, name, password_hash) VALUES ('alex@toft.test', 'alex', 'x') RETURNING id::text`, &alex)
	q(`INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', '', 'x') RETURNING id::text`, &sam)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'storefront') RETURNING id::text`, &p1, alex, orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'billing') RETURNING id::text`, &p2, alex, orgID)

	key := func(user, name, project string) {
		t.Helper()
		in := apikey.CreateInput{Name: name}
		if project != "" {
			in.ProjectID, in.Scopes = project, []string{apikey.ScopeDeploy}
		}
		_, err := svc.Create(ctx, user, in)
		require.NoError(t, err)
	}
	key(alex, "claude-code", p1)
	key(sam, "ci-deploy", p1)
	key(sam, "billing-ci", p2)
	key(sam, "laptop", "")

	names := func(keys []apikey.ProjectKey) []string {
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			out = append(out, k.Name+":"+k.OwnerName)
		}
		return out
	}
	all, more, err := svc.ListForProject(ctx, p1, alex, "owner", time.Time{}, "", 50)
	require.NoError(t, err)
	assert.False(t, more)
	assert.Equal(t, []string{"ci-deploy:sam@toft.test", "claude-code:alex"}, names(all), "every member's, newest first; the email without a name")

	own, _, err := svc.ListForProject(ctx, p1, sam, "developer", time.Time{}, "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"ci-deploy:sam@toft.test"}, names(own), "a developer sees their own")

	page, more, err := svc.ListForProject(ctx, p1, alex, "owner", time.Time{}, "", 1)
	require.NoError(t, err)
	require.True(t, more)
	rest, more, err := svc.ListForProject(ctx, p1, alex, "owner", page[0].CreatedAt, page[0].ID, 1)
	require.NoError(t, err)
	assert.False(t, more)
	assert.Equal(t, []string{"claude-code:alex"}, names(rest), "the next page is older than the last row")
}
