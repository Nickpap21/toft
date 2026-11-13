package preview_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

var sha = strings.Repeat("a", 40)

// GetPullForPreview decides whether a preview waits for approval (M2): a
// pull request is untrusted when a key opened it, a key pushed to it while
// open, a key pushed to its head branch before, or the branch is an
// agent's. Pushes elsewhere don't count.
func TestPullForPreview(t *testing.T) {
	db := testdb.New(t)
	store := preview.NewPostgresStore(db)
	ctx := context.Background()
	f := newFixture(t, db)

	plain := f.pull("plain", f.repo, "")
	_, err := db.Exec(ctx, `INSERT INTO pull_request_events (pull_request_id, kind, data) VALUES ($1, 'pushed', '{"with_key": false}')`, plain)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `INSERT INTO pull_request_events (pull_request_id, kind, data) VALUES ($1, 'title_changed', '{"with_key": true}')`, plain)
	require.NoError(t, err)
	f.push(f.repo, "plain", false)
	f.push(f.repo, "other-branch", true)
	f.push(f.second, "plain", true)
	_, err = db.Exec(ctx, `INSERT INTO git_events (repository_id, kind, branch, before_sha, after_sha, with_key)
		VALUES ($1, 'merge', 'plain', $2, $2, TRUE)`, f.repo, sha)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `INSERT INTO agent_branches (repository_id, name, connection_id, user_id, token_hash, token_expires_at)
		VALUES ($1, 'plain', $2, $3, 'other-repository-token', NOW() + interval '1 hour')`, f.second, f.agent, f.user)
	require.NoError(t, err)

	openedWithKey := f.pull("opened-with-key", f.repo, "")
	_, err = db.Exec(ctx, `UPDATE pull_requests SET opened_with_key = TRUE, api_key_id = $2 WHERE id = $1`, openedWithKey, f.key)
	require.NoError(t, err)

	keyPushedWhileOpen := f.pull("key-pushed-while-open", f.repo, "")
	_, err = db.Exec(ctx, `INSERT INTO pull_request_events (pull_request_id, kind, data) VALUES ($1, 'pushed', '{"with_key": true}')`, keyPushedWhileOpen)
	require.NoError(t, err)

	keyPushedBefore := f.pull("key-pushed-before", f.repo, "")
	f.push(f.repo, "key-pushed-before", true)

	agentBranch := f.pull("agent-branch", f.repo, f.agent)
	_, err = db.Exec(ctx, `INSERT INTO agent_branches (repository_id, name, connection_id, user_id, token_hash, token_expires_at)
		VALUES ($1, 'agent-branch', $2, $3, 'agent-branch-token', NOW() + interval '1 hour')`, f.repo, f.agent, f.user)
	require.NoError(t, err)

	ofSecond := f.pull("of-second", f.second, "")

	for _, tc := range []struct {
		name, id                        string
		openedWithKey, keyPushed, first bool
		keyName, agentName, repoName    string
	}{
		{name: "a member's own pushes", id: plain, first: true, repoName: "web"},
		{name: "opened with a key", id: openedWithKey, openedWithKey: true, first: true, keyName: "ci", repoName: "web"},
		{name: "a key pushed while open", id: keyPushedWhileOpen, keyPushed: true, first: true, repoName: "web"},
		{name: "a key pushed to the branch before", id: keyPushedBefore, keyPushed: true, first: true, repoName: "web"},
		{name: "an agent's branch", id: agentBranch, keyPushed: true, first: true, agentName: "Claude", repoName: "web"},
		{name: "the project's second repository", id: ofSecond, repoName: "api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := store.Pull(ctx, tc.id)
			require.NoError(t, err)
			require.Equal(t, tc.id, p.ID)
			require.Equal(t, tc.openedWithKey, p.OpenedWithKey, "opened with key")
			require.Equal(t, tc.keyPushed, p.KeyPushed, "key pushed")
			require.Equal(t, tc.openedWithKey || tc.keyPushed, p.Untrusted())
			require.Equal(t, tc.first, p.FirstRepository, "first repository")
			require.Equal(t, tc.keyName, p.APIKeyName)
			require.Equal(t, tc.agentName, p.AgentName)
			require.Equal(t, tc.repoName, p.RepoName)
			require.Equal(t, "main", p.Base)
			require.Equal(t, "Dev", p.AuthorName)
			require.Equal(t, f.project, p.ProjectID)
			require.Equal(t, "Shop", p.ProjectName)
			require.Equal(t, f.org, p.OrgID)
		})
	}

	_, err = store.Pull(ctx, "6f1c1d2e-0000-4000-8000-000000000000")
	require.ErrorIs(t, err, preview.ErrPullGone)
}

