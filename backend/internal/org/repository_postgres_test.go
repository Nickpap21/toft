package org_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const encryptionKey = "test-encryption-key"

// ownerAndOrg makes an account and a workspace it owns.
func ownerAndOrg(t *testing.T, db *pgxpool.Pool, repo org.Repository) (userID, orgID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&userID))
	o, err := repo.CreateWithOwner(ctx, "Acme", userID)
	require.NoError(t, err)
	return userID, o.ID
}

// An invite from before 0054 holds the link itself; the start-up conversion
// makes it findable by the link again, and a second start converts nothing.
// Both links, sealed in v1 (D-126), list again.
func TestHashUnhashedInvites(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	userID, orgID := ownerAndOrg(t, db, repo)

	const legacy = "legacy-invite-link"
	_, err := db.Exec(ctx,
		`INSERT INTO org_invites (org_id, created_by, token, role, expires_at) VALUES ($1, $2, $3, 'developer', NOW() + interval '1 day')`,
		orgID, userID, legacy)
	require.NoError(t, err)
	current, err := repo.CreateInvite(ctx, orgID, userID, "viewer", "current-invite-link", time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = repo.GetInviteByToken(ctx, legacy)
	require.ErrorIs(t, err, org.ErrNotFound, "before the conversion the lookup by hash misses it")

	n, err := repo.HashUnhashedInvites(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "only the legacy invite converts")

	inv, err := repo.GetInviteByToken(ctx, legacy)
	require.NoError(t, err)
	require.Equal(t, orgID, inv.OrgID)
	require.Equal(t, "developer", inv.Role)
	var stored, enc string
	require.NoError(t, db.QueryRow(ctx, `SELECT token, token_encrypted FROM org_invites WHERE id = $1`, inv.ID).Scan(&stored, &enc))
	require.Equal(t, org.HashInviteToken(legacy), stored, "the link itself is no longer stored")
	plain, err := crypto.NewKeys(encryptionKey, "").Open(crypto.AD("org_invites.token_encrypted", inv.ID), enc)
	require.NoError(t, err)
	require.Equal(t, legacy, plain)

	_, err = repo.GetInviteByToken(ctx, "current-invite-link")
	require.NoError(t, err, "an invite made after 0054 is untouched")
	require.NotEmpty(t, current.ID)

	n, err = repo.HashUnhashedInvites(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "the second start converts none")

	listed, err := repo.ListInvites(ctx, orgID)
	require.NoError(t, err)
	links := []string{}
	for _, i := range listed {
		links = append(links, i.Token)
	}
	require.ElementsMatch(t, []string{legacy, "current-invite-link"}, links)
}

// An existing account redeems an invite once: a second redemption is
// refused, and one refused because the account is already a member leaves
// the invite usable.
func TestAcceptInvite(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	ownerID, orgID := ownerAndOrg(t, db, repo)
	account := func(email string) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Account', 'x') RETURNING id::text`, email,
		).Scan(&id))
		return id
	}
	first, second := account("first@toft.test"), account("second@toft.test")
	inv, err := repo.CreateInvite(ctx, orgID, ownerID, "viewer", "invite-link", time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = repo.AcceptInvite(ctx, inv.ID, orgID, ownerID, inv.Role)
	require.ErrorIs(t, err, org.ErrAlreadyMember)

	o, err := repo.AcceptInvite(ctx, inv.ID, orgID, first, inv.Role)
	require.NoError(t, err, "the refusal above rolled its claim back")
	require.Equal(t, orgID, o.ID)

	_, err = repo.AcceptInvite(ctx, inv.ID, orgID, second, inv.Role)
	require.ErrorIs(t, err, org.ErrInviteUsed)
	var members int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM org_members WHERE org_id = $1`, orgID).Scan(&members))
	require.Equal(t, 2, members, "the owner and the first account")
}

// An entry written for a request made with an API key records the key
// (D-100); one written for a session records none.
func TestWriteAuditLogRecordsTheAPIKey(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	userID, orgID := ownerAndOrg(t, db, repo)
	const keyID = "4b0f8f6e-7d0a-4c39-9d7e-2a1c5b9e8f10" // gitleaks:allow

	require.NoError(t, repo.WriteAuditLog(actor.WithAPIKey(ctx, keyID), orgID, userID, "project.created", "project:1", nil))
	require.NoError(t, repo.WriteAuditLog(ctx, orgID, userID, "project.deleted", "project:1", nil))

	got := map[string]*string{}
	rows, err := db.Query(ctx, `SELECT action, api_key_id::text FROM audit_log WHERE org_id = $1 AND agent_connection_id IS NULL`, orgID)
	require.NoError(t, err)
	for rows.Next() {
		var action string
		var key *string
		require.NoError(t, rows.Scan(&action, &key))
		got[action] = key
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 2)
	require.NotNil(t, got["project.created"])
	require.Equal(t, keyID, *got["project.created"])
	require.Nil(t, got["project.deleted"])
}

// ?api_key narrows the search and its count to entries made with a key, or
// without one, alone and together with agent=none (a person's own).
func TestAuditLogAPIKeyFilter(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	userID, orgID := ownerAndOrg(t, db, repo)
	const keyID = "4b0f8f6e-7d0a-4c39-9d7e-2a1c5b9e8f10" // gitleaks:allow
	var clientID, agentID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO oauth_clients (client_id, kind, name, redirect_uris) VALUES ('c', 'dcr', 'Claude Code', '{http://localhost/cb}') RETURNING id::text`,
	).Scan(&clientID))
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
		VALUES ($1, $2, $3, 'Claude Code', true, '{read}', now() + interval '1 day') RETURNING id::text`,
		orgID, userID, clientID).Scan(&agentID))

	require.NoError(t, repo.WriteAuditLog(actor.WithAPIKey(ctx, keyID), orgID, userID, "by.key", "project:1", nil))
	require.NoError(t, repo.WriteAuditLog(ctx, orgID, userID, "by.session", "project:1", nil))
	_, err := db.Exec(ctx, `INSERT INTO audit_log (org_id, actor_id, action, resource, agent_connection_id, api_key_id)
		VALUES ($1, $2, 'by.agent', 'project:1', $3, NULL), ($1, $2, 'by.agent_key', 'project:1', $3, $4::uuid)`, orgID, userID, agentID, keyID)
	require.NoError(t, err)

	svc := org.NewService(repo, zap.NewNop())
	for _, tc := range []struct {
		name string
		f    org.AuditLogFilter
		want []string
	}{
		{"everything", org.AuditLogFilter{}, []string{"by.agent", "by.agent_key", "by.key", "by.session"}},
		{"with a key", org.AuditLogFilter{APIKey: "any"}, []string{"by.agent_key", "by.key"}},
		{"without a key", org.AuditLogFilter{APIKey: "none"}, []string{"by.agent", "by.session"}},
		{"people with a key", org.AuditLogFilter{APIKey: "any", Agent: "none"}, []string{"by.key"}},
		{"people without a key", org.AuditLogFilter{APIKey: "none", Agent: "none"}, []string{"by.session"}},
	} {
		entries, err := svc.ListAuditLog(ctx, orgID, tc.f)
		require.NoError(t, err, tc.name)
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Action)
		}
		require.ElementsMatch(t, tc.want, got, tc.name)
		total, err := svc.CountAuditLog(ctx, orgID, tc.f)
		require.NoError(t, err, tc.name)
		require.Equal(t, len(tc.want), total, tc.name)
	}
}

// Toft's own acts (D-135) have no person: listed as "Toft" with an empty
// actor ID. An empty actor is still a mistake, not Toft.
func TestAuditLogToftActor(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	_, orgID := ownerAndOrg(t, db, repo)

	require.Error(t, repo.WriteAuditLog(ctx, orgID, "", "server.disk_cleaned", "server:1", nil))
	require.NoError(t, repo.WriteAuditLog(ctx, orgID, org.ToftActor, "server.disk_cleaned", "server:1", nil))
	entries, _, err := repo.ListAuditLog(ctx, orgID, org.AuditLogFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "", entries[0].ActorID)
	require.Equal(t, "Toft", entries[0].ActorName)
}

// Removing or demoting an owner expires the workspace's pending invites:
// owners read every pending link, so any of them could bring one back.
func TestOwnerRemovalExpiresInvites(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	ownerID, orgID := ownerAndOrg(t, db, repo)
	member := func(email, role string) string {
		t.Helper()
		var id string
		require.NoError(t, db.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Account', 'x') RETURNING id::text`, email).Scan(&id))
		_, err := db.Exec(ctx, `INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)`, orgID, id, role)
		require.NoError(t, err)
		return id
	}
	pending := func() int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(ctx,
			`SELECT count(*) FROM org_invites WHERE org_id = $1 AND used_at IS NULL AND expires_at > NOW()`, orgID).Scan(&n))
		return n
	}
	invite := func(role string) {
		t.Helper()
		_, err := repo.CreateInvite(ctx, orgID, ownerID, role, "link-"+role+time.Now().String(), time.Now().Add(time.Hour))
		require.NoError(t, err)
	}
	dev, other := member("dev@toft.test", "developer"), member("owner2@toft.test", "owner")

	invite("owner")
	_, err := repo.RemoveMember(ctx, orgID, dev)
	require.NoError(t, err)
	require.Equal(t, 1, pending(), "a developer never read the links")

	_, err = repo.UpdateMemberRole(ctx, orgID, other, "developer")
	require.NoError(t, err)
	require.Zero(t, pending(), "a demoted owner's links are dead")

	invite("viewer")
	_, err = repo.UpdateMemberRole(ctx, orgID, other, "owner")
	require.NoError(t, err)
	_, err = repo.RemoveMember(ctx, orgID, other)
	require.NoError(t, err)
	require.Zero(t, pending(), "a removed owner's links are dead")
}

// Home's checklist hidden (7g): read with the settings, the first time kept,
// and a later PUT of the settings keeps it.
func TestHideSetupKeepsTheFirstTime(t *testing.T) {
	db := testdb.New(t)
	repo := org.NewPostgresRepository(db, crypto.NewKeys(encryptionKey, ""))
	ctx := context.Background()
	_, orgID := ownerAndOrg(t, db, repo)

	s, err := repo.GetSettings(ctx, orgID)
	require.NoError(t, err)
	require.Nil(t, s.SetupHiddenAt)
	hidden, err := repo.HideSetup(ctx, orgID)
	require.NoError(t, err)
	require.True(t, hidden)
	first, err := repo.GetSettings(ctx, orgID)
	require.NoError(t, err)
	require.NotNil(t, first.SetupHiddenAt)
	hidden, err = repo.HideSetup(ctx, orgID)
	require.NoError(t, err)
	require.False(t, hidden, "hidden once: audited once")
	again, err := repo.UpdateSettings(ctx, orgID, org.OrgSettings{ResourceBypassMinRole: "owner", DefaultIsolation: org.IsolationStandard})
	require.NoError(t, err)
	require.Equal(t, first.SetupHiddenAt, again.SetupHiddenAt)
}
