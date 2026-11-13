package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A compose stack names no address until a deploy records the service it
// routed (0075); then the one it recorded, also "" for none.
func TestComposeNetworkServiceRoundTrip(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	svc, err := repo.Create(ctx, envID, service.CreateInput{Name: "umami", Type: service.TypeWeb, SourceType: service.SourceTypeDockerCompose,
		ComposeContent: "services:\n  umami:\n    image: umami\n", Port: 3000, NetworkAlias: "umami-production"})
	require.NoError(t, err)
	connectTo := func() string {
		t.Helper()
		s, err := repo.GetByID(ctx, svc.ID)
		require.NoError(t, err)
		raw, err := json.Marshal(s)
		require.NoError(t, err)
		var out struct {
			ConnectTo *service.ConnectAddress `json:"connect_to"`
		}
		require.NoError(t, json.Unmarshal(raw, &out))
		if out.ConnectTo == nil {
			return "none"
		}
		return fmt.Sprintf("%s:%d", out.ConnectTo.Host, out.ConnectTo.Port)
	}
	require.Equal(t, "none", connectTo(), "no deploy recorded one")
	require.NoError(t, repo.SetComposeNetworkService(ctx, svc.ID, "umami"))
	require.Equal(t, "umami:3000", connectTo())
	require.NoError(t, repo.SetComposeNetworkService(ctx, svc.ID, ""))
	require.Equal(t, "none", connectTo(), "its last deploy routed none")
}

// live_since is when the active deployment finished, on the lists and on
// one service alike, and an explicit null without one.
func TestLiveSince(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, orgID, projectID, envID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, &projectID, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	create := func(name string) string {
		t.Helper()
		svc, err := repo.Create(ctx, envID, service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage,
			DockerImage: "nginx", Port: 80, NetworkAlias: name})
		require.NoError(t, err)
		return svc.ID
	}
	live, idle := create("live"), create("idle")
	var workspace string
	q(`INSERT INTO services (scope, scoped_org_id, name) VALUES ('org', $1, 'shared') RETURNING id::text`, &workspace, orgID)

	finished := time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)
	for _, id := range []string{live, workspace} {
		var older, active string
		q(`INSERT INTO deployments (service_id, status, finished_at) VALUES ($1, 'running', $2) RETURNING id::text`, &older, id, finished.Add(-time.Hour))
		q(`INSERT INTO deployments (service_id, status, finished_at) VALUES ($1, 'running', $2) RETURNING id::text`, &active, id, finished)
		require.NoError(t, repo.UpdateRuntimeStatus(ctx, id, "running", active))
	}

	inEnv, err := repo.ListByEnvironment(ctx, envID)
	require.NoError(t, err)
	byID := map[string]service.Service{}
	for _, s := range inEnv {
		byID[s.ID] = s
	}
	require.Len(t, byID, 3, "the environment's two and the workspace's")
	require.NotNil(t, byID[live].LiveSince)
	require.True(t, finished.Equal(*byID[live].LiveSince), "the active deployment, not the older one")
	require.Nil(t, byID[idle].LiveSince)

	shared, err := repo.ListOrgScoped(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, shared, 1)
	require.NotNil(t, shared[0].LiveSince)
	require.True(t, finished.Equal(*shared[0].LiveSince))

	one, err := repo.GetByID(ctx, live)
	require.NoError(t, err)
	require.NotNil(t, one.LiveSince)
	require.True(t, finished.Equal(*one.LiveSince))

	none, err := repo.GetByID(ctx, idle)
	require.NoError(t, err)
	raw, err := json.Marshal(none)
	require.NoError(t, err)
	var shown map[string]any
	require.NoError(t, json.Unmarshal(raw, &shown))
	value, present := shown["live_since"]
	require.True(t, present, "null, not left out")
	require.Nil(t, value)
}

