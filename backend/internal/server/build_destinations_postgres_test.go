package server_test

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// destShell is a host that takes a list, or doesn't answer.
type destShell struct {
	own  string
	down bool
	cmds []string
}

func (h *destShell) Exec(cmd string) (string, error) {
	h.cmds = append(h.cmds, cmd)
	switch {
	case h.down:
		return "", errors.New("connection refused")
	case strings.Contains(cmd, `print "own:"`):
		return "own:" + h.own + "\nnet:172.18.0.0/16\nread", nil
	case strings.HasPrefix(cmd, "nft list table ip toft"):
		return "loaded", nil
	}
	return "", nil
}

func (h *destShell) ExecInput(cmd string, _ io.Reader) (string, error) { return h.Exec(cmd) }

func (h *destShell) ExecStream(cmd string, _ func(string)) error {
	_, err := h.Exec(cmd)
	return err
}

// destRemote is a pooled connection to a destShell.
type destRemote struct {
	sshpool.RemoteHost
	h *destShell
}

func (r destRemote) Exec(_ context.Context, cmd string) (string, error) { return r.h.Exec(cmd) }
func (r destRemote) ExecInput(_ context.Context, cmd string, in io.Reader) (string, error) {
	return r.h.ExecInput(cmd, in)
}
func (r destRemote) ExecStream(_ context.Context, cmd string, logFn func(string)) error {
	return r.h.ExecStream(cmd, logFn)
}

type destConn struct{ h *destShell }

func (c destConn) Handle(sshpool.Target) sshpool.RemoteHost { return destRemote{h: c.h} }
func (destConn) Invalidate(string)                          {}

type destPanel struct{ h *destShell }

func (p destPanel) PanelHostShell(context.Context) server.Shell { return p.h }

