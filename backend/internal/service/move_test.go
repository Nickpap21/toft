package service_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Move to workspace (D-162): the service keeps its id and gets its own
// copy of what its environment shared, a secret sealed again for it, its
// own value winning; a name the workspace has already moves nothing.
func TestMoveToWorkspaceKeepsWhatTheEnvironmentShared(t *testing.T) {
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

	env := vars.Environment()
	_, err := env.Upsert(ctx, envID, userID, envvar.UpsertInput{Key: "REGION", Value: "eu"})
	require.NoError(t, err)
	_, err = env.Upsert(ctx, envID, userID, envvar.UpsertInput{Key: "LOG_LEVEL", Value: "info"})
	require.NoError(t, err)
	_, err = env.CreateSecret(ctx, envID, userID, envvar.CreateSecretInput{Key: "SMTP_PASSWORD", Value: "s3cret"})
	require.NoError(t, err)

	image := func(name string) service.CreateInput {
		return service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/grafana:10.4.2", Port: 3000}
	}
	grafana, err := m.Create(ctx, envID, userID, image("grafana"))
	require.NoError(t, err)
	_, err = vars.Upsert(ctx, grafana.ID, userID, envvar.UpsertInput{Key: "LOG_LEVEL", Value: "debug"})
	require.NoError(t, err)

	moved, _, err := m.MoveToWorkspace(ctx, orgID, grafana.ID)
	require.NoError(t, err)
	require.Equal(t, grafana.ID, moved.ID)
	require.Equal(t, service.ScopeOrg, moved.Scope)
	require.Empty(t, moved.EnvironmentID)

	all, err := vars.ListAllDecrypted(ctx, grafana.ID)
	require.NoError(t, err)
	got := map[string]string{}
	for _, v := range all {
		got[v.Key] = v.Value
	}
	require.Equal(t, map[string]string{"REGION": "eu", "LOG_LEVEL": "debug", "SMTP_PASSWORD": "s3cret"}, got)

	_, _, err = m.MoveToWorkspace(ctx, orgID, grafana.ID)
	require.ErrorIs(t, err, service.ErrAlreadyWorkspace)

	// A second grafana in the environment: the workspace has one by that
	// name now, so nothing moves and nothing is copied.
	in := image("grafana")
	in.NetworkAlias = "grafana-2"
	second, err := m.Create(ctx, envID, userID, in)
	require.NoError(t, err)
	_, _, err = m.MoveToWorkspace(ctx, orgID, second.ID)
	require.ErrorIs(t, err, service.ErrDuplicateName)
	still, err := m.Get(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, service.ScopeEnvironment, still.Scope)
	var own int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM env_vars WHERE service_id = $1`, second.ID).Scan(&own))
	require.Zero(t, own, "the failed move copied the environment's variables")
}

// A workspace service joins every network of the workspace, so a service
// whose alias another environment's service uses stays where it is.
func TestMoveToWorkspaceRefusesAnAliasInUse(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, prodID, stagingID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id),
		e AS (INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id, project_id)
		INSERT INTO environments (project_id, name) SELECT project_id, 'staging' FROM e RETURNING (SELECT id::text FROM e), id::text`, orgID, userID).Scan(&prodID, &stagingID))

	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithVariables(envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop()))
	m.WithTransactor(dbtx.NewRunner(db))
	in := service.CreateInput{Name: "grafana", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/grafana:10.4.2", Port: 3000, NetworkAlias: "grafana"}
	grafana, err := m.Create(ctx, prodID, userID, in)
	require.NoError(t, err)
	in.Name = "dashboards"
	_, err = m.Create(ctx, stagingID, userID, in)
	require.NoError(t, err)

	_, _, err = m.MoveToWorkspace(ctx, orgID, grafana.ID)
	require.ErrorIs(t, err, service.ErrMoveAliasTaken)

	// A preview's copy stays with its preview.
	copyIn := service.CreateInput{Name: "grafana-pr-7", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/grafana:10.4.2", Port: 3000}
	previewCopy, err := m.Create(ctx, stagingID, userID, copyIn)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE services SET preview_of = $1 WHERE id = $2`, grafana.ID, previewCopy.ID)
	require.NoError(t, err)
	_, _, err = m.MoveToWorkspace(ctx, orgID, previewCopy.ID)
	require.ErrorIs(t, err, service.ErrPreviewService)

	// Creating a workspace service checks the same: by its alias, or by the
	// alias its name gives it.
	_, err = m.CreateOrgScoped(ctx, orgID, userID, service.CreateInput{Name: "metrics", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/grafana:10.4.2", Port: 3000, NetworkAlias: "grafana"})
	require.ErrorIs(t, err, service.ErrMoveAliasTaken)
	_, err = m.CreateOrgScoped(ctx, orgID, userID, service.CreateInput{Name: "grafana", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/grafana:10.4.2", Port: 3000})
	require.ErrorIs(t, err, service.ErrMoveAliasTaken)
	_, err = m.CreateOrgScoped(ctx, orgID, userID, service.CreateInput{Name: "grafana-shared", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/grafana:10.4.2", Port: 3000})
	require.NoError(t, err)

	// Deploy on push and compose stacks stay in their project.
	on := true
	api, err := m.Create(ctx, prodID, userID, service.CreateInput{Name: "api", Type: service.TypeWeb, SourceType: service.SourceTypeGit,
		GitRepoURL: "https://github.com/founders/api.git", GitBranch: "main", Port: 8080, DeployOnPush: &on})
	require.NoError(t, err)
	_, _, err = m.MoveToWorkspace(ctx, orgID, api.ID)
	require.ErrorIs(t, err, service.ErrMoveDeploysOnPush)
	stack, err := m.Create(ctx, prodID, userID, service.CreateInput{Name: "stack", Type: service.TypeWeb, SourceType: service.SourceTypeDockerCompose,
		ComposeContent: "services:\n  web:\n    image: nginx:1.27\n"})
	require.NoError(t, err)
	_, _, err = m.MoveToWorkspace(ctx, orgID, stack.ID)
	require.ErrorIs(t, err, service.ErrMoveCompose)
}
