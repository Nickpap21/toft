package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ─── Fakes ────────────────────────────────────────────────────────────────────

type fakeUserRepo struct {
	byEmail      map[string]*user.User
	byID         map[string]*user.User
	lastLoginIDs []string
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{byEmail: map[string]*user.User{}, byID: map[string]*user.User{}}
}

func (f *fakeUserRepo) add(u *user.User) {
	f.byEmail[u.Email] = u
	f.byID[u.ID] = u
}

func (f *fakeUserRepo) Create(ctx context.Context, email, name, passwordHash string) (*user.User, error) {
	return nil, errors.New("auth must not create users outside auth.Repository")
}
func (f *fakeUserRepo) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	if u, ok := f.byEmail[email]; ok {
		return u, nil
	}
	return nil, user.ErrNotFound
}
func (f *fakeUserRepo) GetByID(ctx context.Context, id string) (*user.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, user.ErrNotFound
}
func (f *fakeUserRepo) RequestIdentity(ctx context.Context, id, orgID, _ string) (user.Identity, error) {
	active, err := f.IsActive(ctx, id)
	return user.Identity{Active: active}, err
}
func (f *fakeUserRepo) IsActive(ctx context.Context, id string) (bool, error) {
	if u, ok := f.byID[id]; ok {
		return u.IsActive, nil
	}
	return false, user.ErrNotFound
}
func (f *fakeUserRepo) UpdateLastLogin(ctx context.Context, id string) error {
	f.lastLoginIDs = append(f.lastLoginIDs, id)
	return nil
}
func (f *fakeUserRepo) UpdateProfile(ctx context.Context, id, name string) (*user.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) RevokeSessions(ctx context.Context, id string) error { return nil }
func (f *fakeUserRepo) RevokeSession(ctx context.Context, tokenHash string, expires time.Time) error {
	return nil
}
func (f *fakeUserRepo) UpdateTheme(ctx context.Context, id, theme string) error { return nil }
func (f *fakeUserRepo) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	return nil
}

// fakeAuthRepo records calls. On success it "creates" the user in users so
// Register can load it back.
type fakeAuthRepo struct {
	users *fakeUserRepo

	usersExist    bool
	usersExistErr error

	firstUserErr  error
	inviteUserErr error

	firstUserCalls  int
	inviteUserCalls int
	gotOrgName      string
	gotToken        string
	gotNewUser      auth.NewUser
}

func (f *fakeAuthRepo) UsersExist(ctx context.Context) (bool, error) {
	return f.usersExist, f.usersExistErr
}

func (f *fakeAuthRepo) CreateFirstUser(ctx context.Context, u auth.NewUser, orgName string) (string, error) {
	f.firstUserCalls++
	f.gotNewUser = u
	f.gotOrgName = orgName
	if f.firstUserErr != nil {
		return "", f.firstUserErr
	}
	f.users.add(&user.User{ID: "user-1", Email: u.Email, Name: u.Name, Role: "admin", IsActive: true})
	return "user-1", nil
}

func (f *fakeAuthRepo) CreateUserWithInvite(ctx context.Context, u auth.NewUser, token string) (string, error) {
	f.inviteUserCalls++
	f.gotNewUser = u
	f.gotToken = token
	if f.inviteUserErr != nil {
		return "", f.inviteUserErr
	}
	f.users.add(&user.User{ID: "user-2", Email: u.Email, Name: u.Name, Role: "admin", IsActive: true})
	return "user-2", nil
}

type fakeGate struct {
	mode        string
	operators   map[string]bool
	operatorErr error
	setupToken  string // "" means none configured
	expired     bool
}

func (g *fakeGate) RegistrationMode(ctx context.Context) string {
	if g.mode == "" {
		return instance.ModeInviteOnly
	}
	return g.mode
}
func (g *fakeGate) IsOperator(ctx context.Context, userID string) (bool, error) {
	if g.operatorErr != nil {
		return false, g.operatorErr
	}
	return g.operators[userID], nil
}
func (g *fakeGate) VerifySetupToken(provided string) bool {
	return g.setupToken == "" || provided == g.setupToken
}

func (g *fakeGate) SetupTokenExpired() bool { return g.expired }

func newSvc(t *testing.T, repo *fakeAuthRepo, gate auth.InstanceGate) *auth.Service {
	t.Helper()
	return auth.NewService(repo.users, repo, gate, zap.NewNop(), "test-secret")
}

func params(invite string) auth.RegisterParams {
	return auth.RegisterParams{Email: "ada@example.com", Name: "Ada", Password: "password123", InviteToken: invite}
}

// ─── Register: first run ──────────────────────────────────────────────────────

