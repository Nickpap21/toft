package server_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const encryptionKey = "test-encryption-key"

// A plain-text private key from before encryption is encrypted at start-up
// and still reads back; a value under another ENCRYPTION_KEY is left as it
// is; a second start converts nothing.
func TestEncryptLegacySSHKeys(t *testing.T) {
	db := testdb.New(t)
	repo := server.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	var userID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&userID))

	const plain = "-----BEGIN OPENSSH PRIVATE KEY-----\nlegacy\n-----END OPENSSH PRIVATE KEY-----\n" // gitleaks:allow
	current, err := crypto.Encrypt(encryptionKey, "-----BEGIN OPENSSH PRIVATE KEY-----\ncurrent\n")  // gitleaks:allow
	require.NoError(t, err)
	foreign, err := crypto.Encrypt("another-encryption-key", "-----BEGIN OPENSSH PRIVATE KEY-----\nforeign\n")
	require.NoError(t, err)
	legacyID := insertServer(t, db, userID, "legacy", plain)
	currentID := insertServer(t, db, userID, "current", current)
	foreignID := insertServer(t, db, userID, "foreign", foreign)

	n, err := repo.EncryptLegacySSHKeys(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "only the plain-text key converts")

	require.NotEqual(t, plain, storedKey(t, db, legacyID), "the key is no longer stored in plain text")
	s, err := repo.GetByID(ctx, legacyID)
	require.NoError(t, err)
	require.Equal(t, plain, s.SSHKey)
	require.Equal(t, current, storedKey(t, db, currentID))
	require.Equal(t, foreign, storedKey(t, db, foreignID))

	n, err = repo.EncryptLegacySSHKeys(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "the second start converts none")
}

func insertServer(t *testing.T, db *pgxpool.Pool, userID, name, key string) string {
	t.Helper()
	var id string
	require.NoError(t, db.QueryRow(context.Background(),
		`INSERT INTO servers (user_id, name, ip_address, ssh_key) VALUES ($1, $2, '203.0.113.10', $3) RETURNING id::text`,
		userID, name, key,
	).Scan(&id))
	return id
}

func storedKey(t *testing.T, db *pgxpool.Pool, id string) string {
	t.Helper()
	var key string
	require.NoError(t, db.QueryRow(context.Background(), `SELECT ssh_key FROM servers WHERE id = $1`, id).Scan(&key))
	return key
}

// What the server package seals in v1 (D-126) opens where it was written: a
// server's SSH key (created, and set again by an agent's join), its DNS
// credentials, its backup destination's secrets (created and updated) and
// the panel's agent key.
func TestSealedRoundTripInV1(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys := crypto.NewKeys(encryptionKey, "")
	repo := server.NewPostgresRepository(db, keys)
	agents := server.NewPostgresAgentStore(db, keys)
	var userID, orgID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))

	srv, err := repo.Create(ctx, orgID, userID, server.CreateServerInput{Name: "web", IPAddress: "203.0.113.10", User: "root", Port: 22, SSHKey: "pem-1"})
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "pem-1", got.SSHKey)
	require.NoError(t, agents.SetSSHCredentials(ctx, srv.ID, "toft", 22, "pem-2"))
	got, err = repo.GetByID(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "pem-2", got.SSHKey)

	dns, err := repo.SetDNSProvider(ctx, srv.ID, "cloudflare", map[string]string{"api_token": "cf"})
	require.NoError(t, err)
	require.Equal(t, "cf", dns.Credentials["api_token"])
	dns, err = repo.GetDNSProvider(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "cf", dns.Credentials["api_token"])

	in := server.BackupInput{Endpoint: "https://s3.example.com", Bucket: "toft-backups", AccessKey: "ak", SecretKey: "sk-1", Schedule: "03:00"}
	dest, err := repo.CreateBackupDestination(ctx, srv.ID, in, "repo-pw")
	require.NoError(t, err)
	require.Equal(t, []string{"sk-1", "repo-pw"}, []string{dest.SecretKey, dest.RepoPassword})
	in.SecretKey = "sk-2"
	_, err = repo.UpdateBackupDestination(ctx, dest.ID, in)
	require.NoError(t, err)
	dest, err = repo.GetBackupDestination(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"sk-2", "repo-pw"}, []string{dest.SecretKey, dest.RepoPassword})

	made, err := agents.PanelAgentKey(ctx)
	require.NoError(t, err)
	again, err := agents.PanelAgentKey(ctx)
	require.NoError(t, err)
	require.Equal(t, made, again)
}

