package reseal_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/reseal"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/internal/webhook"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

const (
	keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	keyC = "cccccccccccccccccccccccccccccccccccccccc"
)

type fixture struct {
	svc, srv, org, project, mirror, hook, approval, reveal, lost string
}

// seed stores a value in the format before v0.28.0 in every sealed place,
// 1,005 service secrets (more than a batch), and one secret sealed with a
// key nobody has.
func seed(t *testing.T, db *pgxpool.Pool) fixture {
	ctx := context.Background()
	old := func(plain string) string {
		s, err := crypto.Encrypt(keyA, plain)
		require.NoError(t, err)
		return s
	}
	row := func(sql string, args ...any) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(&id), sql)
		return id
	}
	var f fixture
	user := row(`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`)
	f.org = row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	f.project = row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, user, f.org)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, f.project)
	f.svc = row(`INSERT INTO services (environment_id, name, registry_password, git_token, basic_auth_password)
		VALUES ($1, 'web', $2, $3, $4) RETURNING id::text`, env, old("registry-pw"), old("git-token"), old("basic-pw"))
	row(`INSERT INTO env_vars (service_id, key, value, is_secret) VALUES ($1, 'DATABASE_URL', $2, true) RETURNING id::text`, f.svc, old("db-secret"))
	row(`INSERT INTO env_vars (service_id, key, value, is_secret)
		SELECT $1, 'BULK_' || g, $2, true FROM generate_series(1, 1005) g RETURNING 'x'`, f.svc, old("bulk"))
	// A variable that isn't secret is never touched, whatever it looks like.
	row(`INSERT INTO env_vars (service_id, key, value) VALUES ($1, 'PLAIN', $2) RETURNING id::text`, f.svc, old("plain"))
	lost, err := crypto.Encrypt(keyC, "lost")
	require.NoError(t, err)
	f.lost = row(`INSERT INTO env_vars (service_id, key, value, is_secret) VALUES ($1, 'LOST', $2, true) RETURNING id::text`, f.svc, lost)
	row(`INSERT INTO environment_env_vars (environment_id, key, value, is_secret) VALUES ($1, 'SHARED', $2, true) RETURNING id::text`, env, old("shared-secret"))

	f.srv = row(`INSERT INTO servers (user_id, org_id, name, ip_address, ssh_key) VALUES ($1, $2, 'web', '203.0.113.10', $3) RETURNING id::text`,
		user, f.org, old("-----BEGIN OPENSSH PRIVATE KEY-----\npem\n"))
	row(`INSERT INTO server_dns_providers (server_id, provider, credentials) VALUES ($1, 'cloudflare', $2) RETURNING server_id::text`,
		f.srv, old(`{"api_token":"cf"}`))
	row(`INSERT INTO backup_destinations (server_id, endpoint, bucket, access_key, secret_key, repo_password, schedule)
		VALUES ($1, 'https://s3.example.com', 'toft-backups', 'ak', $2, $3, '03:00') RETURNING id::text`, f.srv, old("secret-key"), old("repo-pw"))
	row(`INSERT INTO org_invites (org_id, created_by, token, expires_at, token_encrypted)
		VALUES ($1, $2, 'sha', now() + interval '1 day', $3) RETURNING id::text`, f.org, user, old("invite-link"))
	row(`INSERT INTO project_webhooks (project_id, secret) VALUES ($1, $2) RETURNING project_id::text`, f.project, old("project-hook"))
	repo := row(`INSERT INTO repositories (org_id, project_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, f.org, f.project)
	f.mirror = row(`INSERT INTO repository_mirrors (repository_id, direction, url, password_encrypted)
		VALUES ($1, 'pull', 'https://example.com/a.git', $2) RETURNING id::text`, repo, old("mirror-pw"))
	f.hook = row(`INSERT INTO repository_webhooks (repository_id, url, events, secret_encrypted)
		VALUES ($1, 'https://example.com/hook', '{push}', $2) RETURNING id::text`, repo, old("repo-hook"))
	row(`INSERT INTO alert_channels (org_id, webhook_url, signing_secret) VALUES ($1, 'https://example.com/alert', $2) RETURNING org_id::text`,
		f.org, old("alert-secret"))
	client := row(`INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Agent', '{https://example.com/cb}') RETURNING id::text`)
	conn := row(`INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
		VALUES ($1, $2, $3, 'agent', true, '{read}', now() + interval '1 day') RETURNING id::text`, f.org, user, client)
	f.approval = row(`INSERT INTO agent_approvals (org_id, connection_id, user_id, action, tool, target, request, request_hash, reason, expires_at, sealed)
		VALUES ($1, $2, $3, 'secrets', 'set_secret', 'x', '{}', 'h', 'why', now() + interval '1 day', $4) RETURNING id::text`, f.org, conn, user, old("approval-value"))
	f.reveal = row(`INSERT INTO secret_reveals (id, service_id, user_id, secret_key, sealed, reason)
		VALUES (gen_random_uuid(), $1, $2, 'GF_SECURITY_ADMIN_PASSWORD', $3, 'created') RETURNING id::text`, f.svc, user, old("admin-pw"))
	for key, plain := range map[string]string{"git_ssh_host_key": "git-pem", "agent_panel_key": "panel-pem", "hmac_key": "hmac"} {
		row(`INSERT INTO instance_settings (key, value) VALUES ($1, jsonb_build_object('key', $2::text)) RETURNING key`, key, old(plain))
	}
	return f
}

func TestRun_SealsEveryPlaceUnderTheCurrentKey(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	q := sqlcdb.New(db)
	f := seed(t, db)

	// v0.28.0's first start: every old value becomes v1 under A.
	res, err := reseal.Run(ctx, q, crypto.NewKeys(keyA, ""), false)
	require.NoError(t, err)
	require.EqualValues(t, 1024, res.Resealed, "1,006 service secrets and a value in each of the 18 other places")
	require.Empty(t, res.Left)
	require.Len(t, res.Lost, 1)
	require.Equal(t, reseal.Row{Column: "env_vars.value", ID: f.lost, Err: res.Lost[0].Err}, res.Lost[0])
	require.Error(t, res.Lost[0].Err)

	// A rotation: under B, opening A's values, twice. Until it runs, the
	// check counts every value A opens.
	rotating := crypto.NewKeys(keyB, keyA)
	check, err := reseal.Run(ctx, q, rotating, true)
	require.NoError(t, err)
	require.Len(t, check.Left, 1024)
	require.Len(t, check.Lost, 1)
	res, err = reseal.Run(ctx, q, rotating, false)
	require.NoError(t, err)
	require.EqualValues(t, 1024, res.Resealed)
	res, err = reseal.Run(ctx, q, rotating, false)
	require.NoError(t, err)
	require.Zero(t, res.Resealed, "the second run seals nothing")

	check, err = reseal.Run(ctx, q, rotating, true)
	require.NoError(t, err)
	require.Empty(t, check.Left, "a value nobody can open doesn't hold the rotation up")
	require.Equal(t, []string{f.lost}, []string{check.Lost[0].ID})
	require.Zero(t, check.Resealed)

	// Every value opens where its module reads it, with B alone.
	keys := crypto.NewKeys(keyB, "")
	vars, err := envvar.NewPostgresRepository(db, keys).ListAllForDeployment(ctx, f.svc)
	require.Error(t, err, "LOST is still sealed with C")
	_, err = db.Exec(ctx, `DELETE FROM env_vars WHERE id = $1`, f.lost)
	require.NoError(t, err)
	vars, err = envvar.NewPostgresRepository(db, keys).ListAllForDeployment(ctx, f.svc)
	require.NoError(t, err)
	got := map[string]string{}
	for _, v := range vars {
		got[v.Key] = v.Value
	}
	require.Equal(t, "db-secret", got["DATABASE_URL"])
	require.Equal(t, "bulk", got["BULK_1005"])
	require.Equal(t, "shared-secret", got["SHARED"])
	require.False(t, strings.HasPrefix(got["PLAIN"], "v1."), "a plain variable is left as it was")

	svc, err := service.NewPostgresRepository(db, keys).GetByID(ctx, f.svc)
	require.NoError(t, err)
	require.Equal(t, []string{"registry-pw", "git-token", "basic-pw"}, []string{svc.RegistryPassword, svc.GitToken, svc.BasicAuthPassword})

	servers := server.NewPostgresRepository(db, keys)
	srv, err := servers.GetByID(ctx, f.srv)
	require.NoError(t, err)
	require.Equal(t, "-----BEGIN OPENSSH PRIVATE KEY-----\npem\n", srv.SSHKey)
	dns, err := servers.GetDNSProvider(ctx, f.srv)
	require.NoError(t, err)
	require.Equal(t, "cf", dns.Credentials["api_token"])
	dest, err := servers.GetBackupDestination(ctx, f.srv)
	require.NoError(t, err)
	require.Equal(t, []string{"secret-key", "repo-pw"}, []string{dest.SecretKey, dest.RepoPassword})
	panelKey, err := server.NewPostgresAgentStore(db, keys).PanelAgentKey(ctx)
	require.NoError(t, err)
	require.Equal(t, "panel-pem", panelKey)

	invites, err := org.NewPostgresRepository(db, keys).ListInvites(ctx, f.org)
	require.NoError(t, err)
	require.Len(t, invites, 1)
	require.Equal(t, "invite-link", invites[0].Token)

	hook, err := webhook.NewSecrets(webhook.NewPostgresRepository(db), keys).Get(ctx, f.project)
	require.NoError(t, err)
	require.Equal(t, "project-hook", hook)

	// The places the code, alert and agent use cases open: their modules'
	// names, written out. cmd/server's instance keys: hashkey_test.go.
	for _, c := range []struct{ sql, ad, want string }{
		{`SELECT password_encrypted FROM repository_mirrors WHERE id = '` + f.mirror + `'`,
			crypto.AD("repository_mirrors.password_encrypted", f.mirror), "mirror-pw"},
		{`SELECT secret_encrypted FROM repository_webhooks WHERE id = '` + f.hook + `'`,
			crypto.AD("repository_webhooks.secret_encrypted", f.hook), "repo-hook"},
		{`SELECT signing_secret FROM alert_channels WHERE org_id = '` + f.org + `'`,
			crypto.AD("alert_channels.signing_secret", f.org), "alert-secret"},
		{`SELECT sealed FROM agent_approvals WHERE id = '` + f.approval + `'`,
			crypto.AD("agent_approvals.sealed", f.approval), "approval-value"},
		{`SELECT sealed FROM secret_reveals WHERE id = '` + f.reveal + `'`,
			crypto.AD("secret_reveals.sealed", f.reveal), "admin-pw"},
	} {
		var stored string
		require.NoError(t, db.QueryRow(ctx, c.sql).Scan(&stored))
		plain, err := keys.Open(c.ad, stored)
		require.NoError(t, err, c.ad)
		require.Equal(t, c.want, plain)
	}
}

// A value saved between the pass's read and its write is kept.
func TestReplace_KeepsAValueChangedMeanwhile(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	q := sqlcdb.New(db)
	seed(t, db)
	var id, before string
	require.NoError(t, db.QueryRow(ctx, `SELECT id::text, value FROM env_vars WHERE key = 'DATABASE_URL'`).Scan(&id, &before))
	n, err := q.ResealReplaceEnvVars(ctx, sqlcdb.ResealReplaceEnvVarsParams{Ids: []string{id}, OldValues: []string{"what the pass read"}, NewValues: []string{"v1.x.y"}})
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = q.ResealReplaceInstanceKeys(ctx, sqlcdb.ResealReplaceInstanceKeysParams{Ids: []string{"hmac_key"}, OldValues: []string{"stale"}, NewValues: []string{"v1.x.y"}})
	require.NoError(t, err)
	require.Zero(t, n)
	var after string
	require.NoError(t, db.QueryRow(ctx, `SELECT value FROM env_vars WHERE id = $1`, id).Scan(&after))
	require.Equal(t, before, after)
}
