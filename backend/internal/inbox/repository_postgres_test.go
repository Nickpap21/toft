package inbox_test

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"go.uber.org/zap"
)

// UnprotectedPublic: image services with an address and neither basic
// auth nor an IP allowlist, a project's and the workspace's own; not a
// protected one, one without an address, a build, a preview copy, or
// another workspace's.
func TestUnprotectedPublic(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	row := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	user := row(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	org := row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	other := row(`INSERT INTO organizations (name) VALUES ('Other') RETURNING id::text`)
	project := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, user, org)
	otherProject := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'theirs') RETURNING id::text`, user, other)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, project)
	otherEnv := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, otherProject)
	svc := func(name, envID, extra string) string {
		id := row(`INSERT INTO services (environment_id, name, source_type, docker_image) VALUES ($1, $2, 'docker-image', 'phpmyadmin') RETURNING id::text`, envID, name)
		if extra != "" {
			if _, err := db.Exec(ctx, `UPDATE services SET `+extra+` WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	domain := func(serviceID, host string) {
		if _, err := db.Exec(ctx, `INSERT INTO domains (service_id, hostname) VALUES ($1, $2)`, serviceID, host); err != nil {
			t.Fatal(err)
		}
	}
	open := svc("open", env, "")
	domain(open, "pma.example.com")
	domain(svc("auth", env, "basic_auth_user = 'ops'"), "auth.example.com")
	domain(svc("allow", env, `ip_allowlist = '["203.0.113.7/32"]'`), "allow.example.com")
	svc("internal", env, "")
	domain(svc("build", env, "source_type = 'git'"), "build.example.com")
	copyOf := svc("open-pr", env, "preview_of = '"+open+"'")
	domain(copyOf, "pr.example.com")
	domain(svc("theirs", otherEnv, ""), "theirs.example.com")
	ws := row(`INSERT INTO services (scope, scoped_org_id, name, source_type, docker_image) VALUES ('org', $1, 'mail', 'docker-image', 'mailhog/mailhog') RETURNING id::text`, org)
	domain(ws, "mail.example.com")

	got, err := inbox.NewPostgresRepository(db).UnprotectedPublic(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, p := range got {
		names = append(names, p.Name+" "+p.Hostname+" "+p.Environment+" "+p.Image)
	}
	sort.Strings(names)
	want := []string{"mail mail.example.com  mailhog/mailhog", "open pma.example.com staging phpmyadmin"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("got %q, want %q", names, want)
	}
}

