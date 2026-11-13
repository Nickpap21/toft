package org

import (
	"context"
	"time"
)

// Repository defines the data-access contract for the org domain.
type Repository interface {
	// Org CRUD
	// CreateWithOwner creates an org, adds ownerID as its owner and makes it
	// their active workspace, in one transaction: a failure leaves nothing.
	CreateWithOwner(ctx context.Context, name, ownerID string) (*Organization, error)
	GetByID(ctx context.Context, id string) (*Organization, error)
	UpdateName(ctx context.Context, id, name string) (*Organization, error)
	// Delete removes the workspace's row, its audit log and what cascades
	// from it, in one transaction.
	Delete(ctx context.Context, id string) error

	// Membership
	AddMember(ctx context.Context, orgID, userID, role string) (*Member, error)
	// AddMemberByEmail adds the active account with that email (case-
	// insensitive) as role; ErrNoSuchAccount, ErrAccountInactive,
	// ErrAlreadyMember.
	AddMemberByEmail(ctx context.Context, orgID, email, role string) (*Member, error)
	// ListAddableAccounts: the workspace's former members, plus every other
	// active account when allAccounts is set; never current members.
	ListAddableAccounts(ctx context.Context, orgID string, allAccounts bool) ([]AddableAccount, error)
	ListMembers(ctx context.Context, orgID string) ([]Member, error)
	// UpdateMemberRole and RemoveMember return the member's previous role.
	UpdateMemberRole(ctx context.Context, orgID, userID, role string) (string, error)
	RemoveMember(ctx context.Context, orgID, userID string) (string, error)

	// Active workspace resolution
	GetActiveOrgID(ctx context.Context, userID string) (orgID string, err error)
	GetMembershipInOrg(ctx context.Context, userID, orgID string) (role string, err error)
	GetEarliestMembership(ctx context.Context, userID string) (orgID, role string, err error)
	SetActiveOrg(ctx context.Context, userID, orgID string) error
	ListOrgsForUser(ctx context.Context, userID string) ([]OrgMembership, error)

	// Invites
	CreateInvite(ctx context.Context, orgID, createdBy, role, token string, expiresAt time.Time) (*Invite, error)
	GetInviteByToken(ctx context.Context, token string) (*Invite, error)
	ListInvites(ctx context.Context, orgID string) ([]Invite, error)
	MarkInviteUsed(ctx context.Context, inviteID string) error
	RevokeInvite(ctx context.Context, orgID, inviteID string) error
	// HashUnhashedInvites converts invites from before 0054, which hold the
	// link itself, and returns how many it converted.
	HashUnhashedInvites(ctx context.Context) (int64, error)
	// AcceptInvite claims inviteID (only while unused, unexpired and its
	// creator is active; else ErrInviteUsed or ErrInviteExpired) and adds
	// userID to orgID as role, atomically. An existing member gets
	// ErrAlreadyMember and the invite stays unconsumed.
	AcceptInvite(ctx context.Context, inviteID, orgID, userID, role string) (*Organization, error)

	// Audit log
	// WriteAuditLog records an action; detail is JSON or nil.
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string, detail []byte) error
	// ListAuditLog returns a page of entries and how many match in all.
	ListAuditLog(ctx context.Context, orgID string, filter AuditLogFilter) ([]AuditEntry, int, error)
	// DeleteAuditLogBefore removes every entry older than cutoff, in every
	// workspace, and reports how many.
	DeleteAuditLogBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// Settings
	GetSettings(ctx context.Context, orgID string) (*OrgSettings, error)
	UpdateSettings(ctx context.Context, orgID string, settings OrgSettings) (*OrgSettings, error)
	// HideSetup hides Home's checklist for the workspace; the first time is
	// kept, and hidden is true only for the call that hid it.
	HideSetup(ctx context.Context, orgID string) (hidden bool, err error)
	// CountDefaultIsolationServices counts the workspace's services that
	// follow its default isolation, by server ID ("" the panel's own), with
	// an entry (perhaps 0) for every server it has services on.
	CountDefaultIsolationServices(ctx context.Context, orgID string) (map[string]int, error)
}