// TestMigrationFilesRoundTrip: a preview keeps the migrations its pull
// request touches, reads them back, and the conversation takes the event
// that names them (D-144).
func TestMigrationFilesRoundTrip(t *testing.T) {
	db := testdb.New(t)
	store := preview.NewPostgresStore(db)
	ctx := context.Background()
	f := newFixture(t, db)
	pull := f.pull("migrations", f.repo, "")
	var env string
	f.row(&env, `INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, f.project)

	pv := &preview.Preview{PullID: pull, ProjectID: f.project, SourceEnvID: env, HeadSHA: sha, State: preview.StateWaitingRoom}
	require.NoError(t, store.Insert(ctx, pv))
	got, err := store.ByPull(ctx, pull)
	require.NoError(t, err)
	require.Equal(t, []string{}, got.MigrationFiles, "none until the diff is read")
	require.False(t, got.MigrationsUnchecked, "checked until a diff is too large")

	files := []string{"db/migrations/0042_payment_intents.sql", "schema.prisma"}
	require.NoError(t, store.SetMigrationFiles(ctx, pv.ID, files, true))
	got, err = store.ByPull(ctx, pull)
	require.NoError(t, err)
	require.Equal(t, files, got.MigrationFiles)
	require.True(t, got.MigrationsUnchecked, "a diff too large to check (D-147)")
	listed, _, err := store.List(ctx, f.project, "", 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.True(t, listed[0].MigrationsUnchecked, "the list reads it too")

	require.NoError(t, store.SetMigrationFiles(ctx, pv.ID, files, false))
	got, err = store.ByPull(ctx, pull)
	require.NoError(t, err)
	require.False(t, got.MigrationsUnchecked, "a diff that fits clears it")

	_, err = db.Exec(ctx, `INSERT INTO pull_request_events (pull_request_id, kind, data) VALUES ($1, 'preview_migrations_skipped', $2)`,
		pull, `{"sha": "`+sha+`", "files": ["schema.prisma"]}`)
	require.NoError(t, err, "the event kind's CHECK takes it")
}

type fixture struct {
	t                                            *testing.T
	db                                           *pgxpool.Pool
	user, org, project, repo, second, key, agent string
	numbers                                      map[string]int
}

// newFixture is a workspace with a project of two repositories (web made
// first), an API key and an agent connection.
func newFixture(t *testing.T, db *pgxpool.Pool) *fixture {
	f := &fixture{t: t, db: db, numbers: map[string]int{}}
	f.row(&f.user, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	f.row(&f.org, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	f.row(&f.project, `INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, f.user, f.org)
	f.row(&f.repo, `INSERT INTO repositories (org_id, project_id, name, created_at) VALUES ($1, $2, 'web', NOW() - interval '1 day') RETURNING id::text`, f.org, f.project)
	f.row(&f.second, `INSERT INTO repositories (org_id, project_id, name) VALUES ($1, $2, 'api') RETURNING id::text`, f.org, f.project)
	f.row(&f.key, `INSERT INTO api_keys (user_id, name, key_hash, prefix) VALUES ($1, 'ci', 'hash', 'paas_x') RETURNING id::text`, f.user)
	var client string
	f.row(&client, `INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Claude', '{http://127.0.0.1/cb}') RETURNING id::text`)
	f.row(&f.agent, `INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
		VALUES ($1, $2, $3, 'Claude', TRUE, '{read,code}', NOW() + interval '1 day') RETURNING id::text`, f.org, f.user, client)
	return f
}

func (f *fixture) row(dst *string, sql string, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.db.QueryRow(context.Background(), sql, args...).Scan(dst))
}

// pull opens a pull request from head in repo, by an agent when agent is set.
func (f *fixture) pull(head, repo, agent string) string {
	f.t.Helper()
	f.numbers[repo]++
	var agentID any
	if agent != "" {
		agentID = agent
	}
	var id string
	f.row(&id, `INSERT INTO pull_requests (repository_id, number, title, author_id, base_branch, head_branch, head_sha, agent_connection_id)
		VALUES ($1, $2, $3, $4, 'main', $3, $5, $6) RETURNING id::text`, repo, f.numbers[repo], head, f.user, sha, agentID)
	return id
}

// push records a push to branch in repo, made with a key or not.
func (f *fixture) push(repo, branch string, withKey bool) {
	f.t.Helper()
	_, err := f.db.Exec(context.Background(), `INSERT INTO git_events (repository_id, kind, branch, before_sha, after_sha, with_key)
		VALUES ($1, 'push', $2, $3, $3, $4)`, repo, branch, sha, withKey)
	require.NoError(f.t, err)
}

// The project's own limit (0077): 5 until set, kept when a save leaves it
// out, and the database refuses one outside 1 to 20.
func TestSettingsMaxRoundTrip(t *testing.T) {
	db := testdb.New(t)
	store := preview.NewPostgresStore(db)
	ctx := context.Background()
	f := newFixture(t, db)

	set, err := store.Settings(ctx, f.project)
	require.NoError(t, err)
	require.Equal(t, 5, set.Max)
	eight := 8
	require.NoError(t, store.SetSettings(ctx, f.project, "", "", &eight))
	require.NoError(t, store.SetSettings(ctx, f.project, "", "preview.example.com", nil))
	set, err = store.Settings(ctx, f.project)
	require.NoError(t, err)
	require.Equal(t, 8, set.Max, "a save without max keeps it")
	require.Equal(t, "preview.example.com", set.Domain)
	bad := 21
	require.Error(t, store.SetSettings(ctx, f.project, "", "", &bad), "the CHECK refuses 21")
}
