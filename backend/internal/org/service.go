package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"go.uber.org/zap"
)

var (
	ErrInviteNotFound   = errors.New("invite not found")
	ErrInviteExpired    = errors.New("invite has expired")
	ErrInviteUsed       = errors.New("invite has already been used")
	ErrCannotRemoveSelf = errors.New("you can't remove yourself: leave the workspace instead")
	ErrInvalidSettings  = errors.New("invalid settings")
	ErrNotMember        = errors.New("not a member of this workspace")
	ErrOperatorOnly     = errors.New("only instance operators can create workspaces")
	ErrInvalidRole      = errors.New("role must be owner, developer, or viewer")
	ErrAlreadyMember    = errors.New("already a member of this workspace")
	// ErrNoSuchAccount, ErrAccountInactive: adding a member by email (D-56).
	ErrNoSuchAccount   = errors.New("no account on this instance has that email — send an invite link instead")
	ErrAccountInactive = errors.New("that account is deactivated; an instance operator can reactivate it")
	// ErrInvalidFilter: an audit log filter with a malformed ID.
	ErrInvalidFilter = errors.New("invalid audit log filter")
	// ErrLastOwner: the change would leave the workspace without an owner.
	ErrLastOwner = errors.New("a workspace needs an owner: make another member an owner first")
)

// DefaultWorkspaceName is used when a workspace is created without a name.
const DefaultWorkspaceName = "My Workspace"

// OperatorChecker reports whether a user is an instance operator.
// Satisfied by instance.Service.
type OperatorChecker interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
}

// Service contains the business logic for the org domain.
type Service struct {
	repo      Repository
	log       *zap.Logger
	operators OperatorChecker
	contents  WorkspaceContents
	sandboxes ServerSandboxes
}

// ServerSandboxes lists a workspace's servers with their Sandboxed level
// (D-95), and the panel's own server's (serverID "", D-97). Satisfied by
// server.Service.
type ServerSandboxes interface {
	SandboxedLevels(ctx context.Context, orgID string) ([]server.SandboxedLevel, error)
	Sandboxed(ctx context.Context, serverID string) (server.Level, string, error)
	SandboxedStrict(ctx context.Context, serverID string) (server.Level, string, error)
}

// WithServerSandboxes lets Isolation say where Sandboxed services can run.
func (s *Service) WithServerSandboxes(r ServerSandboxes) *Service {
	s.sandboxes = r
	return s
}

// WorkspaceContents deletes everything a workspace has besides its own rows:
// its services with their containers and data, its shared volumes, its
// servers' entries and its projects. Satisfied by a main.go adapter, since
// those modules import this one.
type WorkspaceContents interface {
	DeleteAll(ctx context.Context, orgID string) error
}

// WithWorkspaceContents enables DeleteWorkspace. Called from main.go.
func (s *Service) WithWorkspaceContents(c WorkspaceContents) {
	s.contents = c
}

// ErrWorkspaceInUse: another workspace has services on one of this
// workspace's servers (a share), so it cannot be deleted yet. Nothing is
// deleted then.
var ErrWorkspaceInUse = errors.New("another workspace runs services on one of this workspace's servers: remove them, or the share, first")

// ErrContentsNotRemoved: a service could not be removed from its server, so
// the workspace was not deleted; what was deleted before it stays deleted.
var ErrContentsNotRemoved = errors.New("the workspace's contents could not all be removed")

// ErrConfirmName: the name typed to confirm a workspace delete is not the
// workspace's.
var ErrConfirmName = errors.New("type the workspace's name exactly to delete it")

// DeleteWorkspace deletes the workspace and everything in it: its services
// are removed from their servers (data included), then its shared volumes,
// its servers' entries (the machines are not touched), its projects, and
// last its own rows: members, invites, settings, alert webhook, audit log.
// confirm must be the workspace's name. The caller checks the actor is one
// of its owners.
func (s *Service) DeleteWorkspace(ctx context.Context, orgID, confirm string) error {
	o, err := s.repo.GetByID(ctx, orgID)
	if err != nil {
		return err
	}
	if confirm != o.Name {
		return ErrConfirmName
	}
	if s.contents == nil {
		return errors.New("deleting a workspace is not configured")
	}
	if err := s.contents.DeleteAll(ctx, orgID); err != nil {
		return err
	}
	return s.repo.Delete(ctx, orgID)
}

