package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// Add a server (D-153) on Postgres: the waiting server reused per person,
// automatic names, the name rule, the hostname only over an automatic name,
// the SSH key made once, moving to SSH, and the sweep.
func TestAddServerWaitingServers(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys(encryptionKey, "")
	repo := server.NewPostgresRepository(db, keys)
	store := server.NewPostgresAgentStore(db, keys)
	svc := server.NewService(repo, "")
	svc.WithAgents(store, nil, nil, t.TempDir())

	row := func(q string, args ...any) string {
		var id string
		require.NoError(t, db.QueryRow(ctx, q, args...).Scan(&id))
		return id
	}
	org := row(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`)
	ada := row(`INSERT INTO users (email, name, password_hash) VALUES ('ada@toft.test', 'Ada', 'x') RETURNING id::text`)
	bo := row(`INSERT INTO users (email, name, password_hash) VALUES ('bo@toft.test', 'Bo', 'x') RETURNING id::text`)

	// Opening Add server twice is one server, with a new command.
	s1, tok1, reused, err := svc.CreateAgentServer(ctx, org, ada, "", "")
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, "server-1", s1.Name)
	again, tok2, reused, err := svc.CreateAgentServer(ctx, org, ada, "", "")
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, s1.ID, again.ID)
	_, err = store.PeekJoinToken(ctx, hash(tok1.Token))
	assert.Error(t, err, "the older command stops working")
	_, err = store.PeekJoinToken(ctx, hash(tok2.Token))
	require.NoError(t, err)
	// The older one is gone, not kept: a page that made many leaves one.
	var tokens int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM server_join_tokens WHERE server_id = $1`, s1.ID).Scan(&tokens))
	assert.Equal(t, 1, tokens)

	// Someone else's visit is their own server.
	s2, _, reused, err := svc.CreateAgentServer(ctx, org, bo, "", "")
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, "server-2", s2.Name)

	// The rule applies to a name given; the workspace's names are unique.
	_, _, _, err = svc.CreateAgentServer(ctx, org, ada, "Hel1_prod", "")
	require.ErrorIs(t, err, server.ErrInvalidServerName)
	named, _, _, err := svc.CreateAgentServer(ctx, org, ada, "hel1-prod-03", "")
	require.NoError(t, err)
	_, _, _, err = svc.CreateAgentServer(ctx, org, ada, "hel1-prod-03", "")
	require.ErrorIs(t, err, server.ErrServerNameTaken)
	_, err = svc.RenameServer(ctx, s2.ID, "hel1-prod-03")
	require.ErrorIs(t, err, server.ErrServerNameTaken)
	_, err = svc.RenameServer(ctx, s2.ID, "Bad name")
	require.ErrorIs(t, err, server.ErrInvalidServerName)

	// The hostname replaces an automatic name once, never a chosen one.
	ok, err := repo.NameFromHostname(ctx, s1.ID, "web-1")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = repo.NameFromHostname(ctx, s1.ID, "web-2")
	require.NoError(t, err)
	assert.False(t, ok, "no longer automatic")
	ok, err = repo.NameFromHostname(ctx, named.ID, "web-3")
	require.NoError(t, err)
	assert.False(t, ok, "a chosen name stays")
	_, err = repo.NameFromHostname(ctx, s2.ID, "web-1")
	require.ErrorIs(t, err, server.ErrServerNameTaken)

	// Joined, a server is no longer reused: the next visit makes server-3
	// (server-1 is web-1 now; server-2 is the highest left).
	_, _, by, pinned, err := store.JoinWithToken(ctx, hash(tok2.Token), "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOvCSPs1+hFcIdqTTxSd7mH59R37qkPZPpq6PxmPEe4D")
	require.NoError(t, err)
	require.True(t, pinned)
	assert.Equal(t, ada, by)
	creator, err := store.LastJoinTokenCreator(ctx, s1.ID)
	require.NoError(t, err)
	assert.Equal(t, ada, creator)
	// A join from over an hour ago (the old page provisioned on a click)
	// isn't provisioned by a reconnect.
	_, err = db.Exec(ctx, `UPDATE server_join_tokens SET used_at = NOW() - INTERVAL '2 hours' WHERE server_id = $1 AND used_at IS NOT NULL`, s1.ID)
	require.NoError(t, err)
	creator, err = store.LastJoinTokenCreator(ctx, s1.ID)
	require.NoError(t, err)
	assert.Empty(t, creator)
	s3, _, reused, err := svc.CreateAgentServer(ctx, org, ada, "", "")
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, "server-3", s3.Name)
	_, err = svc.WaitingServerSSHKey(ctx, s1.ID)
	require.ErrorIs(t, err, server.ErrServerJoined)

	// The SSH key is made once; moving to SSH stops the commands.
	line, err := svc.WaitingServerSSHKey(ctx, s2.ID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(line, "ssh-ed25519 "), line)
	assert.True(t, strings.HasSuffix(line, " toft-server-2"), line)
	same, err := svc.WaitingServerSSHKey(ctx, s2.ID)
	require.NoError(t, err)
	assert.Equal(t, line, same)
	_, bTok, _, err := svc.CreateAgentServer(ctx, org, bo, "", "")
	require.NoError(t, err)
	// It signs in first: a key root doesn't take, or no key at all,
	// leaves the server waiting.
	port := sshd(t, line)
	_, err = svc.UseSSH(ctx, s2.ID, "127.0.0.1", port, "not a key")
	require.ErrorIs(t, err, server.ErrSSHFailed)
	_, err = svc.UseSSH(ctx, s2.ID, "127.0.0.1", sshd(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOvCSPs1+hFcIdqTTxSd7mH59R37qkPZPpq6PxmPEe4D"), "")
	require.ErrorIs(t, err, server.ErrSSHFailed)
	_, err = store.PeekJoinToken(ctx, hash(bTok.Token))
	require.NoError(t, err, "still waiting")
	moved, err := svc.UseSSH(ctx, s2.ID, "127.0.0.1", port, "")
	require.NoError(t, err)
	assert.Equal(t, server.ConnectionSSH, moved.Connection)
	assert.Equal(t, "root", moved.User)
	assert.Equal(t, port, moved.Port)
	assert.Contains(t, moved.SSHKey, "OPENSSH PRIVATE KEY")
	assert.NotEmpty(t, moved.HostKey, "the host key it presented is pinned")
	_, err = store.PeekJoinToken(ctx, hash(bTok.Token))
	assert.Error(t, err, "a server reached over SSH can't be joined")
	_, err = svc.UseSSH(ctx, s2.ID, "127.0.0.1", port, "")
	require.ErrorIs(t, err, server.ErrServerJoined)

	// The sweep: a waiting server whose command expired over a day ago goes;
	// a recent one, a joined one, one with a service and one a person named
	// (the old page's) stay.
	old, _, _, err := svc.CreateAgentServer(ctx, org, bo, "", "")
	require.NoError(t, err)
	busy, _, _, err := svc.CreateAgentServer(ctx, org, ada, "", "")
	require.NoError(t, err)
	for _, id := range []string{old.ID, busy.ID, s1.ID, named.ID} {
		_, err = db.Exec(ctx, `UPDATE servers SET created_at = NOW() - INTERVAL '3 days', updated_at = NOW() - INTERVAL '3 days' WHERE id = $1`, id)
		require.NoError(t, err)
		_, err = db.Exec(ctx, `UPDATE server_join_tokens SET expires_at = NOW() - INTERVAL '2 days' WHERE server_id = $1`, id)
		require.NoError(t, err)
	}
	project := row(`INSERT INTO projects (org_id, user_id, name) VALUES ($1, $2, 'shop') RETURNING id::text`, org, ada)
	env := row(`INSERT INTO environments (project_id, name) VALUES ($1, 'production') RETURNING id::text`, project)
	_, err = db.Exec(ctx, `INSERT INTO services (environment_id, name, server_id) VALUES ($1, 'web', $2)`, env, busy.ID)
	require.NoError(t, err)

	gone, err := store.DeleteUnjoinedServers(ctx)
	require.NoError(t, err)
	require.Len(t, gone, 1)
	assert.Equal(t, server.RemovedServer{ID: old.ID, OrgID: org, Name: old.Name}, gone[0])
	for _, id := range []string{s1.ID, s3.ID, busy.ID, named.ID, s2.ID} {
		_, err := repo.GetByID(ctx, id)
		require.NoError(t, err, id)
	}
}

func hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// sshd serves SSH on 127.0.0.1 for root with the key in authorized (an
// authorized_keys line) until the test ends, and returns its port.
func sshd(t *testing.T, authorized string) int {
	t.Helper()
	want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	require.NoError(t, err)
	_, hk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	host, err := ssh.NewSignerFromKey(hk)
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if c.User() == "root" && bytes.Equal(k.Marshal(), want.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("refused")
	}}
	cfg.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "no channels here")
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}