// A service's credentials sealed in v1 (D-126) open where they were
// written: created in an environment or a project, updated, and copied for a
// preview, which reseals them for the copy (and drops its migrate and
// pre-deploy commands). The start-up pass for basic auth
// passwords converts both ways.
func TestCredentialsRoundTripInV1(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID, previewEnvID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'pr-1') RETURNING id::text`, &previewEnvID, projectID)

	in := service.CreateInput{Name: "api", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "api:1",
		RegistryPassword: "registry-pw", GitToken: "git-token", BasicAuthUser: "admin", BasicAuthPassword: "basic-pw"}
	check := func(id, registry, token, basic string) {
		t.Helper()
		s, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, []string{registry, token, basic}, []string{s.RegistryPassword, s.GitToken, s.BasicAuthPassword})
	}

	svc, err := repo.Create(ctx, envID, in)
	require.NoError(t, err)
	check(svc.ID, "registry-pw", "git-token", "basic-pw")

	in.Name = "shared"
	shared, err := repo.CreateProjectScoped(ctx, projectID, in)
	require.NoError(t, err)
	check(shared.ID, "registry-pw", "git-token", "basic-pw")

	_, err = repo.UpdateConfig(ctx, svc.ID, service.UpdateConfigInput{RegistryPassword: "registry-pw-2", GitToken: "git-token-2", BasicAuthUser: "admin"})
	require.NoError(t, err)
	check(svc.ID, "registry-pw-2", "git-token-2", "basic-pw")

	_, err = db.Exec(ctx, `UPDATE services SET migrate_command = 'rails db:migrate', pre_deploy_commands = 'echo hi' WHERE id = $1`, svc.ID)
	require.NoError(t, err)

	// Moved elsewhere (credentialsMoved): cleared.
	_, err = repo.UpdateConfig(ctx, shared.ID, service.UpdateConfigInput{ClearRegistryPassword: true, ClearGitToken: true, BasicAuthUser: "admin"})
	require.NoError(t, err)
	check(shared.ID, "", "", "basic-pw")
	_, err = repo.UpdateConfig(ctx, shared.ID, service.UpdateConfigInput{RegistryPassword: "registry-pw", BasicAuthUser: "admin"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateDockerImage(ctx, shared.ID, "api:2", false))
	check(shared.ID, "registry-pw", "", "basic-pw")
	require.NoError(t, repo.UpdateDockerImage(ctx, shared.ID, "registry.example/api:2", true))
	check(shared.ID, "", "", "basic-pw")
	_, err = repo.UpdateConfig(ctx, shared.ID, service.UpdateConfigInput{RegistryPassword: "registry-pw", GitToken: "git-token", BasicAuthUser: "admin"})
	require.NoError(t, err)

	copyID, err := repo.CopyForPreview(ctx, service.PreviewCopy{OriginalID: svc.ID, EnvironmentID: previewEnvID, Branch: "feature"}, "")
	require.NoError(t, err)
	check(copyID, "registry-pw-2", "git-token-2", "basic-pw")
	// A preview never migrates (D-144), whichever environment it copies.
	var migrate, preDeploy string
	require.NoError(t, db.QueryRow(ctx, `SELECT migrate_command, pre_deploy_commands FROM services WHERE id = $1`, copyID).Scan(&migrate, &preDeploy))
	require.Equal(t, []string{"", ""}, []string{migrate, preDeploy})

	// The start-up pass: plain text from before 0019 is sealed, and the
	// rollback command opens it again.
	_, err = db.Exec(ctx, `UPDATE services SET basic_auth_password = 'plain-pw' WHERE id = $1`, shared.ID)
	require.NoError(t, err)
	n, err := repo.StoreBasicAuthPasswords(ctx, true)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	check(shared.ID, "registry-pw", "git-token", "plain-pw")
	n, err = repo.StoreBasicAuthPasswords(ctx, false)
	require.NoError(t, err)
	require.EqualValues(t, 3, n, "the rollback opens every service's")
	var stored string
	q(`SELECT basic_auth_password FROM services WHERE id = $1`, &stored, shared.ID)
	require.Equal(t, "plain-pw", stored)

	// No user turns basic auth off, and the password goes with it: a user
	// set again later never brings the old one back.
	_, err = repo.UpdateConfig(ctx, shared.ID, service.UpdateConfigInput{BasicAuthUser: ""})
	require.NoError(t, err)
	q(`SELECT basic_auth_password FROM services WHERE id = $1`, &stored, shared.ID)
	require.Empty(t, stored)
}

// The catalog Loki's 127.0.0.1 port (D-148): unique on the instance, so a
// Loki keeps its own at every deploy, a move included, and never meets it
// taken on another server.
func TestAssignLoopbackPort(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID, srvA, srvB string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO servers (user_id, name, ip_address, ssh_key) VALUES ($1, 'a', '203.0.113.10', 'k') RETURNING id::text`, &srvA, userID)
	q(`INSERT INTO servers (user_id, name, ip_address, ssh_key) VALUES ($1, 'b', '203.0.113.11', 'k') RETURNING id::text`, &srvB, userID)
	loki := func(name, serverID string) string {
		t.Helper()
		s, err := repo.Create(ctx, envID, service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/loki:3", AddonSlug: "loki"})
		require.NoError(t, err)
		if serverID != "" {
			_, err = db.Exec(ctx, `UPDATE services SET server_id = $2 WHERE id = $1`, s.ID, serverID)
			require.NoError(t, err)
		}
		return s.ID
	}
	port := func(id string) int {
		t.Helper()
		p, err := repo.AssignLoopbackPort(ctx, id)
		require.NoError(t, err)
		return p
	}
	a1, a2, b1, panel := loki("loki-a1", srvA), loki("loki-a2", srvA), loki("loki-b1", srvB), loki("loki-panel", "")
	app, err := repo.Create(ctx, envID, service.CreateInput{Name: "app", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "grafana/loki:3"})
	require.NoError(t, err)
	require.Equal(t, 0, port(app.ID), "only the catalog Loki gets one, whatever its image")

	require.Equal(t, 39100, port(a1))
	require.Equal(t, 39101, port(a2), "a second Loki on the same server")
	require.Equal(t, 39100, port(a1), "a Loki keeps its port")
	require.Equal(t, 39102, port(b1), "another server's Loki gets its own")
	require.Equal(t, 39103, port(panel), "so does the panel's")

	// Moved next to another Loki, it keeps its port: none is shared.
	_, err = repo.AssignServer(ctx, b1, srvA)
	require.NoError(t, err)
	require.Equal(t, 39102, port(b1))

	// The range is the database's rule too.
	_, err = db.Exec(ctx, `UPDATE services SET loopback_port = 8080 WHERE id = $1`, a1)
	require.Error(t, err)
	_, err = db.Exec(ctx, `UPDATE services SET loopback_port = 39101 WHERE id = $1`, a1)
	require.Error(t, err, "two services on one server never share a port")
}

