package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/reveal"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A catalog create keeps its admin password for its creator (D-160), in
// its transaction: Grafana's generated one, the caller's own, a temporary
// login (key ""), and nothing for a service outside the catalog.
func TestCreateKeepsTheAdminPasswordForItsCreator(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, envID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, userID).Scan(&envID))

	reveals := reveal.NewPostgresRepository(db, keys)
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithVariables(envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop()))
	m.WithTransactor(dbtx.NewRunner(db))
	m.WithReveals(reveals)
	kept := func(serviceID string) *reveal.Reveal {
		t.Helper()
		r, err := reveals.Get(ctx, serviceID, userID, time.Now().Add(-reveal.Keep))
		require.NoError(t, err)
		return r
	}
	grafana := func(name string, secrets ...service.KeyValue) service.CreateInput {
		return service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage,
			DockerImage: "grafana/grafana:10.4.2", Port: 3001, AddonSlug: "grafana", Secrets: secrets}
	}

	saved, err := m.Create(ctx, envID, userID, grafana("grafana"))
	require.NoError(t, err)
	r := kept(saved.ID)
	require.NotNil(t, r)
	require.Equal(t, "GF_SECURITY_ADMIN_PASSWORD", r.Key)
	require.Equal(t, saved.GeneratedSecrets[0].Value, r.Password)
	require.Equal(t, "created", r.Reason)

	// The browser sends the value it showed: kept as sent.
	saved, err = m.CreateOrgScoped(ctx, orgID, userID, grafana("grafana-shared", service.KeyValue{Key: "GF_SECURITY_ADMIN_PASSWORD", Value: "shown-in-the-form"}))
	require.NoError(t, err)
	require.Equal(t, "shown-in-the-form", kept(saved.ID).Password)

	ghost := service.CreateInput{Name: "blog", Type: service.TypeWeb, SourceType: service.SourceTypeDockerCompose,
		ComposeContent: mustTemplate(t, "ghost").ComposeContent, Port: 2368, AddonSlug: "ghost"}
	saved, err = m.Create(ctx, envID, userID, ghost)
	require.NoError(t, err)
	r = kept(saved.ID)
	require.Equal(t, "", r.Key, "the temporary login")
	require.Equal(t, saved.TemporaryLogin.Password, r.Password)

	// The new-service form generates the temporary login and sends it.
	ghost.Name, ghost.BasicAuthUser, ghost.BasicAuthPassword = "blog-form", "toft", "shown-in-the-form"
	saved, err = m.Create(ctx, envID, userID, ghost)
	require.NoError(t, err)
	r = kept(saved.ID)
	require.NotNil(t, r)
	require.Equal(t, "", r.Key, "the temporary login")
	require.Equal(t, "shown-in-the-form", r.Password)

	// The caller's own basic auth is not a temporary login.
	ghost.Name, ghost.BasicAuthUser = "blog-own", "editor"
	saved, err = m.Create(ctx, envID, userID, ghost)
	require.NoError(t, err)
	require.Nil(t, kept(saved.ID))

	saved, err = m.Create(ctx, envID, userID, service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "nginx:1", Port: 80})
	require.NoError(t, err)
	require.Nil(t, kept(saved.ID))
}

type failingKeeper struct{}

func (failingKeeper) Keep(context.Context, string, string, string, string, string) error {
	return errors.New("keep failed")
}
func (failingKeeper) Forget(context.Context, string, string) error { return nil }

// The project-scoped create and the create with a new project keep it too,
// and a copy that can't be kept leaves no service and no secret.
func TestEveryCreateKeepsTheAdminPassword(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, projectID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, orgID, userID).Scan(&projectID))

	projects := project.NewService(project.NewPostgresRepository(db))
	projects.WithEnvCreator(defaultEnv{environment.NewService(environment.NewPostgresRepository(db))})
	projects.WithTransactor(dbtx.NewRunner(db))
	reveals := reveal.NewPostgresRepository(db, keys)
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithAddresser(namedAddresser{})
	m.WithProjects(projects)
	m.WithVariables(envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop()))
	m.WithTransactor(dbtx.NewRunner(db))
	m.WithReveals(reveals)
	grafana := func(name string) service.CreateInput {
		return service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage,
			DockerImage: "grafana/grafana:10.4.2", Port: 3001, AddonSlug: "grafana"}
	}
	kept := func(serviceID string) string {
		t.Helper()
		r, err := reveals.Get(ctx, serviceID, userID, time.Now().Add(-reveal.Keep))
		require.NoError(t, err)
		require.NotNil(t, r, serviceID)
		return r.Password
	}

	saved, err := m.CreateProjectScoped(ctx, projectID, userID, grafana("grafana-project"))
	require.NoError(t, err)
	require.Equal(t, saved.GeneratedSecrets[0].Value, kept(saved.ID))

	_, saved, err = m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "metrics"}, grafana("grafana"))
	require.NoError(t, err)
	require.Equal(t, saved.GeneratedSecrets[0].Value, kept(saved.ID))

	count := func(sql string) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, sql).Scan(&n))
		return n
	}
	services, secrets := count(`SELECT count(*) FROM services`), count(`SELECT count(*) FROM env_vars`)
	m.WithReveals(failingKeeper{})
	_, err = m.CreateProjectScoped(ctx, projectID, userID, grafana("grafana-2"))
	require.Error(t, err)
	_, _, err = m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "metrics-2"}, grafana("grafana"))
	require.Error(t, err)
	require.Equal(t, services, count(`SELECT count(*) FROM services`), "no service")
	require.Equal(t, secrets, count(`SELECT count(*) FROM env_vars`), "no secret")
}
