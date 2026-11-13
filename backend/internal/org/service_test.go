package org_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeRepository implements org.Repository. Only GetInviteByToken and
// AcceptInvite have real behavior in these tests; every other method is
// unused by ValidateInvite/AcceptInvite and returns a zero value if
// accidentally called.
type fakeRepository struct {
	invite    *org.Invite
	inviteErr error

	activeOrgID     string
	activeOrgErr    error
	membershipRole  string
	membershipErr   error
	earliestOrgID   string
	earliestRole    string
	earliestErr     error
	setActiveOrgErr error
	setActiveCalls  []string
	orgsForUser     []org.OrgMembership

	createResult  *org.Organization
	createErr     error
	createdNames  []string
	ownedCreates  []string // CreateWithOwner calls: "name/ownerID"
	getByIDResult *org.Organization
	getByIDErr    error

	deleted []string // Delete calls

	addMemberCalls []string // "orgID/userID/role"
	memberChanges  []string // "orgID/userID/role", role empty for a removal
	audit          []string // "action resource detail"
	auditFilters   []org.AuditLogFilter
	memberErr      error
	inviteRoles    []string
	acceptErr      error
	addableCalls   []bool
}

func (f *fakeRepository) CreateWithOwner(ctx context.Context, name, ownerID string) (*org.Organization, error) {
	f.ownedCreates = append(f.ownedCreates, name+"/"+ownerID)
	f.createdNames = append(f.createdNames, name)
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResult != nil {
		return f.createResult, nil
	}
	return &org.Organization{ID: "org-created", Name: name}, nil
}
func (f *fakeRepository) GetByID(ctx context.Context, id string) (*org.Organization, error) {
	if f.getByIDErr != nil {
		return nil, f.getByIDErr
	}
	if f.getByIDResult != nil {
		return f.getByIDResult, nil
	}
	return &org.Organization{ID: id}, nil
}
func (f *fakeRepository) UpdateName(ctx context.Context, id, name string) (*org.Organization, error) {
	return nil, nil
}
func (f *fakeRepository) Delete(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeRepository) ListAddableAccounts(ctx context.Context, orgID string, allAccounts bool) ([]org.AddableAccount, error) {
	f.addableCalls = append(f.addableCalls, allAccounts)
	return []org.AddableAccount{{UserID: "u-9", Name: "Nine", Email: "nine@example.com", Former: true, FormerRole: "developer"}}, nil
}
func (f *fakeRepository) AddMemberByEmail(ctx context.Context, orgID, email, role string) (*org.Member, error) {
	f.memberChanges = append(f.memberChanges, orgID+"/"+email+"/"+role)
	if f.memberErr != nil {
		return nil, f.memberErr
	}
	return &org.Member{OrgID: orgID, UserID: "u-" + email, UserEmail: email, Role: role}, nil
}
func (f *fakeRepository) AddMember(ctx context.Context, orgID, userID, role string) (*org.Member, error) {
	f.addMemberCalls = append(f.addMemberCalls, orgID+"/"+userID+"/"+role)
	return &org.Member{ID: "member-1", OrgID: orgID, UserID: userID, Role: role}, nil
}
func (f *fakeRepository) GetActiveOrgID(ctx context.Context, userID string) (string, error) {
	return f.activeOrgID, f.activeOrgErr
}
func (f *fakeRepository) GetMembershipInOrg(ctx context.Context, userID, orgID string) (string, error) {
	return f.membershipRole, f.membershipErr
}
func (f *fakeRepository) GetEarliestMembership(ctx context.Context, userID string) (string, string, error) {
	return f.earliestOrgID, f.earliestRole, f.earliestErr
}
func (f *fakeRepository) SetActiveOrg(ctx context.Context, userID, orgID string) error {
	f.setActiveCalls = append(f.setActiveCalls, orgID)
	return f.setActiveOrgErr
}
func (f *fakeRepository) ListOrgsForUser(ctx context.Context, userID string) ([]org.OrgMembership, error) {
	if f.orgsForUser == nil {
		return []org.OrgMembership{}, nil
	}
	return f.orgsForUser, nil
}
func (f *fakeRepository) ListMembers(ctx context.Context, orgID string) ([]org.Member, error) {
	return []org.Member{}, nil
}
func (f *fakeRepository) UpdateMemberRole(ctx context.Context, orgID, userID, role string) (string, error) {
	f.memberChanges = append(f.memberChanges, orgID+"/"+userID+"/"+role)
	return "developer", f.memberErr
}
func (f *fakeRepository) RemoveMember(ctx context.Context, orgID, userID string) (string, error) {
	f.memberChanges = append(f.memberChanges, orgID+"/"+userID+"/")
	return "developer", f.memberErr
}
func (f *fakeRepository) CreateInvite(ctx context.Context, orgID, createdBy, role, token string, expiresAt time.Time) (*org.Invite, error) {
	f.inviteRoles = append(f.inviteRoles, role)
	return &org.Invite{ID: "invite-new", OrgID: orgID, Role: role, Token: token, ExpiresAt: expiresAt}, nil
}
func (f *fakeRepository) GetInviteByToken(ctx context.Context, token string) (*org.Invite, error) {
	return f.invite, f.inviteErr
}
func (f *fakeRepository) AcceptInvite(ctx context.Context, inviteID, orgID, userID, role string) (*org.Organization, error) {
	if f.acceptErr != nil {
		return nil, f.acceptErr
	}
	return &org.Organization{ID: orgID}, nil
}
func (f *fakeRepository) ListInvites(ctx context.Context, orgID string) ([]org.Invite, error) {
	return []org.Invite{}, nil
}
func (f *fakeRepository) MarkInviteUsed(ctx context.Context, inviteID string) error { return nil }
func (f *fakeRepository) RevokeInvite(ctx context.Context, orgID, inviteID string) error {
	return nil
}
func (f *fakeRepository) HashUnhashedInvites(ctx context.Context) (int64, error) { return 0, nil }
func (f *fakeRepository) WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string, detail []byte) error {
	f.audit = append(f.audit, action+" "+resource+" "+string(detail))
	return nil
}
func (f *fakeRepository) DeleteAuditLogBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeRepository) ListAuditLog(ctx context.Context, orgID string, filter org.AuditLogFilter) ([]org.AuditEntry, int, error) {
	f.auditFilters = append(f.auditFilters, filter)
	return []org.AuditEntry{}, 7, nil
}
func (f *fakeRepository) GetSettings(ctx context.Context, orgID string) (*org.OrgSettings, error) {
	return nil, nil
}
func (f *fakeRepository) UpdateSettings(ctx context.Context, orgID string, settings org.OrgSettings) (*org.OrgSettings, error) {
	return nil, nil
}
func (f *fakeRepository) HideSetup(context.Context, string) (bool, error) { return false, nil }
func (f *fakeRepository) CountDefaultIsolationServices(context.Context, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func TestValidateInvite(t *testing.T) {
	now := time.Now()
	usedAt := now.Add(-time.Hour)

	tests := []struct {
		name    string
		repo    *fakeRepository
		wantErr error
	}{
		{
			name:    "token not found",
			repo:    &fakeRepository{inviteErr: org.ErrNotFound},
			wantErr: org.ErrInviteNotFound,
		},
		{
			name: "already used",
			repo: &fakeRepository{invite: &org.Invite{
				UsedAt:    &usedAt,
				ExpiresAt: now.Add(time.Hour),
			}},
			wantErr: org.ErrInviteUsed,
		},
		{
			name: "expired",
			repo: &fakeRepository{invite: &org.Invite{
				ExpiresAt: now.Add(-time.Hour),
			}},
			wantErr: org.ErrInviteExpired,
		},
		{
			name: "creator deactivated",
			repo: &fakeRepository{invite: &org.Invite{
				ExpiresAt:     now.Add(time.Hour),
				CreatorActive: false,
			}},
			wantErr: org.ErrInviteExpired,
		},
		{
			name: "valid",
			repo: &fakeRepository{invite: &org.Invite{
				ID:            "invite-1",
				ExpiresAt:     now.Add(time.Hour),
				CreatorActive: true,
			}},
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := org.NewService(tc.repo, zap.NewNop())
			inv, err := svc.ValidateInvite(context.Background(), "any-token")
			if tc.wantErr == nil {
				assert.NoError(t, err)
				assert.Same(t, tc.repo.invite, inv, "should return the exact invite the repo supplied")
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestValidateInvite_WrapsUnexpectedError verifies that an infrastructure
// failure from the repo (e.g. a dead connection) is NOT silently flattened
// into ErrInviteNotFound — only the repo's ErrNotFound sentinel maps there.
func TestValidateInvite_WrapsUnexpectedError(t *testing.T) {
	repo := &fakeRepository{inviteErr: errors.New("connection reset")}
	svc := org.NewService(repo, zap.NewNop())
	_, err := svc.ValidateInvite(context.Background(), "any-token")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, org.ErrInviteNotFound)
	assert.Contains(t, err.Error(), "connection reset")
}

func TestGetForUser(t *testing.T) {
	tests := []struct {
		name          string
		repo          *fakeRepository
		wantOrgID     string
		wantRole      string
		wantErr       error
		wantHealCalls []string
	}{
		{
			name: "active org membership still valid",
			repo: &fakeRepository{
				activeOrgID:    "org-1",
				membershipRole: "developer",
			},
			wantOrgID: "org-1",
			wantRole:  "developer",
		},
		{
			name: "no active org set — falls back to earliest and self-heals",
			repo: &fakeRepository{
				earliestOrgID: "org-2",
				earliestRole:  "owner",
			},
			wantOrgID:     "org-2",
			wantRole:      "owner",
			wantHealCalls: []string{"org-2"},
		},
		{
			name: "active org membership removed — falls back to earliest and self-heals",
			repo: &fakeRepository{
				activeOrgID:   "org-1",
				membershipErr: org.ErrNotFound,
				earliestOrgID: "org-3",
				earliestRole:  "viewer",
			},
			wantOrgID:     "org-3",
			wantRole:      "viewer",
			wantHealCalls: []string{"org-3"},
		},
		{
			name: "no memberships at all",
			repo: &fakeRepository{
				earliestErr: org.ErrNotFound,
			},
			wantErr: org.ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := org.NewService(tc.repo, zap.NewNop())
			orgID, role, err := svc.GetForUser(context.Background(), "user-1")
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.wantOrgID, orgID)
			assert.Equal(t, tc.wantRole, role)
			assert.Equal(t, tc.wantHealCalls, tc.repo.setActiveCalls)
		})
	}
}

func TestGetForUser_WrapsUnexpectedError(t *testing.T) {
	repo := &fakeRepository{activeOrgErr: errors.New("connection reset")}
	svc := org.NewService(repo, zap.NewNop())
	_, _, err := svc.GetForUser(context.Background(), "user-1")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, org.ErrNotFound)
	assert.Contains(t, err.Error(), "connection reset")
}

func TestSwitchActiveOrg_Success(t *testing.T) {
	repo := &fakeRepository{membershipRole: "developer"}
	svc := org.NewService(repo, zap.NewNop())

	o, role, err := svc.SwitchActiveOrg(context.Background(), "user-1", "org-9")

	assert.NoError(t, err)
	assert.Equal(t, "org-9", o.ID)
	assert.Equal(t, "developer", role)
	assert.Equal(t, []string{"org-9"}, repo.setActiveCalls)
}

func TestSwitchActiveOrg_NotMember(t *testing.T) {
	repo := &fakeRepository{membershipErr: org.ErrNotFound}
	svc := org.NewService(repo, zap.NewNop())

	_, _, err := svc.SwitchActiveOrg(context.Background(), "user-1", "org-9")

	assert.ErrorIs(t, err, org.ErrNotMember)
	assert.Empty(t, repo.setActiveCalls, "must not change active org when the user isn't a member")
}

func TestListOrgsForUser(t *testing.T) {
	want := []org.OrgMembership{
		{OrgID: "org-1", Name: "Acme", Role: "owner"},
		{OrgID: "org-2", Name: "Client B", Role: "developer"},
	}
	repo := &fakeRepository{orgsForUser: want}
	svc := org.NewService(repo, zap.NewNop())

	got, err := svc.ListOrgsForUser(context.Background(), "user-1")

	assert.NoError(t, err)
	assert.Equal(t, want, got)
}

type fakeChecker struct {
	operators map[string]bool
	err       error
}

func (f *fakeChecker) IsOperator(ctx context.Context, userID string) (bool, error) {
	return f.operators[userID], f.err
}

func TestCreateWorkspace_NonOperator_Forbidden(t *testing.T) {
	repo := &fakeRepository{}
	svc := org.NewService(repo, zap.NewNop()).WithOperatorChecker(&fakeChecker{})

	o, err := svc.CreateWorkspace(context.Background(), "user-1", "Mine")

	assert.ErrorIs(t, err, org.ErrOperatorOnly)
	assert.Nil(t, o)
	assert.Empty(t, repo.createdNames, "must not create anything")
}

func TestCreateWorkspace_NilChecker_FailsClosed(t *testing.T) {
	repo := &fakeRepository{}
	svc := org.NewService(repo, zap.NewNop())

	_, err := svc.CreateWorkspace(context.Background(), "user-1", "Mine")

	assert.ErrorIs(t, err, org.ErrOperatorOnly)
	assert.Empty(t, repo.createdNames)
}

func TestCreateWorkspace_CheckerError_FailsClosed(t *testing.T) {
	repo := &fakeRepository{}
	svc := org.NewService(repo, zap.NewNop()).WithOperatorChecker(&fakeChecker{
		operators: map[string]bool{"user-1": true},
		err:       errors.New("relation instance_operators does not exist"),
	})

	_, err := svc.CreateWorkspace(context.Background(), "user-1", "Mine")

	assert.ErrorIs(t, err, org.ErrOperatorOnly)
	assert.Empty(t, repo.createdNames)
}

func TestCreateWorkspace_Operator_CreatesOwnsAndActivates(t *testing.T) {
	repo := &fakeRepository{}
	svc := org.NewService(repo, zap.NewNop()).WithOperatorChecker(&fakeChecker{operators: map[string]bool{"user-1": true}})

	o, err := svc.CreateWorkspace(context.Background(), "user-1", "New Workspace")

	assert.NoError(t, err)
	assert.Equal(t, "org-created", o.ID)
	assert.Equal(t, []string{"New Workspace/user-1"}, repo.ownedCreates,
		"org, owner membership and active org are created in one transaction")
	assert.Empty(t, repo.addMemberCalls, "no separate, non-transactional writes")
	assert.Empty(t, repo.setActiveCalls)
}

func TestCreateWorkspace_RepoError_Returned(t *testing.T) {
	repo := &fakeRepository{createErr: errors.New("statement timeout")}
	svc := org.NewService(repo, zap.NewNop()).WithOperatorChecker(&fakeChecker{operators: map[string]bool{"user-1": true}})

	o, err := svc.CreateWorkspace(context.Background(), "user-1", "Mine")

	assert.ErrorContains(t, err, "statement timeout")
	assert.Nil(t, o)
}

func TestCreateWorkspace_DefaultName(t *testing.T) {
	repo := &fakeRepository{}
	svc := org.NewService(repo, zap.NewNop()).WithOperatorChecker(&fakeChecker{operators: map[string]bool{"user-1": true}})

	_, err := svc.CreateWorkspace(context.Background(), "user-1", "")

	assert.NoError(t, err)
	assert.Equal(t, []string{org.DefaultWorkspaceName}, repo.createdNames)
}

func TestCreateInvite_ValidatesRole(t *testing.T) {
	for _, role := range []string{org.RoleOwner, org.RoleDeveloper, org.RoleViewer} {
		repo := &fakeRepository{}
		inv, err := org.NewService(repo, zap.NewNop()).CreateInvite(context.Background(), "org-1", "user-1", role)
		assert.NoError(t, err, role)
		assert.Equal(t, role, inv.Role)
	}
	for _, role := range []string{"", "admin", "Owner", "operator"} {
		repo := &fakeRepository{}
		_, err := org.NewService(repo, zap.NewNop()).CreateInvite(context.Background(), "org-1", "user-1", role)
		assert.ErrorIs(t, err, org.ErrInvalidRole, "role %q", role)
		assert.Empty(t, repo.inviteRoles, "must not store an invite with role %q", role)
	}
}

func TestInviteErrorMessage(t *testing.T) {
	for _, err := range []error{org.ErrInviteNotFound, org.ErrInviteExpired, org.ErrInviteUsed} {
		msg, ok := org.InviteErrorMessage(fmt.Errorf("wrapped: %w", err))
		assert.True(t, ok, err.Error())
		assert.NotEmpty(t, msg)
	}
	_, ok := org.InviteErrorMessage(errors.New("connection reset"))
	assert.False(t, ok)
	_, ok = org.InviteErrorMessage(org.ErrAlreadyMember)
	assert.False(t, ok)
}

// The invite page picks its view from these codes, never from the text.
func TestInviteErrorCode(t *testing.T) {
	for err, code := range map[error]string{
		org.ErrInviteNotFound: "invite_not_found",
		org.ErrInviteExpired:  "invite_expired",
		org.ErrInviteUsed:     "invite_used",
		org.ErrAlreadyMember:  "",
	} {
		assert.Equal(t, code, org.InviteErrorCode(fmt.Errorf("wrapped: %w", err)))
	}
}

// A deactivated owner's invite links must not let anyone (including the owner,
// signed in as another account) join the workspace.
func TestAcceptInvite_CreatorDeactivated_Refused(t *testing.T) {
	inv := &org.Invite{ID: "invite-1", OrgID: "org-1", Role: org.RoleOwner, ExpiresAt: time.Now().Add(time.Hour)}
	repo := &fakeRepository{invite: inv, acceptErr: errors.New("AcceptInvite must not be reached")}

	_, err := org.NewService(repo, zap.NewNop()).AcceptInvite(context.Background(), "tok", "user-2")

	assert.ErrorIs(t, err, org.ErrInviteExpired)
}

func TestAcceptInvite_PassesThroughRepoRefusals(t *testing.T) {
	valid := &org.Invite{ID: "invite-1", OrgID: "org-1", Role: org.RoleOwner, ExpiresAt: time.Now().Add(time.Hour), CreatorActive: true}
	for _, repoErr := range []error{org.ErrAlreadyMember, org.ErrInviteUsed, org.ErrInviteExpired} {
		repo := &fakeRepository{invite: valid, acceptErr: repoErr}
		_, err := org.NewService(repo, zap.NewNop()).AcceptInvite(context.Background(), "tok", "user-1")
		assert.ErrorIs(t, err, repoErr)
	}
}

type fakeContents struct {
	calls []string
	err   error
}

func (f *fakeContents) DeleteAll(_ context.Context, orgID string) error {
	f.calls = append(f.calls, orgID)
	return f.err
}

// Deleting a workspace needs its exact name, removes its contents first and
// keeps its rows when they could not all be removed.
func TestDeleteWorkspace(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepository{getByIDResult: &org.Organization{ID: "org-1", Name: "Northwind"}}
	contents := &fakeContents{}
	svc := org.NewService(repo, zap.NewNop())
	svc.WithWorkspaceContents(contents)

	for _, wrong := range []string{"", "northwind", "Northwind "} {
		assert.ErrorIs(t, svc.DeleteWorkspace(ctx, "org-1", wrong), org.ErrConfirmName, "%q", wrong)
	}
	assert.Empty(t, contents.calls, "nothing removed without the name")

	contents.err = org.ErrContentsNotRemoved
	assert.ErrorIs(t, svc.DeleteWorkspace(ctx, "org-1", "Northwind"), org.ErrContentsNotRemoved)
	assert.Empty(t, repo.deleted, "the workspace stays when its contents could not all be removed")

	contents.err = nil
	require.NoError(t, svc.DeleteWorkspace(ctx, "org-1", "Northwind"))
	assert.Equal(t, []string{"org-1"}, repo.deleted)
}