// An agent server's desired state ships to the catalog Loki's 127.0.0.1
// port when Loki runs there, and keeps logs local elsewhere and for Loki
// itself; an address set wins on every server (D-148).
func TestDesiredOnShipsToLokiOnItsServer(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, orgID, projectID, envID, srvA, srvB string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`, &orgID)
	q(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'Shop') RETURNING id::text`, &projectID, userID, orgID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO servers (user_id, name, ip_address, ssh_key) VALUES ($1, 'a', '203.0.113.10', 'k') RETURNING id::text`, &srvA, userID)
	q(`INSERT INTO servers (user_id, name, ip_address, ssh_key) VALUES ($1, 'b', '203.0.113.11', 'k') RETURNING id::text`, &srvB, userID)
	on := func(name, serverID string) string {
		t.Helper()
		in := service.CreateInput{Name: name, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: name + ":1"}
		if name == "loki" {
			in.AddonSlug = "loki"
		}
		s, err := repo.Create(ctx, envID, in)
		require.NoError(t, err)
		_, err = repo.AssignServer(ctx, s.ID, serverID)
		require.NoError(t, err)
		return s.ID
	}
	loki, appA, appB := on("loki", srvA), on("app-a", srvA), on("app-b", srvB)
	_, err := db.Exec(ctx, `INSERT INTO observability_configs (org_id, loki_service_id) VALUES ($1, $2)`, orgID, loki)
	require.NoError(t, err)
	shipped := func(serverID string) map[string]string {
		t.Helper()
		ds, err := repo.DesiredOn(ctx, serverID)
		require.NoError(t, err)
		out := map[string]string{}
		for _, d := range ds {
			out[d.ID] = d.Loki
		}
		return out
	}

	require.Equal(t, map[string]string{loki: "", appA: ""}, shipped(srvA), "Loki has no port until it deploys")
	_, err = repo.AssignLoopbackPort(ctx, loki)
	require.NoError(t, err)
	require.Equal(t, map[string]string{loki: "", appA: "http://127.0.0.1:39100"}, shipped(srvA))
	require.Equal(t, map[string]string{appB: ""}, shipped(srvB))

	_, err = db.Exec(ctx, `UPDATE observability_configs SET loki_url = 'https://logs.example.com' WHERE org_id = $1`, orgID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{appB: "https://logs.example.com"}, shipped(srvB))
}

// A new service's address is stored with it, or neither is (D-150); a move
// points its sslip.io names at the new server.
func TestCreateWithAddress(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	const addr = "web-production-shop.203-0-113-10.sslip.io"
	in := service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "nginx:1", Port: 80, AddressHostname: addr}

	web, err := repo.Create(ctx, envID, in)
	require.NoError(t, err)
	var host string
	q(`SELECT hostname FROM domains WHERE service_id = $1`, &host, web.ID)
	require.Equal(t, addr, host)

	in.Name = "web2"
	_, err = repo.Create(ctx, envID, in)
	require.ErrorIs(t, err, service.ErrAddressTaken)
	var n string
	q(`SELECT count(*)::text FROM services WHERE name = 'web2'`, &n)
	require.Equal(t, "0", n, "no service without its address")

	_, err = db.Exec(ctx, `UPDATE domains SET routed_at = NOW(), certificate_at = NOW() WHERE service_id = $1`, web.ID)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `INSERT INTO domains (service_id, hostname) VALUES ($1, 'mine.192-0-2-1.sslip.io')`, web.ID)
	require.NoError(t, err)
	renamed, err := repo.RenameTemporaryAddresses(ctx, web.ID, "203.0.113.10", "198.51.100.7")
	require.NoError(t, err)
	require.EqualValues(t, 1, renamed, "only the name under the old server's address")
	var routed, cert *string
	require.NoError(t, db.QueryRow(ctx, `SELECT hostname, routed_at::text, certificate_at::text FROM domains WHERE service_id = $1 AND hostname <> 'mine.192-0-2-1.sslip.io'`, web.ID).Scan(&host, &routed, &cert))
	require.Equal(t, "web-production-shop.198-51-100-7.sslip.io", host)
	require.NotNil(t, routed, "the move's deploy routed the new name")
	require.Nil(t, cert, "which waits for its certificate again")
	renamed, err = repo.RenameTemporaryAddresses(ctx, web.ID, "203.0.113.10", "198.51.100.7")
	require.NoError(t, err)
	require.Zero(t, renamed, "already there")
}

// seqAddresser names each address in turn, as preview.Service would once
// the one before is taken.
type seqAddresser struct{ names []string }

func (a *seqAddresser) Address(context.Context, string, string, string) (string, error) {
	n := a.names[0]
	a.names = a.names[1:]
	return n, nil
}
func (a *seqAddresser) ServerAddress(context.Context, string) (string, error) { return "", nil }
func (a *seqAddresser) AddressIn(ctx context.Context, _, _, serverID, name string) (string, error) {
	return a.Address(ctx, "", serverID, name)
}

// A name taken between naming and creating: the create names the next one
// instead of failing (D-150).
func TestCreateNamesTheNextAddressWhenTaken(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	var userID, projectID, envID, otherID string
	for _, q := range []struct {
		sql  string
		dst  *string
		args []string
	}{
		{`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID, nil},
		{`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, []string{"user"}},
		{`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, []string{"project"}},
		{`INSERT INTO services (environment_id, name, type, source_type, port) VALUES ($1, 'other', 'web', 'docker-image', 80) RETURNING id::text`, &otherID, []string{"env"}},
	} {
		args := make([]any, 0, len(q.args))
		for _, a := range q.args {
			args = append(args, map[string]string{"user": userID, "project": projectID, "env": envID}[a])
		}
		require.NoError(t, db.QueryRow(ctx, q.sql, args...).Scan(q.dst))
	}
	_, err := db.Exec(ctx, `INSERT INTO domains (service_id, hostname) VALUES ($1, 'web-production-shop.203-0-113-10.sslip.io')`, otherID)
	require.NoError(t, err)

	m := service.NewServiceManager(repo, zap.NewNop())
	m.WithAddresser(&seqAddresser{names: []string{"web-production-shop.203-0-113-10.sslip.io", "web-production-shop-2.203-0-113-10.sslip.io"}})
	saved, err := m.Create(ctx, envID, "", service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage,
		DockerImage: "nginx:1", Port: 80, Address: true})
	require.NoError(t, err)
	var host string
	require.NoError(t, db.QueryRow(ctx, `SELECT hostname FROM domains WHERE service_id = $1`, saved.ID).Scan(&host))
	require.Equal(t, "web-production-shop-2.203-0-113-10.sslip.io", host)
}