// A run that ended ok with warnings records and returns them, and the
// workspace's server list reads them in one query; the next run starts with
// none, and only the workspace's own servers are listed.
func TestProvisionRunWarnings(t *testing.T) {
	db := testdb.New(t)
	repo := server.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	var userID, orgID, otherOrgID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&userID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Acme') RETURNING id::text`).Scan(&orgID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('Other') RETURNING id::text`).Scan(&otherOrgID))
	key, err := crypto.Encrypt(encryptionKey, "k")
	require.NoError(t, err)
	warnedID := insertServer(t, db, userID, "warned", key)
	cleanID := insertServer(t, db, userID, "clean", key)
	otherID := insertServer(t, db, userID, "other", key)
	_, err = db.Exec(ctx, `UPDATE servers SET org_id = CASE WHEN id = $1 THEN $2::uuid ELSE $3::uuid END`, otherID, otherOrgID, orgID)
	require.NoError(t, err)

	warnings := []string{"the firewall (ufw, fail2ban)", "the host firewall for app containers"}
	for _, id := range []string{warnedID, cleanID, otherID} {
		require.NoError(t, repo.StartProvisionRun(ctx, id, userID))
	}
	require.NoError(t, repo.FinishProvisionRun(ctx, warnedID, true, "", warnings))
	require.NoError(t, repo.FinishProvisionRun(ctx, cleanID, true, "", nil))
	require.NoError(t, repo.FinishProvisionRun(ctx, otherID, true, "", warnings))

	run, err := repo.GetProvisionRun(ctx, warnedID)
	require.NoError(t, err)
	require.True(t, *run.OK)
	require.Equal(t, warnings, run.Warnings)
	run, err = repo.GetProvisionRun(ctx, cleanID)
	require.NoError(t, err)
	require.Equal(t, []string{}, run.Warnings, "none is [], never null")

	listed, err := repo.ListProvisionWarnings(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{warnedID: warnings}, listed)

	require.NoError(t, repo.StartProvisionRun(ctx, warnedID, userID))
	run, err = repo.GetProvisionRun(ctx, warnedID)
	require.NoError(t, err)
	require.Empty(t, run.Warnings, "a new run starts with none")
	listed, err = repo.ListProvisionWarnings(ctx, orgID)
	require.NoError(t, err)
	require.Empty(t, listed)
}

// A server's updates read (D-133) round-trips; a read that fails keeps what
// the last one read, with its error; a server read under 6 hours ago isn't
// stale, and a removed server takes its read with it.
func TestServerUpdatesStore(t *testing.T) {
	db := testdb.New(t)
	repo := server.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	store := repo.(server.UpdateStore)
	ctx := context.Background()
	var userID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&userID))
	key, err := crypto.Encrypt(encryptionKey, "k")
	require.NoError(t, err)
	readID := insertServer(t, db, userID, "read", key)
	neverID := insertServer(t, db, userID, "never", key)

	got, err := store.GetUpdates(ctx, readID)
	require.NoError(t, err)
	require.Nil(t, got)
	stale, err := store.StaleUpdateServers(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{readID, neverID}, stale)

	lists := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	read := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	u := server.HostUpdates{Security: 1, ReadAt: &read, ListsUpdatedAt: &lists, Error: "dropped",
		Packages:   []server.PendingPackage{{Name: "liblzma5", Current: "1", Candidate: "2", Security: true}},
		Reboot:     server.RebootState{Packages: []string{}},
		Docker:     server.DockerEngine{Version: "29.4.3"},
		Unattended: server.Unattended{State: "active", LastResult: "never"}}
	require.NoError(t, store.SaveUpdates(ctx, readID, u))
	got, err = store.GetUpdates(ctx, readID)
	require.NoError(t, err)
	require.Equal(t, "", got.Error)
	require.Equal(t, 1, got.Security)
	require.Equal(t, lists, *got.ListsUpdatedAt)
	require.Equal(t, u.Packages, got.Packages)
	require.Equal(t, "29.4.3", got.Docker.Version)
	require.WithinDuration(t, time.Now(), got.CheckedAt, time.Minute)

	require.NoError(t, store.SetUpdatesError(ctx, readID, "The read couldn't run: dial"))
	got, err = store.GetUpdates(ctx, readID)
	require.NoError(t, err)
	require.Equal(t, "The read couldn't run: dial", got.Error)
	require.Equal(t, 1, got.Security, "the last read that worked stays")
	require.Equal(t, read, *got.ReadAt, "with when it ran")

	require.NoError(t, store.SetUpdatesError(ctx, neverID, "The read couldn't run: dial"))
	all, err := store.ListUpdates(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "unknown", all[neverID].Unattended.State, "never read: nothing to judge")
	require.False(t, all[neverID].Health(time.Now()).Known)
	require.Equal(t, []server.PendingPackage{}, all[neverID].Packages)

	stale, err = store.StaleUpdateServers(ctx)
	require.NoError(t, err)
	require.Empty(t, stale)
	_, err = db.Exec(ctx, `UPDATE server_updates SET checked_at = NOW() - INTERVAL '7 hours' WHERE server_id = $1`, neverID)
	require.NoError(t, err)
	stale, err = store.StaleUpdateServers(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{neverID}, stale)

	_, err = db.Exec(ctx, `DELETE FROM servers WHERE id = $1`, readID)
	require.NoError(t, err)
	all, err = store.ListUpdates(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestServerUpdateRunStore(t *testing.T) {
	db := testdb.New(t)
	repo := server.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	store := repo.(server.UpdateRunStore)
	ctx := context.Background()
	var userID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&userID))
	key, err := crypto.Encrypt(encryptionKey, "k")
	require.NoError(t, err)
	srvID := insertServer(t, db, userID, "app", key)

	got, err := store.GetUpdateRun(ctx, srvID)
	require.NoError(t, err)
	require.Nil(t, got)

	first, second := server.NewUpdateRunID(), server.NewUpdateRunID()
	took, err := store.StartUpdateRun(ctx, srvID, first, "docker", userID)
	require.NoError(t, err)
	require.True(t, took)
	took, err = store.StartUpdateRun(ctx, srvID, second, "install", userID)
	require.NoError(t, err)
	require.False(t, took, "a second run while one is unfinished")
	updating, err := store.ServerUpdating(ctx, srvID)
	require.NoError(t, err)
	require.True(t, updating, "a Docker update holds the server's deploys")
	ids, err := store.ListUnfinishedUpdateRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{srvID}, ids)

	got, err = store.GetUpdateRun(ctx, srvID)
	require.NoError(t, err)
	require.Equal(t, first, got.RunID)
	require.Equal(t, userID, got.StartedByID)
	require.Equal(t, "Owner", got.StartedBy)
	require.Equal(t, "running", got.Outcome)
	require.Nil(t, got.FinishedAt)

	at := time.Date(2026, 10, 30, 18, 4, 0, 0, time.UTC)
	run := server.UpdateRun{Outcome: "succeeded", Packages: 5, FinishedAt: &at, Output: []string{"a", "b"}}
	finished, err := store.FinishUpdateRun(ctx, srvID, second, run)
	require.NoError(t, err)
	require.False(t, finished, "another run's ID finishes nothing")
	finished, err = store.FinishUpdateRun(ctx, srvID, first, run)
	require.NoError(t, err)
	require.True(t, finished)
	finished, err = store.FinishUpdateRun(ctx, srvID, first, run)
	require.NoError(t, err)
	require.False(t, finished, "a run finishes once, so its outcome is audited once")

	got, err = store.GetUpdateRun(ctx, srvID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", got.Outcome)
	require.Equal(t, 5, got.Packages)
	require.Equal(t, []string{"a", "b"}, got.Output)
	require.True(t, at.Equal(*got.FinishedAt))
	updating, err = store.ServerUpdating(ctx, srvID)
	require.NoError(t, err)
	require.False(t, updating)

	took, err = store.StartUpdateRun(ctx, srvID, second, "install", userID)
	require.NoError(t, err)
	require.True(t, took, "a finished run makes way for the next")
	updating, err = store.ServerUpdating(ctx, srvID)
	require.NoError(t, err)
	require.False(t, updating, "an install doesn't hold deploys")
}