func TestRegister_FirstUser_CreatesOperatorAndWorkspace(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	svc := newSvc(t, repo, &fakeGate{})

	res, err := svc.Register(context.Background(), params(""))

	require.NoError(t, err)
	assert.Equal(t, 1, repo.firstUserCalls)
	assert.Equal(t, 0, repo.inviteUserCalls)
	assert.Equal(t, "Ada's workspace", repo.gotOrgName, "default workspace name derives from the user's name")

	long := params("")
	long.Name = strings.Repeat("a", 100)
	repo2 := &fakeAuthRepo{users: newFakeUserRepo()}
	_, err = newSvc(t, repo2, &fakeGate{}).Register(context.Background(), long)
	require.NoError(t, err)
	assert.Equal(t, org.DefaultWorkspaceName, repo2.gotOrgName, "a name too long for the workspace's gets the default")
	assert.True(t, res.IsOperator, "the first account is the instance operator")
	assert.Equal(t, "user-1", res.User.ID)
	assert.NotEmpty(t, res.Token)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(repo.gotNewUser.PasswordHash), []byte("password123")),
		"the repository receives a bcrypt hash, never the password")
}

func TestRegister_FirstUser_UsesProvidedOrgName(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	svc := newSvc(t, repo, &fakeGate{})
	p := params("")
	p.OrgName = "  Acme  "

	_, err := svc.Register(context.Background(), p)

	require.NoError(t, err)
	assert.Equal(t, "Acme", repo.gotOrgName)
}

func TestRegister_NoInvite_UsersExist_Refused(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true}
	svc := newSvc(t, repo, &fakeGate{})
	p := params("")
	p.OrgName = "Sneaky"

	res, err := svc.Register(context.Background(), p)

	assert.ErrorIs(t, err, auth.ErrRegistrationClosed)
	assert.Nil(t, res)
	assert.Zero(t, repo.firstUserCalls, "must not create a user")
	assert.Zero(t, repo.inviteUserCalls)
}

func TestRegister_NoInvite_UnknownModeFailsClosed(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true}
	svc := newSvc(t, repo, &fakeGate{mode: instance.ModeEmailDomain})

	_, err := svc.Register(context.Background(), params(""))

	assert.ErrorIs(t, err, auth.ErrRegistrationClosed)
	assert.Zero(t, repo.firstUserCalls)
}

func TestRegister_NoInvite_NilGateIsInviteOnly(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true}
	svc := newSvc(t, repo, nil)

	_, err := svc.Register(context.Background(), params(""))

	assert.ErrorIs(t, err, auth.ErrRegistrationClosed)
}

func TestRegister_NoInvite_LostFirstUserRace(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), firstUserErr: auth.ErrNotFirstUser}
	svc := newSvc(t, repo, &fakeGate{})

	res, err := svc.Register(context.Background(), params(""))

	assert.ErrorIs(t, err, auth.ErrRegistrationClosed)
	assert.Nil(t, res)
}

func TestRegister_UsersExistError_IsReturned(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExistErr: errors.New("connection reset")}
	svc := newSvc(t, repo, &fakeGate{})

	_, err := svc.Register(context.Background(), params(""))

	assert.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrRegistrationClosed)
	assert.Zero(t, repo.firstUserCalls)
}

// ─── Register: setup token (TOFT_SETUP_TOKEN) ─────────────────────────────────

func TestRegister_FirstUser_SetupTokenRequired(t *testing.T) {
	tests := []struct {
		name     string
		provided string
		expired  bool
		wantErr  error
	}{
		{name: "missing", provided: "", wantErr: auth.ErrSetupTokenInvalid},
		{name: "wrong", provided: "nope", wantErr: auth.ErrSetupTokenInvalid},
		{name: "correct", provided: "s3cret", wantErr: nil},
		// The installer's code lasts 30 minutes (D-61): past that even the
		// right code creates nothing.
		{name: "correct but expired", provided: "s3cret", expired: true, wantErr: auth.ErrSetupTokenExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeAuthRepo{users: newFakeUserRepo()}
			svc := newSvc(t, repo, &fakeGate{setupToken: "s3cret", expired: tc.expired})
			p := params("")
			p.SetupToken = tc.provided

			res, err := svc.Register(context.Background(), p)

			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, res)
				assert.Zero(t, repo.firstUserCalls, "must not create anything without the setup token")
				return
			}
			require.NoError(t, err)
			assert.True(t, res.IsOperator)
		})
	}
}

func TestRegister_SetupTokenNotCheckedOnceUsersExist(t *testing.T) {
	// After first run the answer is "invite-only" regardless of the token,
	// so a correct token is no way in.
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true}
	svc := newSvc(t, repo, &fakeGate{setupToken: "s3cret"})
	p := params("")
	p.SetupToken = "s3cret"

	_, err := svc.Register(context.Background(), p)

	assert.ErrorIs(t, err, auth.ErrRegistrationClosed)
}

