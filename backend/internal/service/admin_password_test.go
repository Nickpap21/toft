package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/reveal"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// resetOrch records the commands a Reset runs, in order, and answers each
// with out, exit and err.
type resetOrch struct {
	mu   sync.Mutex
	ran  []service.ContainerCommand
	out  func(stdin string) string
	exit int
	err  error
}

func (o *resetOrch) ExecStdin(_ context.Context, _, _ string, cmd service.ContainerCommand) (service.ExecResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ran = append(o.ran, cmd)
	out := ""
	if o.out != nil {
		out = o.out(cmd.Stdin)
	}
	return service.ExecResult{Output: out, ExitCode: o.exit}, o.err
}
func (*resetOrch) Stop(context.Context, string) error                        { return nil }
func (*resetOrch) StopCompose(context.Context, string, string, string) error { return nil }
func (*resetOrch) StreamLogs(context.Context, string, int, string, func(string)) error {
	return nil
}
func (*resetOrch) ExecInContainer(context.Context, string, string, string, func(string)) error {
	return nil
}
func (*resetOrch) ListContainers(context.Context, string, string) ([]string, error) { return nil, nil }

type resetFactory struct{ orch *resetOrch }

func (f resetFactory) ForServer(*serverPkg.Server) (service.ContainerOrchestrator, func(), error) {
	return f.orch, func() {}, nil
}

type noServers struct{}

func (noServers) GetServer(context.Context, string) (*serverPkg.Server, error) {
	return nil, errors.New("no servers here")
}