// NewService creates an org Service.
func NewService(repo Repository, log *zap.Logger) *Service {
	return &Service{repo: repo, log: log}
}

// WithOperatorChecker enables workspace creation for instance operators.
// Without a checker, CreateWorkspace denies everyone (fail closed).
func (s *Service) WithOperatorChecker(c OperatorChecker) *Service {
	s.operators = c
	return s
}

// CreateWorkspace creates a workspace owned by actorID and makes it their
// active one. Only instance operators may create workspaces (D-21); a missing
// checker or a failed check denies with ErrOperatorOnly.
func (s *Service) CreateWorkspace(ctx context.Context, actorID, name string) (*Organization, error) {
	if s.operators == nil {
		return nil, ErrOperatorOnly
	}
	ok, err := s.operators.IsOperator(ctx, actorID)
	if err != nil {
		s.log.Warn("operator check failed; denying workspace creation", zap.Error(err), zap.String("user_id", actorID))
		return nil, ErrOperatorOnly
	}
	if !ok {
		return nil, ErrOperatorOnly
	}
	if name == "" {
		name = DefaultWorkspaceName
	}
	// One transaction: a failure part-way must not leave an org with no
	// members, which nobody could reach or delete.
	o, err := s.repo.CreateWithOwner(ctx, name, actorID)
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	return o, nil
}

// IsValidRole reports whether role is one of the org roles.
func IsValidRole(role string) bool {
	switch role {
	case RoleOwner, RoleDeveloper, RoleViewer:
		return true
	default:
		return false
	}
}

// InviteErrorMessage returns the user-facing message for an invite error
// (not found, expired, used), and false for any other error. Shared by the
// org handlers and registration, so the wording stays the same everywhere.
func InviteErrorMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInviteNotFound):
		return "this invite link doesn't exist — check that you copied the full link", true
	case errors.Is(err, ErrInviteExpired):
		return "this invite has expired — ask the sender for a new link", true
	case errors.Is(err, ErrInviteUsed):
		return "this invite has already been used", true
	default:
		return "", false
	}
}

// InviteErrorCode is the stable code for an invite error, sent next to
// InviteErrorMessage's text so clients tell the cases apart without
// reading it: invite_not_found, invite_expired or invite_used; "" for any
// other error.
func InviteErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInviteNotFound):
		return "invite_not_found"
	case errors.Is(err, ErrInviteExpired):
		return "invite_expired"
	case errors.Is(err, ErrInviteUsed):
		return "invite_used"
	default:
		return ""
	}
}

// GetByID returns an org by its ID.
func (s *Service) GetByID(ctx context.Context, id string) (*Organization, error) {
	return s.repo.GetByID(ctx, id)
}

// RoleIn returns the user's role in orgID; ErrNotFound if they are not a
// member. Used by the auth middleware for project-scoped API keys.
func (s *Service) RoleIn(ctx context.Context, userID, orgID string) (string, error) {
	return s.repo.GetMembershipInOrg(ctx, userID, orgID)
}

// GetForUser returns the orgID and role of the user's active workspace. Used
// by the auth middleware on every request. Resolves via users.active_org_id;
// if that's unset, or points to a membership that no longer exists (e.g. the
// user was removed from that org), falls back to the earliest membership and
// self-heals active_org_id to it.
func (s *Service) GetForUser(ctx context.Context, userID string) (orgID, role string, err error) {
	activeOrgID, err := s.repo.GetActiveOrgID(ctx, userID)
	if err != nil {
		return "", "", fmt.Errorf("get active org id: %w", err)
	}

	if activeOrgID != "" {
		role, err := s.repo.GetMembershipInOrg(ctx, userID, activeOrgID)
		if err == nil {
			return activeOrgID, role, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return "", "", fmt.Errorf("get membership in active org: %w", err)
		}
		// active_org_id is stale (membership removed) — fall through to self-heal.
	}

	orgID, role, err = s.repo.GetEarliestMembership(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if healErr := s.repo.SetActiveOrg(ctx, userID, orgID); healErr != nil {
		s.log.Warn("failed to self-heal active org", zap.Error(healErr), zap.String("user_id", userID))
	}
	return orgID, role, nil
}

// ListOrgsForUser returns every org the user belongs to, for the workspace switcher.
func (s *Service) ListOrgsForUser(ctx context.Context, userID string) ([]OrgMembership, error) {
	return s.repo.ListOrgsForUser(ctx, userID)
}

// SwitchActiveOrg makes orgID the user's active workspace. Returns ErrNotMember
// if the user isn't a member of orgID — switching never grants membership.
func (s *Service) SwitchActiveOrg(ctx context.Context, userID, orgID string) (*Organization, string, error) {
	role, err := s.repo.GetMembershipInOrg(ctx, userID, orgID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, "", ErrNotMember
		}
		return nil, "", fmt.Errorf("check membership: %w", err)
	}
	if err := s.repo.SetActiveOrg(ctx, userID, orgID); err != nil {
		return nil, "", fmt.Errorf("set active org: %w", err)
	}
	o, err := s.repo.GetByID(ctx, orgID)
	if err != nil {
		return nil, "", fmt.Errorf("get org: %w", err)
	}
	return o, role, nil
}