func TestRegister_WithInvite_SetupTokenIgnored(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	svc := newSvc(t, repo, &fakeGate{setupToken: "s3cret"})

	_, err := svc.Register(context.Background(), params("invite-token-123"))

	require.NoError(t, err)
	assert.Equal(t, 1, repo.inviteUserCalls)
}

// ─── Register: invite ─────────────────────────────────────────────────────────

func TestRegister_WithInvite_Success(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true}
	svc := newSvc(t, repo, &fakeGate{})
	p := params("invite-token-123")
	p.OrgName = "ignored"

	res, err := svc.Register(context.Background(), p)

	require.NoError(t, err)
	assert.Equal(t, 1, repo.inviteUserCalls)
	assert.Equal(t, "invite-token-123", repo.gotToken)
	assert.Zero(t, repo.firstUserCalls, "an invited user never gets their own workspace")
	assert.False(t, res.IsOperator)
	assert.Equal(t, "user-2", res.User.ID)
}

func TestRegister_WithInvite_ErrorsReturnedWithoutFallback(t *testing.T) {
	for _, inviteErr := range []error{org.ErrInviteNotFound, org.ErrInviteExpired, org.ErrInviteUsed} {
		t.Run(inviteErr.Error(), func(t *testing.T) {
			repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true, inviteUserErr: inviteErr}
			svc := newSvc(t, repo, &fakeGate{})

			res, err := svc.Register(context.Background(), params("bad-token"))

			assert.ErrorIs(t, err, inviteErr)
			assert.Nil(t, res)
			assert.Zero(t, repo.firstUserCalls, "a failed invite must not fall back to a new workspace")
		})
	}
}

func TestRegister_EmailTaken_ReturnsErrUserExists(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo(), usersExist: true, inviteUserErr: auth.ErrUserExists}
	svc := newSvc(t, repo, &fakeGate{})

	_, err := svc.Register(context.Background(), params("invite-token-123"))

	assert.ErrorIs(t, err, auth.ErrUserExists)
}

// ─── Login ────────────────────────────────────────────────────────────────────

func addLoginUser(t *testing.T, users *fakeUserRepo, active bool) *user.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	require.NoError(t, err)
	u := &user.User{ID: "user-9", Email: "ada@example.com", Name: "Ada", PasswordHash: string(hash), IsActive: active}
	users.add(u)
	return u
}

func TestLogin_ReturnsOperatorFlag(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, true)
	svc := newSvc(t, repo, &fakeGate{operators: map[string]bool{"user-9": true}})

	res, err := svc.Login(context.Background(), "ada@example.com", "password123")

	require.NoError(t, err)
	assert.True(t, res.IsOperator)
	assert.NotEmpty(t, res.Token)
	assert.Equal(t, []string{"user-9"}, repo.users.lastLoginIDs)
}

func TestLogin_OperatorCheckError_StillSucceeds(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, true)
	svc := newSvc(t, repo, &fakeGate{operatorErr: errors.New("relation instance_operators does not exist")})

	res, err := svc.Login(context.Background(), "ada@example.com", "password123")

	require.NoError(t, err, "login must not fail because the operator check failed")
	assert.False(t, res.IsOperator, "a failed check reports false")
}

func TestLogin_WrongPassword(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, true)
	svc := newSvc(t, repo, &fakeGate{})

	_, err := svc.Login(context.Background(), "ada@example.com", "wrong-password")

	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestLogin_DeactivatedUser_Refused(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, false)
	svc := newSvc(t, repo, &fakeGate{})

	res, err := svc.Login(context.Background(), "ada@example.com", "password123")

	assert.ErrorIs(t, err, auth.ErrAccountDisabled)
	assert.Nil(t, res)
	assert.Empty(t, repo.users.lastLoginIDs, "a refused login must not count as a login")
}

func TestLogin_DeactivatedUser_WrongPasswordRevealsNothing(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, false)
	svc := newSvc(t, repo, &fakeGate{})

	_, err := svc.Login(context.Background(), "ada@example.com", "wrong-password")

	assert.ErrorIs(t, err, auth.ErrInvalidCredentials, "account state is only revealed after the password matched")
}

// ─── Reauthenticate (D-53) ────────────────────────────────────────────────────

func TestReauthenticate_IssuesAProofForThisSession(t *testing.T) {
	repo := &fakeAuthRepo{users: newFakeUserRepo()}
	addLoginUser(t, repo.users, true)
	svc := newSvc(t, repo, &fakeGate{})

	token, expires, err := svc.Reauthenticate(context.Background(), "user-9", "session-1", "password123")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(reauth.TTL), expires, time.Second)
	assert.True(t, reauth.Valid([]byte("test-secret"), token, "user-9", "session-1"))
	assert.False(t, reauth.Valid([]byte("test-secret"), token, "user-9", "session-2"))

	_, _, err = svc.Reauthenticate(context.Background(), "user-9", "session-1", "wrong-password")
	assert.ErrorIs(t, err, auth.ErrWrongPassword)
}