// The address pool the probe read keeps its three states: unknown (a
// server only a failed probe or a release before it read), none (Docker
// has no default-address-pools) and the pools Docker reported.
func TestCapabilitiesAddressPools(t *testing.T) {
	db := testdb.New(t)
	repo := server.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	store := repo.(server.CapabilityStore)
	ctx := context.Background()
	var userID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&userID))
	key, err := crypto.Encrypt(encryptionKey, "k")
	require.NoError(t, err)
	srvID := insertServer(t, db, userID, "app", key)

	require.NoError(t, store.SetCapabilitiesError(ctx, srvID, "The probe couldn't run"))
	got, err := store.GetCapabilities(ctx, srvID)
	require.NoError(t, err)
	require.Nil(t, got.AddressPools, "never read")

	require.NoError(t, store.SaveCapabilities(ctx, srvID, server.Capabilities{Kernel: "6.8.0", Runtimes: []server.Runtime{}, AddressPools: []server.AddressPool{}}))
	got, err = store.GetCapabilities(ctx, srvID)
	require.NoError(t, err)
	require.Equal(t, []server.AddressPool{}, got.AddressPools, "not set")

	pools := []server.AddressPool{{Base: "10.50.0.0/16", Size: 26}}
	require.NoError(t, store.SaveCapabilities(ctx, srvID, server.Capabilities{Kernel: "6.8.0", Runtimes: []server.Runtime{}, AddressPools: pools}))
	require.NoError(t, store.SetCapabilitiesError(ctx, srvID, "connection reset"))
	got, err = store.GetCapabilities(ctx, srvID)
	require.NoError(t, err)
	require.Equal(t, pools, got.AddressPools, "a failed probe keeps the last read")

	// 64 IPv6 pools pass Marshal's 4096 bytes in jsonb's spaced text: the
	// ones that fit are kept, with the rest of the probe.
	many := make([]server.AddressPool, 0, 64)
	for i := range 64 {
		many = append(many, server.AddressPool{Base: fmt.Sprintf("fd00:1234:5678:9abc:def0:1234:%04x:0000/112", i), Size: 120})
	}
	require.NoError(t, store.SaveCapabilities(ctx, srvID, server.Capabilities{Kernel: "6.9.0", Runtimes: []server.Runtime{}, AddressPools: many}))
	got, err = store.GetCapabilities(ctx, srvID)
	require.NoError(t, err)
	require.Equal(t, "6.9.0", got.Kernel)
	require.NotEmpty(t, got.AddressPools)
	require.Equal(t, many[:len(got.AddressPools)], got.AddressPools)
}