// UpdateName changes the org's display name.
func (s *Service) UpdateName(ctx context.Context, orgID, name string) (*Organization, error) {
	if name == "" {
		return nil, fmt.Errorf("org name is required")
	}
	return s.repo.UpdateName(ctx, orgID, name)
}

// ListMembers returns all members of an org with their resolved user info.
func (s *Service) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	return s.repo.ListMembers(ctx, orgID)
}

// UpdateMemberRole changes a member's role within the org. ErrNotMember when
// the user isn't one; ErrLastOwner when it would leave the workspace without
// an owner.
func (s *Service) UpdateMemberRole(ctx context.Context, orgID, targetUserID, role string) (previous string, err error) {
	if !IsValidRole(role) {
		return "", ErrInvalidRole
	}
	return s.repo.UpdateMemberRole(ctx, orgID, targetUserID, role)
}

// AddMemberByEmail adds an existing, active account to the org (D-56): the
// way back in for someone removed earlier, or anyone who already signs in to
// this instance. ErrNoSuchAccount when no account has the email (an invite
// link is the path then), ErrAccountInactive, ErrAlreadyMember (the role is
// left as it is).
func (s *Service) AddMemberByEmail(ctx context.Context, orgID, email, role string) (*Member, error) {
	if !IsValidRole(role) {
		return nil, ErrInvalidRole
	}
	return s.repo.AddMemberByEmail(ctx, orgID, strings.TrimSpace(email), role)
}

// ListAddableAccounts suggests who an owner can add by email (D-58): the
// workspace's former members for everyone, and every account on the instance
// when the caller is an instance operator, who sees that list anyway. A
// workspace owner who is not an operator never enumerates other workspaces'
// people.
func (s *Service) ListAddableAccounts(ctx context.Context, orgID, actorID string) ([]AddableAccount, error) {
	all := false
	if s.operators != nil {
		ok, err := s.operators.IsOperator(ctx, actorID)
		if err != nil {
			return nil, fmt.Errorf("check operator: %w", err)
		}
		all = ok
	}
	return s.repo.ListAddableAccounts(ctx, orgID, all)
}

// RemoveMember removes another member from the org; members leave with
// Leave. ErrNotMember and ErrLastOwner as UpdateMemberRole.
func (s *Service) RemoveMember(ctx context.Context, orgID, actorID, targetUserID string) (previous string, err error) {
	if actorID == targetUserID {
		return "", ErrCannotRemoveSelf
	}
	return s.repo.RemoveMember(ctx, orgID, targetUserID)
}

// Leave removes the user from the workspace, unless they are its last owner
// (ErrLastOwner). Their next request opens another workspace of theirs, or
// none (GetForUser).
func (s *Service) Leave(ctx context.Context, orgID, userID string) (previous string, err error) {
	return s.repo.RemoveMember(ctx, orgID, userID)
}

// CreateInvite generates a 7-day invite token for the given role.
func (s *Service) CreateInvite(ctx context.Context, orgID, createdBy, role string) (*Invite, error) {
	if !IsValidRole(role) {
		return nil, ErrInvalidRole
	}
	token, err := GenerateInviteToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	return s.repo.CreateInvite(ctx, orgID, createdBy, role, token, expiresAt)
}

// ListInvites returns all pending (unused, unexpired) invites for an org.
func (s *Service) ListInvites(ctx context.Context, orgID string) ([]Invite, error) {
	return s.repo.ListInvites(ctx, orgID)
}

