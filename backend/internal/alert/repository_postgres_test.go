package alert_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/alert"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// ServicesOrgIDs finds the workspace of a service in an environment, one
// shared by a project and one shared by a workspace, each once, and leaves
// out a service that is gone.
func TestServicesOrgIDs(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := alert.NewPostgresRepository(db)
	var user, acme, initech, project, env, envSvc, projectSvc, orgSvc string
	row := func(dst *string, sql string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	row(&user, `INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', 'Sam', 'x') RETURNING id::text`)
	row(&acme, `INSERT INTO organizations (name, slug) VALUES ('Acme', 'acme') RETURNING id::text`)
	row(&initech, `INSERT INTO organizations (name, slug) VALUES ('Initech', 'initech') RETURNING id::text`)
	row(&project, `INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, user, acme)
	row(&env, `INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, project)
	row(&envSvc, `INSERT INTO services (environment_id, name, source_type, port) VALUES ($1, 'web', 'docker-image', 80) RETURNING id::text`, env)
	row(&projectSvc, `INSERT INTO services (scope, project_id, name, source_type, port) VALUES ('project', $1, 'db', 'docker-image', 5432) RETURNING id::text`, project)
	row(&orgSvc, `INSERT INTO services (scope, scoped_org_id, name, source_type, port) VALUES ('org', $1, 'cache', 'docker-image', 6379) RETURNING id::text`, initech)

	got, err := repo.ServicesOrgIDs(ctx, []string{envSvc, projectSvc, orgSvc, uuid.NewString()})
	require.NoError(t, err)
	slices.Sort(got)
	want := []string{acme, initech}
	slices.Sort(want)
	require.Equal(t, want, got)

	got, err = repo.ServicesOrgIDs(ctx, []string{uuid.NewString()})
	require.NoError(t, err)
	require.Empty(t, got)
}
