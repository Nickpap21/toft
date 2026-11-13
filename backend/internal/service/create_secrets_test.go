package service_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A catalog service created through the API without its secrets gets them
// generated, stored write-once with it and returned this once; a value the
// caller sent is kept; an app whoever opens first would own gets a
// temporary login; and a secret that fails leaves no service.
func TestCreateGeneratesTheTemplateSecrets(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, envID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, userID).Scan(&envID))

	vars := envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop())
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithVariables(vars)
	m.WithTransactor(dbtx.NewRunner(db))

	grafana := func(name string, secrets ...service.KeyValue) service.CreateInput {
		return service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage,
			DockerImage: "grafana/grafana:10.4.2", Port: 3001, AddonSlug: "grafana", Secrets: secrets}
	}
	// The secrets stored; the template's plain settings are stored at their
	// defaults beside them (TestCreateSetsVariablesAndStartsTheFirstDeploy).
	stored := func(serviceID string) map[string]string {
		t.Helper()
		all, err := vars.ListAllDecrypted(ctx, serviceID)
		require.NoError(t, err)
		out := map[string]string{}
		for _, v := range all {
			if v.IsSecret {
				out[v.Key] = v.Value
			}
		}
		return out
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&n))
		return n
	}

	saved, err := m.Create(ctx, envID, userID, grafana("grafana"))
	require.NoError(t, err)
	require.Len(t, saved.GeneratedSecrets, 1)
	g := saved.GeneratedSecrets[0]
	require.Equal(t, "GF_SECURITY_ADMIN_PASSWORD", g.Key)
	require.Regexp(t, `^[A-HJ-NP-Za-km-z2-9]{24}$`, g.Value)
	require.Equal(t, map[string]string{g.Key: g.Value}, stored(saved.ID))
	require.Nil(t, saved.TemporaryLogin)

	// Shared by the workspace (Grafana's scope on the New service screen),
	// with the caller's own value: kept, nothing generated.
	saved, err = m.CreateOrgScoped(ctx, orgID, userID, grafana("grafana-shared", service.KeyValue{Key: "GF_SECURITY_ADMIN_PASSWORD", Value: "mine"}))
	require.NoError(t, err)
	require.Empty(t, saved.GeneratedSecrets)
	require.Equal(t, map[string]string{"GF_SECURITY_ADMIN_PASSWORD": "mine"}, stored(saved.ID))

	// A secret refused after the service was written leaves no service, in
	// each kind of create.
	bad := service.KeyValue{Key: "not a key", Value: "x"}
	_, err = m.Create(ctx, envID, userID, grafana("bad-env", bad))
	require.ErrorIs(t, err, envvar.ErrInvalidKey)
	_, err = m.CreateOrgScoped(ctx, orgID, userID, grafana("bad-org", bad))
	require.ErrorIs(t, err, envvar.ErrInvalidKey)
	require.Zero(t, count(`SELECT count(*) FROM services WHERE name LIKE 'bad-%'`))

	// A key given twice is refused before anything is written.
	twice := service.KeyValue{Key: "K", Value: "v"}
	_, err = m.Create(ctx, envID, userID, grafana("twice", twice, twice))
	require.ErrorIs(t, err, service.ErrInvalidConfig)

	// Ghost: whoever opens it first becomes its owner, so a temporary
	// login goes in front of it, with its two MySQL passwords generated.
	ghost := service.CreateInput{Name: "blog", Type: service.TypeWeb, SourceType: service.SourceTypeDockerCompose,
		ComposeContent: mustTemplate(t, "ghost").ComposeContent, Port: 2368, AddonSlug: "ghost"}
	saved, err = m.Create(ctx, envID, userID, ghost)
	require.NoError(t, err)
	require.NotNil(t, saved.TemporaryLogin)
	require.Equal(t, "toft", saved.TemporaryLogin.User)
	require.Len(t, saved.TemporaryLogin.Password, 24)
	require.Equal(t, 1, count(`SELECT count(*) FROM services WHERE id = $1 AND basic_auth_user = 'toft' AND basic_auth_password NOT IN ('', $2)`,
		saved.ID, saved.TemporaryLogin.Password))
	require.Len(t, saved.GeneratedSecrets, 2)
	require.Len(t, stored(saved.ID), 2)

	// One the caller protects itself keeps its own.
	ghost.Name, ghost.BasicAuthUser, ghost.BasicAuthPassword = "blog-2", "me", "own-password"
	saved, err = m.Create(ctx, envID, userID, ghost)
	require.NoError(t, err)
	require.Nil(t, saved.TemporaryLogin)
	require.Equal(t, 1, count(`SELECT count(*) FROM services WHERE id = $1 AND basic_auth_user = 'me'`, saved.ID))

	// A service from outside the catalog gets nothing.
	saved, err = m.Create(ctx, envID, userID, service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "nginx:1", Port: 80})
	require.NoError(t, err)
	require.Empty(t, saved.GeneratedSecrets)
	require.Nil(t, saved.TemporaryLogin)
}

func mustTemplate(t *testing.T, slug string) catalog.Template {
	t.Helper()
	tmpl, ok := catalog.Get(slug)
	require.True(t, ok, slug)
	return tmpl
}
