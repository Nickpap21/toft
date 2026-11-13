package org

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

type postgresRepository struct {
	db   *pgxpool.Pool
	keys *crypto.Keys
}

// Delete removes the workspace with its audit log, in one transaction.
func (r *postgresRepository) Delete(ctx context.Context, id string) error {
	oid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid org id %q: %w", id, err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck — a no-op after Commit
	q := sqlcdb.New(tx)
	pid := pgtype.UUID{Bytes: oid, Valid: true}
	if err := q.DeleteOrgAuditLog(ctx, pid); err != nil {
		return fmt.Errorf("DeleteOrgAuditLog: %w", err)
	}
	n, err := q.DeleteOrganization(ctx, pid)
	if err != nil {
		return fmt.Errorf("DeleteOrganization: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// NewPostgresRepository returns a Repository backed by PostgreSQL.
func NewPostgresRepository(db *pgxpool.Pool, keys *crypto.Keys) Repository {
	return &postgresRepository{db: db, keys: keys}
}

// ─── Org ──────────────────────────────────────────────────────────────────────

func (r *postgresRepository) CreateWithOwner(ctx context.Context, name, ownerID string) (*Organization, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var o Organization
	if err := tx.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ($1) RETURNING id, name, created_at, updated_at`,
		name,
	).Scan(&o.ID, &o.Name, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)`,
		o.ID, ownerID, RoleOwner,
	); err != nil {
		return nil, fmt.Errorf("add owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET active_org_id = $1 WHERE id = $2`, o.ID, ownerID); err != nil {
		return nil, fmt.Errorf("set active org: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &o, nil
}

func (r *postgresRepository) GetByID(ctx context.Context, id string) (*Organization, error) {
	var o Organization
	err := r.db.QueryRow(ctx,
		`SELECT id, name, created_at, updated_at FROM organizations WHERE id = $1`,
		id,
	).Scan(&o.ID, &o.Name, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get org: %w", err)
	}
	return &o, nil
}

func (r *postgresRepository) UpdateName(ctx context.Context, id, name string) (*Organization, error) {
	var o Organization
	err := r.db.QueryRow(ctx,
		`UPDATE organizations SET name = $2, updated_at = NOW() WHERE id = $1
		 RETURNING id, name, created_at, updated_at`,
		id, name,
	).Scan(&o.ID, &o.Name, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update org name: %w", err)
	}
	return &o, nil
}

// ─── Membership ───────────────────────────────────────────────────────────────

func (r *postgresRepository) AddMember(ctx context.Context, orgID, userID, role string) (*Member, error) {
	var m Member
	err := r.db.QueryRow(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
		 RETURNING id, org_id, user_id, role, created_at`,
		orgID, userID, role,
	).Scan(&m.ID, &m.OrgID, &m.UserID, &m.Role, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("add member: %w", err)
	}
	return &m, nil
}

func (r *postgresRepository) AddMemberByEmail(ctx context.Context, orgID, email, role string) (*Member, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, ErrNotFound
	}
	q := sqlcdb.New(r.db)
	users, err := q.FindUsersByEmailCI(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("FindUsersByEmailCI: %w", err)
	}
	if len(users) == 0 {
		return nil, ErrNoSuchAccount
	}
	// Emails differing only by case are one person in practice; the exact
	// match wins, else the earliest account.
	u := users[0]
	for _, c := range users {
		if c.Email == email {
			u = c
		}
	}
	if !u.IsActive {
		return nil, ErrAccountInactive
	}
	err = q.InsertOrgMember(ctx, sqlcdb.InsertOrgMemberParams{OrgID: pgtype.UUID{Bytes: oid, Valid: true}, UserID: u.ID, Role: role})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrAlreadyMember
		}
		return nil, fmt.Errorf("InsertOrgMember: %w", err)
	}
	return &Member{OrgID: orgID, UserID: uuid.UUID(u.ID.Bytes).String(), UserEmail: u.Email, UserName: u.Name, Role: role, CreatedAt: time.Now()}, nil
}

func (r *postgresRepository) ListAddableAccounts(ctx context.Context, orgID string, allAccounts bool) ([]AddableAccount, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, ErrNotFound
	}
	rows, err := sqlcdb.New(r.db).ListAddableAccounts(ctx, sqlcdb.ListAddableAccountsParams{OrgID: pgtype.UUID{Bytes: oid, Valid: true}, AllAccounts: allAccounts})
	if err != nil {
		return nil, fmt.Errorf("ListAddableAccounts: %w", err)
	}
	out := make([]AddableAccount, 0, len(rows))
	for _, row := range rows {
		a := AddableAccount{UserID: uuid.UUID(row.ID.Bytes).String(), Name: row.Name, Email: row.Email, Former: row.Former, FormerRole: row.FormerRole}
		if row.LeftAt.Valid {
			t := row.LeftAt.Time
			a.LeftAt = &t
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *postgresRepository) GetActiveOrgID(ctx context.Context, userID string) (string, error) {
	var activeOrgID *string
	err := r.db.QueryRow(ctx,
		`SELECT active_org_id FROM users WHERE id = $1`,
		userID,
	).Scan(&activeOrgID)
	if err != nil {
		return "", fmt.Errorf("get active org id: %w", err)
	}
	if activeOrgID == nil {
		return "", nil
	}
	return *activeOrgID, nil
}

func (r *postgresRepository) GetMembershipInOrg(ctx context.Context, userID, orgID string) (string, error) {
	var role string
	err := r.db.QueryRow(ctx,
		`SELECT role FROM org_members WHERE user_id = $1 AND org_id = $2`,
		userID, orgID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get membership in org: %w", err)
	}
	return role, nil
}

func (r *postgresRepository) GetEarliestMembership(ctx context.Context, userID string) (string, string, error) {
	var orgID, role string
	err := r.db.QueryRow(ctx,
		`SELECT org_id, role FROM org_members WHERE user_id = $1 ORDER BY created_at ASC LIMIT 1`,
		userID,
	).Scan(&orgID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("get earliest membership: %w", err)
	}
	return orgID, role, nil
}

func (r *postgresRepository) SetActiveOrg(ctx context.Context, userID, orgID string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET active_org_id = $1 WHERE id = $2`, orgID, userID)
	if err != nil {
		return fmt.Errorf("set active org: %w", err)
	}
	return nil
}

func (r *postgresRepository) ListOrgsForUser(ctx context.Context, userID string) ([]OrgMembership, error) {
	rows, err := r.db.Query(ctx,
		`SELECT o.id, o.name, m.role
		 FROM org_members m
		 JOIN organizations o ON o.id = m.org_id
		 WHERE m.user_id = $1
		 ORDER BY m.created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list orgs for user: %w", err)
	}
	defer rows.Close()

	memberships := make([]OrgMembership, 0)
	for rows.Next() {
		var m OrgMembership
		if err := rows.Scan(&m.OrgID, &m.Name, &m.Role); err != nil {
			return nil, fmt.Errorf("scan org membership: %w", err)
		}
		memberships = append(memberships, m)
	}
	return memberships, rows.Err()
}

func (r *postgresRepository) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.id, m.org_id, m.user_id, u.email, u.name, m.role, m.created_at
		 FROM org_members m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.org_id = $1
		 ORDER BY m.created_at ASC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := make([]Member, 0)
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.OrgID, &m.UserID, &m.UserEmail, &m.UserName, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (r *postgresRepository) UpdateMemberRole(ctx context.Context, orgID, userID, role string) (string, error) {
	return r.changeMember(ctx, orgID, userID, role)
}

func (r *postgresRepository) RemoveMember(ctx context.Context, orgID, userID string) (string, error) {
	return r.changeMember(ctx, orgID, userID, "")
}

// changeMember sets a member's role, or removes them when role is empty,
// unless that would leave the workspace without an owner (ErrLastOwner). It
// locks the workspace's row first (READ COMMITTED; each statement after the
// lock sees changes committed before it was granted), so two owners demoting
// or removing each other at once can't both pass the check. The rule lives
// here, not in a database constraint: "at least one owner" spans rows, and
// the declarative schema carries no triggers.
func (r *postgresRepository) changeMember(ctx context.Context, orgID, userID, role string) (string, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return "", ErrNotFound
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", ErrNotFound
	}
	org := pgtype.UUID{Bytes: oid, Valid: true}
	user := pgtype.UUID{Bytes: uid, Valid: true}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck — a no-op after Commit
	q := sqlcdb.New(tx)
	if _, err := q.LockOrganization(ctx, org); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("lock workspace: %w", err)
	}
	current, err := q.GetMemberRole(ctx, sqlcdb.GetMemberRoleParams{OrgID: org, UserID: user})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotMember
		}
		return "", fmt.Errorf("get member role: %w", err)
	}
	if current == RoleOwner && role != RoleOwner {
		others, err := q.CountOtherOwners(ctx, sqlcdb.CountOtherOwnersParams{OrgID: org, UserID: user})
		if err != nil {
			return "", fmt.Errorf("count owners: %w", err)
		}
		if others == 0 {
			return "", ErrLastOwner
		}
	}
	if role == "" {
		err = q.DeleteMember(ctx, sqlcdb.DeleteMemberParams{OrgID: org, UserID: user})
	} else {
		err = q.SetMemberRole(ctx, sqlcdb.SetMemberRoleParams{OrgID: org, UserID: user, Role: role})
	}
	if err != nil {
		return "", fmt.Errorf("change member: %w", err)
	}
	if current == RoleOwner && role != RoleOwner {
		if err := q.ExpireOrgInvites(ctx, org); err != nil {
			return "", fmt.Errorf("expire invites: %w", err)
		}
	}
	return current, tx.Commit(ctx)
}

// ─── Invites ──────────────────────────────────────────────────────────────────

// inviteAD is where an invite's link is sealed for (D-126).
func inviteAD(id pgtype.UUID) string {
	return crypto.AD("org_invites.token_encrypted", uuid.UUID(id.Bytes).String())
}

// CreateInvite stores the link's sha256, for lookup, and the link encrypted,
// so owners can copy it again; never the link as it is.
func (r *postgresRepository) CreateInvite(ctx context.Context, orgID, createdBy, role, token string, expiresAt time.Time) (*Invite, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	uid, err := uuid.Parse(createdBy)
	if err != nil {
		return nil, fmt.Errorf("invalid user id %q: %w", createdBy, err)
	}
	// The ID is made here, so the link is sealed for it (D-126).
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	enc, err := r.keys.Seal(inviteAD(id), token)
	if err != nil {
		return nil, fmt.Errorf("encrypt invite: %w", err)
	}
	row, err := sqlcdb.New(r.db).InsertInvite(ctx, sqlcdb.InsertInviteParams{
		ID:             id,
		OrgID:          pgtype.UUID{Bytes: oid, Valid: true},
		CreatedBy:      pgtype.UUID{Bytes: uid, Valid: true},
		Token:          HashInviteToken(token),
		TokenEncrypted: &enc,
		Role:           role,
		ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("InsertInvite: %w", err)
	}
	return &Invite{
		ID:        uuid.UUID(row.ID.Bytes).String(),
		OrgID:     uuid.UUID(row.OrgID.Bytes).String(),
		CreatedBy: uuid.UUID(row.CreatedBy.Bytes).String(),
		Token:     token,
		Role:      row.Role,
		ExpiresAt: row.ExpiresAt.Time,
		CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (r *postgresRepository) GetInviteByToken(ctx context.Context, token string) (*Invite, error) {
	row, err := sqlcdb.New(r.db).GetInviteByToken(ctx, HashInviteToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invite by token: %w", err)
	}
	inv := &Invite{
		ID:            uuid.UUID(row.ID.Bytes).String(),
		OrgID:         uuid.UUID(row.OrgID.Bytes).String(),
		CreatedBy:     uuid.UUID(row.CreatedBy.Bytes).String(),
		Token:         token,
		Role:          row.Role,
		ExpiresAt:     row.ExpiresAt.Time,
		CreatedAt:     row.CreatedAt.Time,
		CreatorActive: row.CreatorActive,
		CreatorName:   row.CreatorName,
	}
	if row.UsedAt.Valid {
		inv.UsedAt = &row.UsedAt.Time
	}
	return inv, nil
}

func (r *postgresRepository) ListInvites(ctx context.Context, orgID string) ([]Invite, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	rows, err := sqlcdb.New(r.db).ListPendingInvites(ctx, pgtype.UUID{Bytes: oid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListPendingInvites: %w", err)
	}
	invites := make([]Invite, 0, len(rows))
	for _, row := range rows {
		inv := Invite{
			ID:        uuid.UUID(row.ID.Bytes).String(),
			OrgID:     uuid.UUID(row.OrgID.Bytes).String(),
			CreatedBy: uuid.UUID(row.CreatedBy.Bytes).String(),
			Role:      row.Role,
			ExpiresAt: row.ExpiresAt.Time,
			CreatedAt: row.CreatedAt.Time,
		}
		// NULL only on a row from before 0054 not yet converted at start.
		if row.TokenEncrypted != nil {
			if inv.Token, err = r.keys.Open(inviteAD(row.ID), *row.TokenEncrypted); err != nil {
				return nil, fmt.Errorf("decrypt invite %s: %w", inv.ID, err)
			}
		}
		invites = append(invites, inv)
	}
	return invites, nil
}

// HashUnhashedInvites converts the invites from before 0054, which hold the
// link itself, to its sha256 and the link encrypted.
func (r *postgresRepository) HashUnhashedInvites(ctx context.Context) (int64, error) {
	q := sqlcdb.New(r.db)
	rows, err := q.ListUnhashedInvites(ctx)
	if err != nil {
		return 0, fmt.Errorf("ListUnhashedInvites: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	params := sqlcdb.HashInvitesParams{
		Ids:             make([]pgtype.UUID, 0, len(rows)),
		TokenHashes:     make([]string, 0, len(rows)),
		TokensEncrypted: make([]string, 0, len(rows)),
	}
	for _, row := range rows {
		enc, err := r.keys.Seal(inviteAD(row.ID), row.Token)
		if err != nil {
			return 0, fmt.Errorf("encrypt invite: %w", err)
		}
		params.Ids = append(params.Ids, row.ID)
		params.TokenHashes = append(params.TokenHashes, HashInviteToken(row.Token))
		params.TokensEncrypted = append(params.TokensEncrypted, enc)
	}
	n, err := q.HashInvites(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("HashInvites: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) MarkInviteUsed(ctx context.Context, inviteID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE org_invites SET used_at = NOW() WHERE id = $1`,
		inviteID,
	)
	return err
}

func (r *postgresRepository) RevokeInvite(ctx context.Context, orgID, inviteID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM org_invites WHERE id = $1 AND org_id = $2`,
		inviteID, orgID,
	)
	return err
}

// AcceptInvite claims inviteID and adds userID to orgID as role within a
// single transaction. The claim only succeeds while the invite is unused,
// unexpired and its creator is still active, so two concurrent redemptions
// cannot both succeed (the loser gets ErrInviteUsed) and a deactivated user's
// links are dead (ErrInviteExpired). An existing member gets ErrAlreadyMember
// and the whole transaction rolls back, leaving the invite unconsumed and the
// member's role unchanged.
func (r *postgresRepository) AcceptInvite(ctx context.Context, inviteID, orgID, userID, role string) (*Organization, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	claimed, err := tx.Exec(ctx,
		`UPDATE org_invites SET used_at = NOW()
		 WHERE id = $1 AND used_at IS NULL AND expires_at > NOW()
		   AND EXISTS (SELECT 1 FROM users u WHERE u.id = org_invites.created_by AND u.is_active)`,
		inviteID,
	)
	if err != nil {
		return nil, fmt.Errorf("claim invite: %w", err)
	}
	if claimed.RowsAffected() == 0 {
		// Used by a concurrent redemption, or else expired or its creator
		// deactivated since ValidateInvite; both of those read as expired.
		var used bool
		if err := tx.QueryRow(ctx,
			`SELECT used_at IS NOT NULL FROM org_invites WHERE id = $1`, inviteID,
		).Scan(&used); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("read invite state: %w", err)
		}
		if used {
			return nil, ErrInviteUsed
		}
		return nil, ErrInviteExpired
	}

	added, err := tx.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (org_id, user_id) DO NOTHING`,
		orgID, userID, role,
	)
	if err != nil {
		return nil, fmt.Errorf("add member: %w", err)
	}
	if added.RowsAffected() == 0 {
		return nil, ErrAlreadyMember
	}

	var o Organization
	if err := tx.QueryRow(ctx,
		`SELECT id, name, created_at, updated_at FROM organizations WHERE id = $1`,
		orgID,
	).Scan(&o.ID, &o.Name, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, fmt.Errorf("get org: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return &o, nil
}

// ─── Audit log ────────────────────────────────────────────────────────────────

func (r *postgresRepository) WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string, detail []byte) error {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	// ToftActor: Toft itself acted (an automatic disk clean, D-135).
	var actorUUID pgtype.UUID
	if actorID != ToftActor {
		aid, err := uuid.Parse(actorID)
		if err != nil {
			return fmt.Errorf("invalid actor id %q: %w", actorID, err)
		}
		actorUUID = pgtype.UUID{Bytes: aid, Valid: true}
	}
	// Who acted through what, from the request (D-100).
	var agentID, keyID pgtype.UUID
	if a, ok := actor.AgentFrom(ctx); ok {
		if id, err := uuid.Parse(a.ConnectionID); err == nil {
			agentID = pgtype.UUID{Bytes: id, Valid: true}
		}
	} else if id, err := uuid.Parse(actor.APIKeyFrom(ctx)); err == nil {
		keyID = pgtype.UUID{Bytes: id, Valid: true}
	}
	return sqlcdb.New(r.db).WriteAuditEntry(ctx, sqlcdb.WriteAuditEntryParams{
		OrgID: pgtype.UUID{Bytes: oid, Valid: true}, ActorID: actorUUID,
		Action: action, Resource: &resource, Metadata: detail, AgentConnectionID: agentID, ApiKeyID: keyID,
	})
}

func (r *postgresRepository) ListAuditLog(ctx context.Context, orgID string, filter AuditLogFilter) ([]AuditEntry, int, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	actions := filter.Actions
	if filter.Action != "" {
		actions = append(actions, filter.Action)
	}
	if actions == nil {
		actions = []string{}
	}
	p := sqlcdb.ListAuditEntriesParams{
		OrgID: pgtype.UUID{Bytes: oid, Valid: true}, Actions: actions, Resource: filter.Resource,
		Q: filter.Search, RowLimit: int32(filter.Limit), Agent: filter.Agent, APIKey: filter.APIKey,
	}
	if filter.ActorID != "" {
		id, err := uuid.Parse(filter.ActorID)
		if err != nil {
			return nil, 0, ErrInvalidFilter
		}
		p.ActorID = pgtype.UUID{Bytes: id, Valid: true}
	}
	if filter.Since != nil {
		p.Since = pgtype.Timestamptz{Time: *filter.Since, Valid: true}
	}
	if filter.Before != nil {
		id, err := uuid.Parse(filter.BeforeID)
		if err != nil {
			return nil, 0, ErrInvalidFilter
		}
		p.BeforeAt = pgtype.Timestamptz{Time: *filter.Before, Valid: true}
		p.BeforeID = pgtype.UUID{Bytes: id, Valid: true}
	}
	rows, err := sqlcdb.New(r.db).ListAuditEntries(ctx, p)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit log: %w", err)
	}
	entries := make([]AuditEntry, 0, len(rows))
	total := 0
	for _, row := range rows {
		total = int(row.Total)
		detail := map[string]any{}
		_ = json.Unmarshal(row.Metadata, &detail)
		raw := json.RawMessage(row.Metadata)
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		target := TargetOf(row.Action, AuditRow{
			Kind: row.Kind, Target: row.Target, Detail: detail,
			ServiceID: uuidPtr(row.ServiceID), ServiceName: row.ServiceName,
			ServiceProjectID: uuidPtr(row.ServiceProjectID), ServiceProjectName: row.ServiceProjectName,
			ServiceEnvironment: row.ServiceEnvironment, ServerName: row.ServerName, ProjectName: row.ProjectName,
			EnvironmentName: row.EnvironmentName, EnvironmentProjectID: uuidPtr(row.EnvironmentProjectID),
			EnvironmentProjectName: row.EnvironmentProjectName, UserName: row.UserName, KeyName: row.KeyName,
			OtherOrgName:   row.OtherOrgName,
			RepositoryName: row.RepositoryName, RepositoryProjectID: uuidPtr(row.RepositoryProjectID),
			RepositoryProjectName: row.RepositoryProjectName, DocPageTitle: row.DocPageTitle,
			DocPageProjectID: uuidPtr(row.DocPageProjectID), DocPageProjectName: row.DocPageProjectName,
		})
		entries = append(entries, AuditEntry{
			ID: uuid.UUID(row.ID.Bytes).String(), OrgID: orgID, ActorID: actorIDOf(row.ActorID),
			ActorName: row.ActorName, ActorEmail: row.ActorEmail, Action: row.Action,
			Resource: target.Name, CreatedAt: row.CreatedAt.Time.Format(time.RFC3339Nano),
			Target: target, Detail: raw,
			AgentID: uuidPtr(row.AgentConnectionID), AgentName: row.AgentName,
			APIKeyID: uuidPtr(row.ApiKeyID), APIKeyName: row.ViaKeyName,
		})
	}
	return entries, total, nil
}

// actorIDOf is an entry's actor, "" for Toft's own (ToftActor).
func actorIDOf(p pgtype.UUID) string {
	if !p.Valid {
		return ""
	}
	return uuid.UUID(p.Bytes).String()
}

func uuidPtr(p pgtype.UUID) *string {
	if !p.Valid {
		return nil
	}
	s := uuid.UUID(p.Bytes).String()
	return &s
}

// ─── Settings ─────────────────────────────────────────────────────────────────

func (r *postgresRepository) GetSettings(ctx context.Context, orgID string) (*OrgSettings, error) {
	id, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("get org settings: %w", ErrNotFound)
	}
	row, err := sqlcdb.New(r.db).GetOrgSettings(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("get org settings: %w", err)
	}
	s := OrgSettings{ResourceBypassMinRole: row.ResourceBypassMinRole, DefaultIsolation: row.DefaultIsolation}
	if row.SetupHiddenAt.Valid {
		s.SetupHiddenAt = &row.SetupHiddenAt.Time
	}
	return &s, nil
}

func (r *postgresRepository) HideSetup(ctx context.Context, orgID string) (bool, error) {
	id, err := uuid.Parse(orgID)
	if err != nil {
		return false, fmt.Errorf("hide setup: %w", ErrNotFound)
	}
	n, err := sqlcdb.New(r.db).HideOrgSetup(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return false, fmt.Errorf("hide setup: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) UpdateSettings(ctx context.Context, orgID string, settings OrgSettings) (*OrgSettings, error) {
	id, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("update org settings: %w", ErrNotFound)
	}
	n, err := sqlcdb.New(r.db).UpdateOrgSettings(ctx, sqlcdb.UpdateOrgSettingsParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, ResourceBypassMinRole: settings.ResourceBypassMinRole, DefaultIsolation: settings.DefaultIsolation,
	})
	if err != nil {
		return nil, fmt.Errorf("update org settings: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("update org settings: %w", ErrNotFound)
	}
	return r.GetSettings(ctx, orgID)
}

func (r *postgresRepository) CountDefaultIsolationServices(ctx context.Context, orgID string) (map[string]int, error) {
	id, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("count services: %w", ErrNotFound)
	}
	rows, err := sqlcdb.New(r.db).CountDefaultIsolationServices(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("count services following the default isolation: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		key := ""
		if row.ServerID.Valid {
			key = uuid.UUID(row.ServerID.Bytes).String()
		}
		out[key] = int(row.Services)
	}
	return out, nil
}

// ─── Token helper ─────────────────────────────────────────────────────────────

// GenerateInviteToken returns a cryptographically random URL-safe token.
func GenerateInviteToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// HashInviteToken is what org_invites.token holds for a link: its sha256.
func HashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (r *postgresRepository) DeleteAuditLogBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := sqlcdb.New(r.db).DeleteAuditLogBefore(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("delete audit log before %s: %w", cutoff.Format(time.RFC3339), err)
	}
	return n, nil
}