// ListData names what deleting services removes: each service, then its
// preview copies (D-97), by name; only named volumes count, not bind mounts.
func TestListDataTakesPreviewCopies(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID, previewEnv string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'pr-42') RETURNING id::text`, &previewEnv, projectID)
	in := service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "nginx:1", Port: 80,
		Volumes: []service.VolumeMount{{Name: "data", Target: "/data"}}}
	web, err := repo.Create(ctx, envID, in)
	require.NoError(t, err)
	in.Name = "api"
	api, err := repo.Create(ctx, envID, in)
	require.NoError(t, err)
	in.Name = "web"
	copyOf, err := repo.Create(ctx, previewEnv, in)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE services SET preview_of = $1 WHERE id = $2`, web.ID, copyOf.ID)
	require.NoError(t, err)

	rows, err := repo.ListData(ctx, []string{web.ID, api.ID})
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	require.Equal(t, []string{api.ID, web.ID, copyOf.ID}, ids, "by name, each copy after its service")
	require.Equal(t, []service.VolumeMount{{Name: "data", Target: "/data"}}, rows[2].Volumes)

	rows, err = repo.ListData(ctx, []string{web.ID})
	require.NoError(t, err)
	require.Len(t, rows, 2, "a service alone still brings its copy")
}

// metricsService creates a service to store samples for.
func metricsService(t *testing.T) (context.Context, *service.PostgresRepository, *pgxpool.Pool, string) {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	var userID, projectID, envID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, userID).Scan(&projectID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, projectID).Scan(&envID))
	svc, err := repo.Create(ctx, envID, service.CreateInput{Name: "stack", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "api:1"})
	require.NoError(t, err)
	return ctx, repo, db, svc.ID
}