// RevokeInvite deletes an invite so it can no longer be accepted.
func (s *Service) RevokeInvite(ctx context.Context, orgID, inviteID string) error {
	return s.repo.RevokeInvite(ctx, orgID, inviteID)
}

// ValidateInvite checks that a token is valid and unexpired without consuming it.
func (s *Service) ValidateInvite(ctx context.Context, token string) (*Invite, error) {
	inv, err := s.repo.GetInviteByToken(ctx, token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrInviteNotFound
		}
		return nil, fmt.Errorf("validate invite: %w", err)
	}
	if inv.UsedAt != nil {
		return nil, ErrInviteUsed
	}
	// A deactivated user's invites are dead (instance deactivation expires
	// them too). Reported as expired: the invitee needs a new link either way,
	// and this does not reveal the creator's account state.
	if time.Now().After(inv.ExpiresAt) || !inv.CreatorActive {
		return nil, ErrInviteExpired
	}
	return inv, nil
}

// AcceptInvite adds the authenticated user to the org identified by the token.
// Membership and invite-consumption are applied atomically at the repo layer:
// the invite is claimed only if still unused and unexpired, so concurrent
// redemptions cannot both succeed (ErrInviteUsed), and an existing member gets
// ErrAlreadyMember with the invite left unconsumed — redeeming an invite
// never changes an existing member's role.
func (s *Service) AcceptInvite(ctx context.Context, token, userID string) (*Organization, error) {
	inv, err := s.ValidateInvite(ctx, token)
	if err != nil {
		return nil, err
	}

	o, err := s.repo.AcceptInvite(ctx, inv.ID, inv.OrgID, userID, inv.Role)
	if err != nil {
		return nil, err
	}
	s.WriteAuditLogDetail(ctx, inv.OrgID, userID, "member.joined", "user:"+userID, map[string]any{"role": inv.Role, "invite": inv.ID})
	return o, nil
}

// WriteAuditLog records an action in the audit log (best-effort; errors are logged, not returned).
func (s *Service) WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string) {
	s.WriteAuditLogDetail(ctx, orgID, actorID, action, resource, nil)
}

// ToftActor is the actor ID of what Toft does on its own (an automatic
// disk clean, D-135): the entry has no person and lists as "Toft". Any
// other actor ID must be a user's.
const ToftActor = "toft"

// WriteAuditLogDetail records an action with what the audit log's Details
// column shows for it (a role, rules, a key's scope, a name a deleted
// object had). Best-effort, as WriteAuditLog.
func (s *Service) WriteAuditLogDetail(ctx context.Context, orgID, actorID, action, resource string, detail map[string]any) {
	var raw []byte
	if len(detail) > 0 {
		var err error
		if raw, err = json.Marshal(detail); err != nil {
			s.log.Warn("audit log detail not encoded", zap.Error(err), zap.String("action", action))
			raw = nil
		}
	}
	if err := s.repo.WriteAuditLog(ctx, orgID, actorID, action, resource, raw); err != nil {
		s.log.Warn("audit log write failed", zap.Error(err), zap.String("action", action))
	}
}

// RecordAuditLog records an action and reports a failed write, for what
// may only happen once it is recorded (an admin password shown, D-160).
func (s *Service) RecordAuditLog(ctx context.Context, orgID, actorID, action, resource string) error {
	if err := s.repo.WriteAuditLog(ctx, orgID, actorID, action, resource, nil); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	return nil
}

// RunAuditRetention deletes audit entries older than keep, once at start
// and then daily, until ctx ends. Zero or negative keep disables it.
func (s *Service) RunAuditRetention(ctx context.Context, keep time.Duration) {
	if keep <= 0 {
		return
	}
	prune := func() {
		n, err := s.repo.DeleteAuditLogBefore(ctx, time.Now().Add(-keep))
		if err != nil {
			s.log.Warn("audit log retention", zap.Error(err))
		} else if n > 0 {
			s.log.Info("audit log retention", zap.Int64("deleted", n), zap.Duration("kept", keep))
		}
	}
	prune()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// ListAuditLog returns audit log entries for an org, newest first, filtered
// (AuditLogFilter; the search covers the actor and what the entry is about).
// Limit is clamped to [1, 200]; 0 defaults to 50.
func (s *Service) ListAuditLog(ctx context.Context, orgID string, filter AuditLogFilter) ([]AuditEntry, error) {
	filter.Limit = clampLimit(filter.Limit)
	entries, _, err := s.repo.ListAuditLog(ctx, orgID, filter)
	return entries, err
}

// clampLimit is the page size convention (D-59): 50 by default, 200 at most.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 200:
		return 200
	}
	return n
}