// Reset (D-160): the secret replaced and the copy kept for whoever reset
// it, everyone's older copy dropped, the app's command run with the
// password on stdin only. A refusal, a command that never ran, or a copy
// that can't be kept changes nothing and never repeats the password; an
// unanswered command keeps the new one; two at once run one after the
// other; nothing happens for a stopped app or one without Reset.
func TestResetAdminPassword(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	var orgID, alex, sam, envID string
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Founders') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('alex@toft.test', 'Alex', 'x') RETURNING id::text`).Scan(&alex))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('sam@toft.test', 'Sam', 'x') RETURNING id::text`).Scan(&sam))
	require.NoError(t, db.QueryRow(ctx, `WITH p AS (INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id)
		INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id::text`, orgID, alex).Scan(&envID))

	reveals := reveal.NewPostgresRepository(db, keys)
	vars := envvar.NewService(envvar.NewPostgresRepository(db, keys), zap.NewNop())
	vars.WithReveals(reveals)
	orch := &resetOrch{}
	m := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	m.WithVariables(vars)
	m.WithTransactor(dbtx.NewRunner(db))
	m.WithReveals(reveals)
	m.WithOrchestration(noServers{}, resetFactory{orch})

	create := func(slug, image string, variables ...service.KeyValue) *service.SavedService {
		t.Helper()
		tmpl := mustTemplate(t, slug)
		in := service.CreateInput{Name: slug, Type: service.TypeWeb, SourceType: service.SourceTypeDockerImage, DockerImage: image, Port: tmpl.DefaultPort, AddonSlug: slug}
		if tmpl.ComposeContent != "" {
			in.SourceType, in.DockerImage, in.ComposeContent = service.SourceTypeDockerCompose, "", tmpl.ComposeContent
		}
		saved, err := m.Create(ctx, envID, alex, in)
		require.NoError(t, err)
		for _, v := range variables {
			_, err := vars.Upsert(ctx, saved.ID, alex, envvar.UpsertInput{Key: v.Key, Value: v.Value})
			require.NoError(t, err)
		}
		_, err = db.Exec(ctx, `UPDATE services SET runtime_status = 'running' WHERE id = $1`, saved.ID)
		require.NoError(t, err)
		return saved
	}
	secret := func(serviceID, key string) string {
		t.Helper()
		all, err := vars.ListAllDecrypted(ctx, serviceID)
		require.NoError(t, err)
		for _, v := range all {
			if v.Key == key {
				return v.Value
			}
		}
		return ""
	}
	kept := func(serviceID, userID string) *reveal.Reveal {
		t.Helper()
		r, err := reveals.Get(ctx, serviceID, userID, time.Now().Add(-reveal.Keep))
		require.NoError(t, err)
		return r
	}

	grafana := create("grafana", "grafana/grafana:10.4.2", service.KeyValue{Key: "GF_SECURITY_ADMIN_USER", Value: "boss"})
	login, err := m.AdminLogin(ctx, grafana.ID)
	require.NoError(t, err)
	require.Equal(t, service.AdminLogin{User: "boss", Reset: "app"}, login)
	require.NotNil(t, kept(grafana.ID, alex), "the creator's copy")

	// Sam resets: Alex's copy of the old password goes, Sam's is kept.
	r, err := m.ResetAdminPassword(ctx, sam, grafana.ID)
	require.NoError(t, err)
	require.Equal(t, "boss", r.User)
	require.False(t, r.Deploy)
	require.Equal(t, r.Password, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))
	require.Nil(t, kept(grafana.ID, alex))
	got := kept(grafana.ID, sam)
	require.Equal(t, r.Password, got.Password)
	require.Equal(t, "reset", got.Reason)
	require.Len(t, orch.ran, 1)
	ran := orch.ran[0]
	require.Equal(t, r.Password+"\n", ran.Stdin)
	require.NotContains(t, strings.Join(append(ran.Argv, ran.Env...), " "), r.Password)

	// The app refuses: its message without the password, and nothing
	// changed (the secret, Sam's copy).
	orch.out, orch.exit = func(stdin string) string { return "bad password " + strings.TrimSpace(stdin) }, 1
	_, err = m.ResetAdminPassword(ctx, sam, grafana.ID)
	var refused *service.ResetFailedError
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "bad password ••••", refused.Output)
	require.Equal(t, r.Password, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))
	require.Equal(t, r.Password, kept(grafana.ID, sam).Password)
	orch.out, orch.exit = nil, 0

	// A command that never started changes nothing either.
	orch.err = errors.New("ContainerExecCreate: no such container")
	_, err = m.ResetAdminPassword(ctx, alex, grafana.ID)
	require.Error(t, err)
	require.Equal(t, r.Password, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))
	require.Nil(t, kept(grafana.ID, alex))

	// One that started but never answered keeps the new password: the app
	// most likely has it.
	orch.err = fmt.Errorf("%w: read output: connection reset", service.ErrOutcomeUnknown)
	_, err = m.ResetAdminPassword(ctx, alex, grafana.ID)
	require.ErrorIs(t, err, service.ErrResetUnconfirmed)
	got = kept(grafana.ID, alex)
	require.NotNil(t, got)
	require.Equal(t, got.Password, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))
	orch.err = nil

	// A copy that can't be kept rolls the secret back.
	before := secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD")
	failing := service.NewServiceManager(service.NewPostgresRepository(db, keys), zap.NewNop())
	failing.WithVariables(vars)
	failing.WithTransactor(dbtx.NewRunner(db))
	failing.WithReveals(failingKeeper{})
	failing.WithOrchestration(noServers{}, resetFactory{orch})
	_, err = failing.ResetAdminPassword(ctx, sam, grafana.ID)
	require.Error(t, err)
	require.Equal(t, before, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))

	// Two at once: the second waits for the first, so the app ends on the
	// password stored, and only the second's copy is left.
	orch.out = func(string) string { time.Sleep(100 * time.Millisecond); return "" }
	from := len(orch.ran)
	var wg sync.WaitGroup
	for _, who := range []string{alex, sam} {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.ResetAdminPassword(ctx, who, grafana.ID); assert.NoError(t, err) }()
	}
	wg.Wait()
	orch.out = nil
	require.Len(t, orch.ran, from+2)
	last := strings.TrimSpace(orch.ran[len(orch.ran)-1].Stdin)
	require.Equal(t, last, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))
	a, b := kept(grafana.ID, alex), kept(grafana.ID, sam)
	require.True(t, (a == nil) != (b == nil), "one copy left")
	require.Equal(t, last, map[bool]*reveal.Reveal{true: a, false: b}[a != nil].Password)

	// Umami's psql prints the accounts it set: none is a refusal.
	umami := create("umami", "")
	orch.out = func(string) string { return "0\n" }
	_, err = m.ResetAdminPassword(ctx, alex, umami.ID)
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "db", orch.ran[len(orch.ran)-1].ComposeService)
	orch.out = func(string) string { return "1\n" }
	_, err = m.ResetAdminPassword(ctx, alex, umami.ID)
	require.NoError(t, err)
	orch.out = nil

	// Stopped: nothing changes.
	before = secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD")
	_, err = db.Exec(ctx, `UPDATE services SET runtime_status = 'stopped' WHERE id = $1`, grafana.ID)
	require.NoError(t, err)
	_, err = m.ResetAdminPassword(ctx, sam, grafana.ID)
	require.ErrorIs(t, err, service.ErrNotRunning)
	require.Equal(t, before, secret(grafana.ID, "GF_SECURITY_ADMIN_PASSWORD"))

	// MinIO reads the secret at each start: no command, a deploy applies it.
	n := len(orch.ran)
	minio := create("minio", "pgsty/silo:RELEASE.2026-10-26T00-00-00Z", service.KeyValue{Key: "MINIO_ROOT_USER", Value: "minioadmin"})
	r, err = m.ResetAdminPassword(ctx, alex, minio.ID)
	require.NoError(t, err)
	require.True(t, r.Deploy)
	require.Equal(t, "minioadmin", r.User)
	require.Len(t, orch.ran, n)

	// A database root has no Reset.
	postgres := create("postgres", "postgres:16")
	_, err = m.ResetAdminPassword(ctx, alex, postgres.ID)
	require.ErrorIs(t, err, service.ErrNoReset)

	// Replacing the secret by hand drops the copy: it no longer opens MinIO.
	require.NotNil(t, kept(minio.ID, alex))
	require.NoError(t, vars.ReplaceSecret(ctx, minio.ID, alex, "MINIO_ROOT_PASSWORD", "chosen-by-hand"))
	require.Nil(t, kept(minio.ID, alex))
	// And deleting it.
	_, err = m.ResetAdminPassword(ctx, alex, minio.ID)
	require.NoError(t, err)
	require.NotNil(t, kept(minio.ID, alex))
	require.NoError(t, vars.DeleteSecret(ctx, minio.ID, "MINIO_ROOT_PASSWORD"))
	require.Nil(t, kept(minio.ID, alex))

	// A new basic auth password drops the temporary login's copy; a save
	// that keeps it doesn't.
	ghost, err := m.Create(ctx, envID, alex, service.CreateInput{Name: "blog", Type: service.TypeWeb, SourceType: service.SourceTypeDockerCompose,
		ComposeContent: mustTemplate(t, "ghost").ComposeContent, Port: 2368, AddonSlug: "ghost"})
	require.NoError(t, err)
	require.NotNil(t, kept(ghost.ID, alex))
	_, err = m.UpdateConfig(ctx, ghost.ID, service.UpdateConfigInput{BasicAuthUser: "toft"})
	require.NoError(t, err)
	require.NotNil(t, kept(ghost.ID, alex))
	_, err = m.UpdateConfig(ctx, ghost.ID, service.UpdateConfigInput{BasicAuthUser: "toft", BasicAuthPassword: "my-own"})
	require.NoError(t, err)
	require.Nil(t, kept(ghost.ID, alex))
}