// range=7d: the 6 days before today (not the 7th, not a kill-only row, not
// today's row, which the hourly summary leaves behind) from the summed
// samples' daily rows, or, for a day summarised before them, the per
// compose service rows summed and marked largest_container; today from
// today's samples summed at each minute, as 1h and 24h draw them: the
// memory peak and CPU p95 of the sums; the throttled share is the highest.
func TestListDailyMetrics(t *testing.T) {
	ctx, repo, db, id := metricsService(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := db.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}
	today := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	usage := `INSERT INTO service_usage_daily (service_id, compose_service, day, samples, memory_peak_mb, memory_p95_mb, cpu_p95_percent, throttled_max_pct)
		VALUES ($1, $2, $3, $4, $5, $5, $6, $7)`
	for i := 1; i <= 7; i++ {
		exec(usage, id, "web", today.AddDate(0, 0, -i), 1440, 100+i, i, i)
	}
	exec(usage, id, "db", today.AddDate(0, 0, -1), 1440, 50, 0.5, nil)
	exec(usage, id, "db", today.AddDate(0, 0, -2), 0, 0, 0, nil) // a kill-only day
	exec(usage, id, "web", today, 30, 9999, 9999, nil)           // today's, an hour old
	sums := `INSERT INTO service_usage_daily_sums (service_id, compose_service, day, memory_peak_mb, cpu_p95_percent) VALUES ($1, $2, $3, $4, $5)`
	exec(sums, id, "", today.AddDate(0, 0, -1), 140, 1.2) // the stack's summed day
	exec(sums, id, "", today, 9999, 9999)                 // today's, an hour old

	sample := func(compose string, at time.Time, cpu, mem float64, peak, throttled *float64) {
		t.Helper()
		require.NoError(t, repo.InsertSample(ctx, id, compose, at, service.MetricSample{CPUPercent: cpu, MemoryMB: mem, MemoryPeakMB: peak, ThrottledPct: throttled}))
	}
	peak, thr := 150.0, 30.0
	sample("web", today.Add(-time.Minute), 1000, 5000, nil, nil) // yesterday's
	for i := 1; i <= 20; i++ {
		var p, th *float64
		if i == 7 {
			p, th = &peak, &thr
		}
		sample("web", today.Add(time.Duration(i)*time.Minute), float64(i), 120, p, th)
	}
	sample("db", today.Add(time.Minute), 4, 60, nil, nil) // no peak read: its memory

	type point struct {
		day                 string
		cpu, mem, throttled float64
		largest             bool
	}
	read := func(compose string) []point {
		t.Helper()
		got, err := repo.ListDailyMetrics(ctx, id, compose, today)
		require.NoError(t, err)
		out := make([]point, 0, len(got))
		for _, p := range got {
			require.Equal(t, p.Timestamp.UTC(), p.Timestamp.UTC().Truncate(24*time.Hour), "a day's start")
			out = append(out, point{p.Timestamp.UTC().Format(time.DateOnly), p.CPUPercent, p.MemoryMB, p.ThrottledPct, p.LargestContainer})
		}
		return out
	}

	all := read("")
	require.Len(t, all, 7)
	for i, p := range all[:5] { // 10-29 … 11-02: web alone
		back := 6 - i
		require.Equal(t, point{today.AddDate(0, 0, -back).Format(time.DateOnly), float64(back), float64(100 + back), float64(back), true}, p, "summarised before the sums")
	}
	require.Equal(t, point{"2026-11-03", 1.2, 140, 1, false}, all[5], "the summed day; the highest throttled of the rows")
	require.Equal(t, "2026-11-04", all[6].day)
	require.False(t, all[6].largest)
	require.InDelta(t, 19.05, all[6].cpu, 0.001, "the p95 of 1+4, 2 … 20")
	require.Equal(t, []float64{180, 30}, []float64{all[6].mem, all[6].throttled}, "web and db summed at minute 1, not web's container peak; the highest throttled")

	require.Equal(t, []point{{"2026-11-03", 0.5, 50, 0, true}, {"2026-11-04", 4, 60, 0, false}}, read("db"), "one compose service's: no sums row for db that day")
}