// A server nothing backs up, with a database or a volume on it, needs an
// owner or developer: not one with a destination, nor one whose machine
// another entry backs up, nor one with no data; the panel host by its own
// destination. Another workspace's services and preview copies don't count.
func TestNoBackupNeedsYou(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	row := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	user := row(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	org := row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	other := row(`INSERT INTO organizations (name) VALUES ('Other') RETURNING id::text`)
	project := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, user, org)
	otherProject := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'theirs') RETURNING id::text`, user, other)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, project)
	otherEnv := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, otherProject)
	server := func(orgID, name, ip string) string {
		return row(`INSERT INTO servers (user_id, org_id, name, ip_address, ssh_key) VALUES ($1, $2, $3, $4, 'k') RETURNING id::text`, user, orgID, name, ip)
	}
	backup := func(serverID any) {
		row(`INSERT INTO backup_destinations (server_id, endpoint, bucket, access_key, secret_key, repo_password)
			VALUES ($1, 'https://s3.example.com', 'toft-backups', 'ak', 'sk', 'pw') RETURNING id::text`, serverID)
	}
	svc := func(envID string, serverID any, image, volumes string) string {
		return row(`INSERT INTO services (environment_id, server_id, name, source_type, docker_image, volumes)
			VALUES ($1, $2, 'svc-' || substr(md5(random()::text), 1, 8), 'docker-image', $3, $4) RETURNING id::text`, envID, serverID, image, volumes)
	}
	vol := `[{"name":"data","target":"/data"}]`

	bare := server(org, "fsn1", "203.0.113.1")
	svc(env, bare, "postgres:16", vol)
	web := svc(env, bare, "ghcr.io/acme/web", `[{"name":"uploads","target":"/u"},{"name":"bind","target":"/b","host_path":"/srv/b"}]`)
	shared := row(`INSERT INTO shared_volumes (org_id, server_id, name, size_limit_bytes) VALUES ($1, $2, 'media', 1073741824) RETURNING id::text`, org, bare)
	row(`INSERT INTO service_shared_volumes (service_id, shared_volume_id, target) VALUES ($1, $2, '/media') RETURNING service_id::text`, web, shared)
	row(`UPDATE services SET preview_of = $1 WHERE id = $2 RETURNING id::text`, web, svc(env, bare, "mysql:8", vol))
	svc(otherEnv, bare, "mongo:7", vol)

	backed := server(org, "hel1", "203.0.113.2")
	backup(backed)
	svc(env, backed, "postgres:16", vol)

	// The same machine as another workspace's entry, which backs it up.
	twin := server(org, "ams1", "203.0.113.3")
	backup(server(other, "ams1-theirs", " 203.0.113.3 "))
	svc(env, twin, "mysql:8", vol)

	empty := server(org, "par1", "203.0.113.4")
	svc(env, empty, "nginx:alpine", "[]")
	svc(env, empty, "ghcr.io/acme/api", "null")

	svc(env, nil, "redis:7", vol) // the panel host

	// Compose stacks: one inline, with a database and a volume; one whose
	// file is in a repository.
	stacks := server(org, "lon1", "203.0.113.5")
	row(`INSERT INTO services (environment_id, server_id, name, source_type, compose_content) VALUES ($1, $2, 'blog', 'docker-compose', $3) RETURNING id::text`,
		env, stacks, "services:\n  app:\n    image: ghost:5\n    volumes: [content:/data]\n  db:\n    image: mysql:8\nvolumes:\n  content:\n")
	row(`INSERT INTO services (environment_id, server_id, name, source_type, compose_repo_url) VALUES ($1, $2, 'kuma', 'docker-compose', 'https://github.com/acme/kuma.git') RETURNING id::text`,
		env, stacks)

	repo := inbox.NewPostgresRepository(db)
	needs := func() []string {
		t.Helper()
		items, err := inbox.NewService(repo, nil, nil, nil, zap.NewNop()).NeedsYou(ctx, org, user, "owner", inbox.MarksOpen)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, it := range items {
			if it.Kind == "no_backup" {
				out = append(out, it.ServerID+" "+it.Title+": "+it.Detail)
			}
		}
		sort.Strings(out)
		return out
	}
	want := []string{" The panel host: 1 volume, no backups", bare + " fsn1: 1 database, 3 volumes, no backups",
		stacks + " lon1: 1 database, 1 volume, 1 compose stack, no backups"}
	sort.Strings(want)
	if got := needs(); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	viewer, err := inbox.NewService(repo, nil, nil, nil, zap.NewNop()).NeedsYou(ctx, org, user, "viewer", inbox.MarksOpen)
	if err != nil || len(viewer) != 0 {
		t.Errorf("a viewer's: %v %+v", err, viewer)
	}

	backup(nil)
	backup(bare)
	backup(stacks)
	if got := needs(); len(got) != 0 {
		t.Errorf("with destinations: %q", got)
	}
}

// DoneKeys says when each key was marked: a condition's snooze runs from it.
func TestDoneKeysTime(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	var user string
	if err := db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	repo := inbox.NewPostgresRepository(db)
	if err := repo.MarkDone(ctx, user, []string{"no_backup:panel"}, true); err != nil {
		t.Fatal(err)
	}
	marked, err := repo.DoneKeys(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := marked["no_backup:panel"]; !ok || time.Since(at).Abs() > time.Minute {
		t.Errorf("got %v %v, want about now", at, ok)
	}
}

// TemporaryLogins: the workspace's services made from one of the slugs
// with Toft's temporary login still on, a project's and the workspace's
// own; not one whose basic auth is off or the owner's own, another app, a preview copy, or another
// workspace's.
func TestTemporaryLoginsQuery(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	row := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	user := row(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	org := row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	other := row(`INSERT INTO organizations (name) VALUES ('Other') RETURNING id::text`)
	project := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, user, org)
	otherProject := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'theirs') RETURNING id::text`, user, other)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, project)
	otherEnv := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, otherProject)
	svc := func(name, envID, slug, auth string) string {
		return row(`INSERT INTO services (environment_id, name, source_type, addon_slug, basic_auth_user) VALUES ($1, $2, 'docker-compose', $3, $4) RETURNING id::text`,
			envID, name, slug, auth)
	}
	blog := svc("blog", env, "ghost", "toft")
	svc("off", env, "ghost", "")
	svc("own", env, "ghost", "ops")
	svc("stats", env, "umami", "toft")
	row(`INSERT INTO services (environment_id, name, source_type, addon_slug, basic_auth_user, preview_of) VALUES ($1, 'blog-pr', 'docker-compose', 'ghost', 'toft', $2) RETURNING id::text`, env, blog)
	svc("theirs", otherEnv, "ghost", "toft")
	row(`INSERT INTO services (scope, scoped_org_id, name, source_type, addon_slug, basic_auth_user) VALUES ('org', $1, 'flows', 'docker-compose', 'n8n', 'toft') RETURNING id::text`, org)

	got, err := inbox.NewPostgresRepository(db).TemporaryLogins(ctx, org, []string{"ghost", "n8n"})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, p := range got {
		names = append(names, p.Name+" "+p.AddonSlug+" "+p.Project+" "+p.Environment)
	}
	want := []string{"blog ghost shop production", "flows n8n  "}
	if !slices.Equal(names, want) {
		t.Fatalf("got %q, want %q", names, want)
	}
}