// A build destination on Postgres (D-170): the entry and its audit rows,
// the instance's and the server's workspace's, are saved with the server's
// answer or not at all.
func TestBuildDestinationsOnPostgres(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys(encryptionKey, "")
	repo := server.NewPostgresRepository(db, keys)
	store := repo.(server.BuildDestinationStore)
	instanceSvc := instance.NewService(instance.NewPostgresRepository(db), zap.NewNop())
	orgSvc := org.NewService(org.NewPostgresRepository(db, keys), zap.NewNop())

	row := func(q string, args ...any) string {
		var out string
		require.NoError(t, db.QueryRow(ctx, q, args...).Scan(&out))
		return out
	}
	count := func(table string) int {
		var n int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n))
		return n
	}
	orgID := row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	ada := row(`INSERT INTO users (email, name, password_hash) VALUES ('ada@toft.test', 'Ada', 'x') RETURNING id::text`)
	_, err := db.Exec(ctx, `INSERT INTO instance_operators (user_id, source) VALUES ($1, 'first_run')`, ada)
	require.NoError(t, err)
	key, err := crypto.Encrypt(encryptionKey, "k")
	require.NoError(t, err)
	web := insertServer(t, db, ada, "web-1", key)
	other := insertServer(t, db, ada, "web-2", key)
	_, err = db.Exec(ctx, `UPDATE servers SET org_id = $1`, orgID)
	require.NoError(t, err)

	host, panel := &destShell{own: "10.0.0.5/24"}, &destShell{own: "10.0.0.2/24"}
	svc := server.NewService(repo, "")
	svc.WithOperatorChecker(instanceSvc)
	svc.WithSSH(destConn{host}, nil)
	svc.WithBuildDestinations(store, dbtx.NewRunner(db), destPanel{panel}, instanceSvc, orgSvc)
	p5000 := 5000
	in := server.BuildDestinationInput{Address: "10.0.5.20", Port: &p5000, Note: "registry"}
	saved := func() [3]int {
		return [3]int{count("build_destinations"), count("audit_log"), count("instance_audit_log")}
	}

	// A server that doesn't answer: nothing is saved.
	host.down = true
	_, err = svc.AddBuildDestination(ctx, ada, web, in)
	require.ErrorIs(t, err, server.ErrBuildDestinationsNotApplied)
	assert.Equal(t, [3]int{}, saved(), "no entry and no audit row")

	// One that takes the list: the entry, and a row in each log.
	host.down = false
	d, err := svc.AddBuildDestination(ctx, ada, web, in)
	require.NoError(t, err)
	assert.Equal(t, [3]int{1, 1, 1}, saved())
	assert.Equal(t, orgID+" "+ada+" server.build_destination_added server:"+web+" 10.0.5.20 5000 registry",
		row(`SELECT concat_ws(' ', org_id, actor_id, action, resource, metadata->>'address', metadata->>'port', metadata->>'note') FROM audit_log`))
	assert.Equal(t, ada+" build_destination.added "+web+" web-1 10.0.5.20 5000",
		row(`SELECT concat_ws(' ', actor_id, action, detail->>'server_id', detail->>'server', detail->>'address', detail->>'port') FROM instance_audit_log`))
	assert.Equal(t, ada, row(`SELECT created_by::text FROM build_destinations`))

	// The same entry again is refused, and leaves no audit row.
	_, err = svc.AddBuildDestination(ctx, ada, web, in)
	var refused *server.BuildDestinationError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "build_destination_exists", refused.Code)
	assert.Equal(t, [3]int{1, 1, 1}, saved())

	// Each server has its own list; an entry isn't removed through another's.
	list, err := svc.ListBuildDestinations(ctx, ada, other)
	require.NoError(t, err)
	assert.Empty(t, list)
	require.ErrorIs(t, svc.RemoveBuildDestination(ctx, ada, other, d.ID), server.ErrBuildDestinationNotFound)
	assert.Equal(t, [3]int{1, 1, 1}, saved())

	// The panel's own server: its list has no server, and no workspace.
	everyPort := server.BuildDestinationInput{Address: "192.168.7.0/24"}
	_, err = svc.AddBuildDestination(ctx, ada, "", everyPort)
	require.NoError(t, err)
	assert.Equal(t, [3]int{2, 1, 2}, saved())
	_, err = svc.AddBuildDestination(ctx, ada, "", everyPort)
	require.ErrorAs(t, err, &refused, "once in a list, with no server and no port too")
	assert.Equal(t, "build_destination_exists", refused.Code)
	list, err = svc.ListBuildDestinations(ctx, ada, "")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "192.168.7.0/24", list[0].Address())
	assert.Nil(t, list[0].Port)

	// A removal the server doesn't take leaves the entry listed.
	host.down = true
	require.ErrorIs(t, svc.RemoveBuildDestination(ctx, ada, web, d.ID), server.ErrBuildDestinationsNotApplied)
	assert.Equal(t, [3]int{2, 1, 2}, saved())
	host.down = false
	require.NoError(t, svc.RemoveBuildDestination(ctx, ada, web, d.ID))
	assert.Equal(t, [3]int{1, 2, 3}, saved())
	assert.Equal(t, "server.build_destination_removed 10.0.5.20",
		row(`SELECT concat_ws(' ', action, metadata->>'address') FROM audit_log ORDER BY created_at DESC LIMIT 1`))

	// The database holds the rule too: only private IPv4 gets in.
	for _, cidr := range []string{"8.8.8.8/32", "127.0.0.1/32", "169.254.169.254/32", "172.32.0.1/32", "fd00::1/128"} {
		_, err := store.AddBuildDestination(ctx, web, netip.MustParsePrefix(cidr), nil, "", ada)
		assert.Error(t, err, cidr)
	}

	// One change to a server's list at a time: a second waits for the
	// first's transaction, and another server's list doesn't.
	tx := dbtx.NewRunner(db)
	require.NoError(t, tx.InTx(ctx, func(held context.Context) error {
		require.NoError(t, store.LockBuildDestinations(held, web))
		short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		err := tx.InTx(short, func(c context.Context) error { return store.LockBuildDestinations(c, web) })
		assert.ErrorIs(t, err, context.DeadlineExceeded, "the same list waits")
		return tx.InTx(ctx, func(c context.Context) error { return store.LockBuildDestinations(c, other) })
	}))

	// A server's list goes with the server.
	_, err = svc.AddBuildDestination(ctx, ada, web, in)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `DELETE FROM servers WHERE id = $1`, web)
	require.NoError(t, err)
	assert.Equal(t, 1, count("build_destinations"), "the panel's own stays")
}