// The summariser keeps the peak and CPU p95 of the summed samples, per
// compose service and for the whole stack, so 7d reads a day as 1h and 24h
// draw its minutes; a single-container service's rows count once; the days
// are UTC on a database in another time zone; reruns overwrite and the
// usage GC deletes the rows.
func TestSummariseUsageSums(t *testing.T) {
	ctx, repo, db, id := metricsService(t)
	res := resources.NewPostgresRepository(db)
	var dbName, envID string
	require.NoError(t, db.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName))
	_, err := db.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{dbName}.Sanitize()+` SET TimeZone TO 'Pacific/Auckland'`)
	require.NoError(t, err)
	db.Reset() // new connections take the time zone
	require.NoError(t, db.QueryRow(ctx, `SELECT environment_id::text FROM services WHERE id = $1`, id).Scan(&envID))
	single, err := repo.Create(ctx, envID, service.CreateInput{Name: "single", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "api:1"})
	require.NoError(t, err)
	today := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	sample := func(compose string, m int, cpu, mem float64, peak *float64) {
		t.Helper()
		require.NoError(t, repo.InsertSample(ctx, id, compose, yesterday.Add(time.Duration(m)*time.Minute), service.MetricSample{CPUPercent: cpu, MemoryMB: mem, MemoryPeakMB: peak}))
	}
	replica := 60.0
	sample("web", 0, 10, 100, &replica) // two replicas, the largest 60
	sample("db", 0, 1, 20, nil)
	sample("web", 1, 2, 50, nil)
	sample("db", 1, 30, 80, nil)
	for m, mem := range map[int]float64{-1: 999, 0: 10, 1: 30, 24 * 60: 500} { // the day before's last minute, yesterday's, today's first
		require.NoError(t, repo.InsertSample(ctx, single.ID, "", yesterday.Add(time.Duration(m)*time.Minute), service.MetricSample{CPUPercent: mem / 10, MemoryMB: mem}))
	}

	for range 2 {
		require.NoError(t, res.SummariseUsage(ctx, yesterday, today))
	}
	read := func(compose string) service.MetricPoint {
		t.Helper()
		got, err := repo.ListDailyMetrics(ctx, id, compose, today)
		require.NoError(t, err)
		require.Len(t, got, 1, "yesterday; no samples today")
		require.False(t, got[0].LargestContainer)
		return got[0]
	}
	all := read("")
	require.Equal(t, 130.0, all.MemoryMB, "minute 1's 50+80, not 60+80 or 100+80")
	require.InDelta(t, 30.95, all.CPUPercent, 0.001, "the p95 of 11 and 32")
	web := read("web")
	require.Equal(t, 100.0, web.MemoryMB, "web's containers summed")
	require.InDelta(t, 9.6, web.CPUPercent, 0.001)

	type sum struct {
		Day      string
		Mem, CPU float64
	}
	rows, err := db.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD'), memory_peak_mb::float8, cpu_p95_percent::float8
		FROM service_usage_daily_sums WHERE service_id = $1 ORDER BY compose_service, day`, single.ID)
	require.NoError(t, err)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[sum])
	require.NoError(t, err)
	require.Len(t, got, 1, "only yesterday's UTC minutes, one '' row: %v", got)
	require.Equal(t, "2026-11-03", got[0].Day)
	require.Equal(t, 30.0, got[0].Mem)
	require.InDelta(t, 2.9, got[0].CPU, 0.001, "the p95 of 1 and 3, each minute counted once")

	require.NoError(t, res.DeleteUsageBefore(ctx, today))
	var n int
	require.NoError(t, db.QueryRow(ctx, `SELECT COUNT(*) FROM service_usage_daily_sums`).Scan(&n))
	require.Zero(t, n)
}

// The daily usage rows (the rightsizing advisor's) cut days in UTC on a
// database in another time zone: the day before's last minute and a kill
// in it stay out of yesterday's row, and yesterday's evening stays in.
func TestSummariseUsageDaysInUTC(t *testing.T) {
	ctx, repo, db, id := metricsService(t)
	res := resources.NewPostgresRepository(db)
	var dbName string
	require.NoError(t, db.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName))
	_, err := db.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{dbName}.Sanitize()+` SET TimeZone TO 'Pacific/Auckland'`)
	require.NoError(t, err)
	db.Reset() // new connections take the time zone
	today := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	for m, mem := range map[int]float64{-1: 999, 0: 10, 18 * 60: 30} {
		require.NoError(t, repo.InsertSample(ctx, id, "", yesterday.Add(time.Duration(m)*time.Minute), service.MetricSample{CPUPercent: 1, MemoryMB: mem}))
	}
	_, err = res.InsertOOMKill(ctx, id, "", "c1", yesterday.Add(-30*time.Minute))
	require.NoError(t, err)

	require.NoError(t, res.SummariseUsage(ctx, yesterday, today))
	type day struct {
		Day     string
		Samples int
		Peak    float64
		Kills   int
	}
	rows, err := db.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD'), samples, memory_peak_mb::float8, oom_kills
		FROM service_usage_daily WHERE service_id = $1 ORDER BY day`, id)
	require.NoError(t, err)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[day])
	require.NoError(t, err)
	require.Equal(t, []day{{"2026-11-03", 2, 30, 0}}, got)
}

