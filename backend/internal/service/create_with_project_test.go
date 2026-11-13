package service_test

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type defaultEnv struct{ envs *environment.Service }

func (d defaultEnv) EnsureDefault(ctx context.Context, projectID string) (string, error) {
	env, err := d.envs.EnsureDefault(ctx, projectID)
	if err != nil {
		return "", err
	}
	return env.ID, nil
}

// namedAddresser names <service>-<environment> under one server's address.
type namedAddresser struct{}

func (namedAddresser) Address(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (namedAddresser) ServerAddress(context.Context, string) (string, error) { return "", nil }
func (namedAddresser) AddressIn(_ context.Context, _, environment, _, name string) (string, error) {
	return name + "-" + environment + ".203-0-113-10.sslip.io", nil
}

// A new project's first service (7g): the project, its production
// environment, the service with its address, variables and secrets are all
// stored, or none of them is.
func TestCreateWithProjectIsOneTransaction(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))

	projects := project.NewService(project.NewPostgresRepository(db))
	projects.WithEnvCreator(defaultEnv{environment.NewService(environment.NewPostgresRepository(db))})
	projects.WithTransactor(dbtx.NewRunner(db))
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithAddresser(namedAddresser{})
	m.WithProjects(projects)
	m.WithVariables(envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop()))

	in := func(name string, secret string) service.CreateInput {
		return service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage,
			DockerImage: "louislam/uptime-kuma:2", Port: 3001, Address: true,
			Secrets:   []service.KeyValue{{Key: secret, Value: "s3cret"}},
			Variables: []service.Variable{{Key: "TZ", Value: "UTC"}},
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&n))
		return n
	}

	p, saved, err := m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "uptime-kuma"}, in("uptime-kuma", "ADMIN_PASSWORD"))
	require.NoError(t, err)
	require.Equal(t, "uptime-kuma", p.Name)
	require.Equal(t, p.ID, saved.ProjectID)
	require.Equal(t, 1, count(`SELECT count(*) FROM environments WHERE project_id = $1 AND name = 'production'`, p.ID))
	require.Equal(t, 1, count(`SELECT count(*) FROM domains WHERE service_id = $1 AND hostname = 'uptime-kuma-production.203-0-113-10.sslip.io'`, saved.ID))
	require.Equal(t, 1, count(`SELECT count(*) FROM env_vars WHERE service_id = $1 AND key = 'TZ' AND NOT is_secret`, saved.ID))
	require.Equal(t, 1, count(`SELECT count(*) FROM env_vars WHERE service_id = $1 AND key = 'ADMIN_PASSWORD' AND is_secret AND value <> 's3cret'`, saved.ID))

	// The secret's key is refused after the project, its environment, the
	// service and its address were written: none of them is left.
	_, _, err = m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "bad"}, in("bad", "not a key"))
	require.ErrorIs(t, err, envvar.ErrInvalidKey)
	require.Zero(t, count(`SELECT count(*) FROM projects WHERE name = 'bad'`))
	require.Zero(t, count(`SELECT count(*) FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.name = 'bad'`))
	require.Zero(t, count(`SELECT count(*) FROM services WHERE name = 'bad'`))
	require.Zero(t, count(`SELECT count(*) FROM domains WHERE hostname LIKE 'bad-%'`))

	_, _, err = m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "uptime-kuma"}, in("uptime-kuma", "OTHER"))
	require.ErrorIs(t, err, project.ErrDuplicateName)

	// A key given twice is refused before anything is written.
	dup := in("dup", "TZ")
	_, _, err = m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "dup"}, dup)
	require.ErrorIs(t, err, service.ErrInvalidConfig)
	require.Zero(t, count(`SELECT count(*) FROM projects WHERE name = 'dup'`))

	// A new project has no repositories yet.
	repoIn := in("api", "K")
	repoIn.SourceType, repoIn.DockerImage, repoIn.RepositoryID = service.SourceTypeGit, "", "00000000-0000-0000-0000-000000000001"
	_, _, err = m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "api"}, repoIn)
	require.ErrorIs(t, err, service.ErrInvalidConfig)
	require.Zero(t, count(`SELECT count(*) FROM projects WHERE name = 'api'`))
}

// An address taken between naming and creating, inside the project's
// transaction: the service's savepoint is undone, the next name is stored,
// and the project commits (D-150, 7g).
func TestCreateWithProjectNamesTheNextAddressWhenTaken(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, userID, envID, otherID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, userID).Scan(&envID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO services (environment_id, name, type, source_type, port) VALUES ($1, 'other', 'web', 'docker-image', 80) RETURNING id::text`, envID).Scan(&otherID))
	const taken, next = "web-production.203-0-113-10.sslip.io", "web-production-2.203-0-113-10.sslip.io"
	_, err := db.Exec(ctx, `INSERT INTO domains (service_id, hostname) VALUES ($1, $2)`, otherID, taken)
	require.NoError(t, err)

	projects := project.NewService(project.NewPostgresRepository(db))
	projects.WithEnvCreator(defaultEnv{environment.NewService(environment.NewPostgresRepository(db))})
	projects.WithTransactor(dbtx.NewRunner(db))
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithAddresser(&seqAddresser{names: []string{taken, next}})
	m.WithProjects(projects)
	m.WithVariables(envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop()))

	p, saved, err := m.CreateWithProject(ctx, orgID, userID, project.CreateProjectInput{Name: "web"}, service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "nginx:1", Port: 80, Address: true})
	require.NoError(t, err)
	var host, committed string
	require.NoError(t, db.QueryRow(ctx, `SELECT hostname FROM domains WHERE service_id = $1`, saved.ID).Scan(&host))
	require.Equal(t, next, host)
	require.NoError(t, db.QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, p.ID).Scan(&committed))
	require.Equal(t, "web", committed)
}