// HTTPSRedeploys (D-159): a running service whose temporary name's
// certificate was recorded after its live deployment started; not one
// deployed since, a custom domain, a name still pending, one being
// redeployed, a stopped service, a preview's copy or another workspace's;
// one behind basic auth says its address redirects already.
func TestHTTPSRedeploys(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	row := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	user := row(`INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id::text`)
	org := row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	other := row(`INSERT INTO organizations (name) VALUES ('Other') RETURNING id::text`)
	project := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, user, org)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, project)
	theirProject := row(`INSERT INTO projects (user_id, org_id, name) VALUES ($1, $2, 'x') RETURNING id::text`, user, other)
	theirs := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'staging') RETURNING id::text`, theirProject)
	// svc is a running service in envID whose live deployment started
	// liveAgo ago, with hostname certified certAgo ago ("" pending).
	svc := func(name, envID, host, liveAgo, certAgo string) string {
		id := row(`INSERT INTO services (environment_id, name, source_type, docker_image, runtime_status) VALUES ($1, $2, 'docker-image', 'nginx', 'running') RETURNING id::text`, envID, name)
		dep := row(`INSERT INTO deployments (service_id, status, created_at) VALUES ($1, 'running', NOW() - $2::interval) RETURNING id::text`, id, liveAgo)
		exec(`UPDATE services SET active_deployment_id = $1 WHERE id = $2`, dep, id)
		if certAgo == "" {
			exec(`INSERT INTO domains (service_id, hostname) VALUES ($1, $2)`, id, host)
		} else {
			exec(`INSERT INTO domains (service_id, hostname, certificate_at) VALUES ($1, $2, NOW() - $3::interval)`, id, host, certAgo)
		}
		return id
	}
	web := svc("web", env, "web.203-0-113-10.sslip.io", "2 hours", "1 hour")
	svc("fresh", env, "fresh.203-0-113-10.sslip.io", "10 minutes", "1 hour")
	svc("custom", env, "shop.example.com", "2 hours", "1 hour")
	svc("pending", env, "pending.203-0-113-10.sslip.io", "2 hours", "")
	again := svc("again", env, "again.203-0-113-10.sslip.io", "2 hours", "1 hour")
	exec(`INSERT INTO deployments (service_id, status) VALUES ($1, 'building')`, again)
	stopped := svc("stopped", env, "stopped.203-0-113-10.sslip.io", "2 hours", "1 hour")
	exec(`UPDATE services SET runtime_status = 'stopped' WHERE id = $1`, stopped)
	copyOf := svc("web-pr", env, "pr.203-0-113-10.sslip.io", "2 hours", "1 hour")
	exec(`UPDATE services SET preview_of = $1 WHERE id = $2`, web, copyOf)
	svc("theirs", theirs, "theirs.203-0-113-10.sslip.io", "2 hours", "1 hour")
	kuma := svc("kuma", env, "kuma.203-0-113-10.sslip.io", "2 hours", "1 hour")
	exec(`UPDATE services SET basic_auth_user = 'toft', basic_auth_password = 'sealed' WHERE id = $1`, kuma)

	got, err := inbox.NewPostgresRepository(db).HTTPSRedeploys(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want kuma and web, got %+v", got)
	}
	// Behind basic auth its HTTP redirected all along: only TOFT_URL waits.
	if k := got[0]; k.ServiceID != kuma || k.Title != "Redeploy kuma to give it its HTTPS address" ||
		k.Detail != "kuma.203-0-113-10.sslip.io · staging: its certificate is out and its address redirects to HTTPS already; TOFT_URL says http:// until its next deploy" {
		t.Fatalf("got %+v", k)
	}
	it := got[1]
	if it.ServiceID != web || it.Kind != "https_redeploy" || it.Title != "Redeploy web to switch it to HTTPS" ||
		it.Detail != "web.203-0-113-10.sslip.io · staging: its certificate is out, and it serves plain HTTP until its next deploy" ||
		it.Project != "shop" || it.ProjectID == nil || *it.ProjectID != project {
		t.Fatalf("got %+v", it)
	}
}