// The resources' restarts from a time on, per compose service: the sum of
// the samples' restarts that carry a count (an old agent's NULLs are
// skipped), when the counted ones start, and whether one before the time
// carries a count, so they cover the window.
func TestRestartsSinceByComposeService(t *testing.T) {
	ctx, repo, db, id := metricsService(t)
	res := resources.NewPostgresRepository(db)
	since := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	sample := func(compose string, m int, restarts *int) {
		t.Helper()
		require.NoError(t, repo.InsertSample(ctx, id, compose, since.Add(time.Duration(m)*time.Minute), service.MetricSample{Restarts: restarts}))
	}
	n := func(v int) *int { return &v }

	got, err := res.RestartsSince(ctx, id, since)
	require.NoError(t, err)
	require.Empty(t, got, "no samples")

	sample("web", -5, nil) // before since, from an old agent
	for i, c := range []*int{nil, nil, n(0), n(2), nil, n(1)} {
		sample("web", i+1, c)
	}
	sample("db", 2, n(0))
	sample("db", 3, n(0))
	sample("db", -1, n(4)) // before since: not counted, but the window is covered
	sample("cache", 1, nil)

	got, err = res.RestartsSince(ctx, id, since)
	require.NoError(t, err)
	require.Len(t, got, 2, "cache has no counted sample")
	web, db2 := got["web"], got["db"]
	require.Equal(t, []int{3, 3}, []int{web.Samples, web.Restarts})
	require.True(t, since.Add(3*time.Minute).Equal(web.FirstAt), "the first sample with a count: %s", web.FirstAt)
	require.False(t, web.Earlier, "web's sample before since has no count")
	require.Equal(t, []int{2, 0}, []int{db2.Samples, db2.Restarts})
	require.True(t, db2.Earlier)
}