// CountAuditLog counts the entries the filter matches, all pages.
func (s *Service) CountAuditLog(ctx context.Context, orgID string, filter AuditLogFilter) (int, error) {
	filter.Limit, filter.Before, filter.BeforeID = 1, nil, ""
	_, total, err := s.repo.ListAuditLog(ctx, orgID, filter)
	return total, err
}

// GetSettings returns the org's settings including the resource bypass role.
func (s *Service) GetSettings(ctx context.Context, orgID string) (*OrgSettings, error) {
	return s.repo.GetSettings(ctx, orgID)
}

// UpdateSettings updates the org's settings with validation.
func (s *Service) UpdateSettings(ctx context.Context, orgID string, input OrgSettings) (*OrgSettings, error) {
	if err := ValidateBypassRole(input.ResourceBypassMinRole); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	switch input.DefaultIsolation {
	case IsolationStandard, IsolationSandboxed, IsolationSandboxedStrict:
	case "":
		cur, err := s.repo.GetSettings(ctx, orgID)
		if err != nil {
			return nil, err
		}
		input.DefaultIsolation = cur.DefaultIsolation
	default:
		return nil, fmt.Errorf("%w: default_isolation must be %q, %q or %q", ErrInvalidSettings, IsolationStandard, IsolationSandboxed, IsolationSandboxedStrict)
	}
	return s.repo.UpdateSettings(ctx, orgID, input)
}

// HideSetup hides Home's Finish setting up checklist for the workspace (7g)
// and returns the settings; hidden reports whether it showed before.
func (s *Service) HideSetup(ctx context.Context, orgID string) (settings *OrgSettings, hidden bool, err error) {
	if hidden, err = s.repo.HideSetup(ctx, orgID); err != nil {
		return nil, false, err
	}
	// Also tells a workspace that doesn't exist (ErrNotFound) from one hidden before.
	if settings, err = s.repo.GetSettings(ctx, orgID); err != nil {
		return nil, false, err
	}
	return settings, hidden, nil
}

// Isolation is the workspace's default level and, for each of its servers
// and the panel's own when services run there, whether it can run Sandboxed
// and how many services follow the default there (D-95).
func (s *Service) Isolation(ctx context.Context, orgID string) (*OrgIsolation, error) {
	settings, err := s.repo.GetSettings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	following, err := s.repo.CountDefaultIsolationServices(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := &OrgIsolation{DefaultIsolation: settings.DefaultIsolation, Servers: make([]IsolationServer, 0, len(following)+1)}
	if s.sandboxes != nil {
		levels, err := s.sandboxes.SandboxedLevels(ctx, orgID)
		if err != nil {
			return nil, err
		}
		for _, l := range levels {
			out.Servers = append(out.Servers, IsolationServer{ID: l.ServerID, Name: l.ServerName, Sandboxed: l.State, Reason: l.Reason,
				SandboxedStrict: l.StrictState, StrictReason: l.StrictReason, GvisorVersion: l.GvisorVersion, Following: following[l.ServerID]})
		}
	}
	// The panel's own server whenever the workspace has services there, so
	// their Settings → Isolation can say whether Sandboxed runs there (D-97).
	if n, ok := following[""]; ok {
		unchecked := "The panel's own server hasn't been checked yet: Check again under Isolation on Instance → Server."
		panel := IsolationServer{Name: "The panel's own server", Sandboxed: "unknown", Reason: unchecked,
			SandboxedStrict: "unknown", StrictReason: unchecked, Following: n}
		if s.sandboxes != nil {
			l, version, err := s.sandboxes.Sandboxed(ctx, "")
			if err != nil {
				return nil, err
			}
			strict, _, err := s.sandboxes.SandboxedStrict(ctx, "")
			if err != nil {
				return nil, err
			}
			panel.Sandboxed, panel.Reason, panel.GvisorVersion = l.State, l.Reason, version
			panel.SandboxedStrict, panel.StrictReason = strict.State, strict.Reason
		}
		out.Servers = append(out.Servers, panel)
	}
	return out, nil
}