// The container check is created, stored and read back, and the table
// refuses what the service layer refuses (0081).
func TestContainerHealthRoundTrip(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := service.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	q := func(sql string, dst *string, args ...any) {
		t.Helper()
		require.NoError(t, db.QueryRow(ctx, sql, args...).Scan(dst))
	}
	var userID, projectID, envID string
	q(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`, &userID)
	q(`INSERT INTO projects (user_id, name) VALUES ($1, 'Shop') RETURNING id::text`, &projectID, userID)
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, &envID, projectID)

	web, err := repo.Create(ctx, envID, service.CreateInput{Name: "web", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "nginx", Port: 80})
	require.NoError(t, err)
	mode, command, err := repo.ContainerHealth(ctx, web.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"image", ""}, []string{mode, command}, "the default")

	worker, err := repo.Create(ctx, envID, service.CreateInput{Name: "worker", Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: "acme/api", Port: 80,
		ContainerHealth: service.ContainerHealthCommand, ContainerHealthCommand: "pgrep -f worker"})
	require.NoError(t, err)
	mode, command, err = repo.ContainerHealth(ctx, worker.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"command", "pgrep -f worker"}, []string{mode, command})

	require.NoError(t, repo.SetContainerHealth(ctx, worker.ID, service.ContainerHealthOff, ""))
	mode, _, err = repo.ContainerHealth(ctx, worker.ID)
	require.NoError(t, err)
	require.Equal(t, "off", mode)

	// A preview runs its containers with the original's check.
	var previewEnvID string
	q(`INSERT INTO environments (project_id, name) VALUES ($1, 'pr-1') RETURNING id::text`, &previewEnvID, projectID)
	require.NoError(t, repo.SetContainerHealth(ctx, worker.ID, service.ContainerHealthCommand, "pgrep -f api.worker"))
	copyID, err := repo.CopyForPreview(ctx, service.PreviewCopy{OriginalID: worker.ID, EnvironmentID: previewEnvID, Branch: "feature"}, "")
	require.NoError(t, err)
	mode, command, err = repo.ContainerHealth(ctx, copyID)
	require.NoError(t, err)
	require.Equal(t, []string{"command", "pgrep -f api.worker"}, []string{mode, command})
	require.NoError(t, repo.SetContainerHealth(ctx, worker.ID, service.ContainerHealthOff, ""))

	for name, in := range map[string][2]string{
		"unknown mode":         {"none", ""},
		"command without one":  {"command", ""},
		"a newline":            {"command", "true\nfalse"},
		"a carriage return":    {"command", "true\r"},
		"over 1024 characters": {"command", strings.Repeat("x", 1025)},
	} {
		require.Error(t, repo.SetContainerHealth(ctx, worker.ID, in[0], in[1]), name)
	}
	mode, _, err = repo.ContainerHealth(ctx, worker.ID)
	require.NoError(t, err)
	require.Equal(t, "off", mode, "a refused write changes nothing")
}
