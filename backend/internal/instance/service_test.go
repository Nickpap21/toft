package instance_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// ─── Fake repository ──────────────────────────────────────────────────────────

type fakeUser struct {
	id, email, name string
	active          bool
	deleted         bool
}

type auditRow struct{ actor, action, target, detail string }

type grant struct {
	userID    string
	grantedBy *string
	source    string
}

type fakeRepo struct {
	users     []fakeUser
	operators map[string]string // userID → source

	setting    []byte // the registration setting
	settingErr error
	// settings holds every other key, as MergeSetting writes them.
	settings          map[string][]byte
	auditKind         string     // the kind the last ListAudit asked for
	settingsMu        sync.Mutex // a run of updates' read after it writes from a goroutine
	panelHostServices int64
	usersErr          error
	isOpErr           error
	findErr           error
	promoteErr        error

	grants        []grant
	promoteCalls  int
	revokeCalls   []string
	setActiveCall []string

	// invitesExposed is what SetUserActive(false) reports as revoked.
	invitesExposed int64

	// resets by token hash; passwords by user ID, the hash RedeemPasswordReset set.
	resets    map[string]*fakeReset
	passwords map[string]string
	// revoked: the accounts a reset revoked the access of.
	revoked []string
	audit   []auditRow
}

type fakeReset struct {
	userID    string
	expiresAt time.Time
	used      bool
}

func newFakeRepo(users ...fakeUser) *fakeRepo {
	return &fakeRepo{users: users, operators: map[string]string{}, resets: map[string]*fakeReset{}, passwords: map[string]string{}}
}

func (f *fakeRepo) CreatePasswordReset(ctx context.Context, userID, createdBy, tokenHash string, expiresAt time.Time) error {
	u := f.user(userID)
	if u == nil {
		return instance.ErrUserNotFound
	}
	if !u.active {
		return instance.ErrUserInactive
	}
	for h, r := range f.resets {
		if r.userID == userID && !r.used {
			delete(f.resets, h)
		}
	}
	f.resets[tokenHash] = &fakeReset{userID: userID, expiresAt: expiresAt}
	return nil
}

func (f *fakeRepo) GetPasswordReset(ctx context.Context, tokenHash string) (*instance.PasswordResetTarget, error) {
	r := f.resets[tokenHash]
	if r == nil {
		return nil, instance.ErrResetNotFound
	}
	if r.used {
		return nil, instance.ErrResetUsed
	}
	u := f.user(r.userID)
	if !u.active || !time.Now().Before(r.expiresAt) {
		return nil, instance.ErrResetExpired
	}
	return &instance.PasswordResetTarget{Email: u.email, Name: u.name, ExpiresAt: r.expiresAt}, nil
}

func (f *fakeRepo) RedeemPasswordReset(ctx context.Context, tokenHash, passwordHash string, revokeAccess bool) (*instance.AuditPerson, error) {
	t, err := f.GetPasswordReset(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	f.resets[tokenHash].used = true
	f.passwords[f.resets[tokenHash].userID] = passwordHash
	if revokeAccess {
		f.revoked = append(f.revoked, f.resets[tokenHash].userID)
	}
	return &instance.AuditPerson{ID: f.resets[tokenHash].userID, Email: t.Email}, nil
}

func (f *fakeRepo) CountDeletedUsers(ctx context.Context) (int64, error) {
	var n int64
	for _, u := range f.users {
		if u.deleted {
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) DeleteUser(ctx context.Context, actorID, userID, passwordHash string) (string, error) {
	u := f.user(userID)
	switch {
	case u == nil:
		return "", instance.ErrUserNotFound
	case u.deleted:
		return "", instance.ErrUserDeleted
	case u.active:
		return "", instance.ErrUserActive
	}
	if _, isOp := f.operators[userID]; isOp {
		return "", instance.ErrIsOperator
	}
	email := u.email
	u.deleted, u.email, u.name = true, "deleted-"+u.id+"@deleted.invalid", "Deleted account"
	f.passwords[userID] = passwordHash
	return email, nil
}

func (f *fakeRepo) WriteAudit(ctx context.Context, actorID, action, targetUserID string, detail []byte) error {
	f.audit = append(f.audit, auditRow{actorID, action, targetUserID, string(detail)})
	return nil
}

func (f *fakeRepo) ListAudit(ctx context.Context, limit int32, before *time.Time, beforeID, kind string) ([]instance.AuditEntry, error) {
	f.auditKind = kind
	out := make([]instance.AuditEntry, 0, len(f.audit))
	for i := len(f.audit) - 1; i >= 0; i-- {
		a := f.audit[i]
		e := instance.AuditEntry{ID: a.action + "-" + a.target, Action: a.action, Detail: map[string]any{}}
		if a.detail != "" {
			_ = json.Unmarshal([]byte(a.detail), &e.Detail)
		}
		if u := f.user(a.actor); u != nil {
			e.Actor = &instance.AuditPerson{ID: u.id, Name: u.name, Email: u.email}
		}
		if u := f.user(a.target); u != nil {
			e.Target = &instance.AuditPerson{ID: u.id, Name: u.name, Email: u.email}
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeRepo) user(id string) *fakeUser {
	for i := range f.users {
		if f.users[i].id == id {
			return &f.users[i]
		}
	}
	return nil
}

func (f *fakeRepo) UsersExist(ctx context.Context) (bool, error) {
	return len(f.users) > 0, f.usersErr
}
func (f *fakeRepo) IsOperator(ctx context.Context, userID string) (bool, error) {
	if f.isOpErr != nil {
		return false, f.isOpErr
	}
	_, ok := f.operators[userID]
	return ok, nil
}
func (f *fakeRepo) ListOperators(ctx context.Context) ([]instance.Operator, error) {
	var ops []instance.Operator
	for _, u := range f.users {
		if src, ok := f.operators[u.id]; ok {
			ops = append(ops, instance.Operator{UserID: u.id, Email: u.email, Name: u.name, IsActive: u.active, Source: src})
		}
	}
	return ops, nil
}
func (f *fakeRepo) FindUserIDsByEmail(ctx context.Context, email string) ([]string, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	var ids []string
	for _, u := range f.users {
		if strings.EqualFold(u.email, email) {
			ids = append(ids, u.id)
		}
	}
	return ids, nil
}
func (f *fakeRepo) UserExists(ctx context.Context, userID string) (bool, error) {
	return f.user(userID) != nil, nil
}
func (f *fakeRepo) GrantOperator(ctx context.Context, userID string, grantedBy *string, source string) (bool, error) {
	f.grants = append(f.grants, grant{userID, grantedBy, source})
	if _, ok := f.operators[userID]; ok {
		return false, nil
	}
	f.operators[userID] = source
	return true, nil
}
func (f *fakeRepo) activeOperatorsExcept(userID string) int {
	n := 0
	for id := range f.operators {
		if u := f.user(id); id != userID && u != nil && u.active {
			n++
		}
	}
	return n
}
func (f *fakeRepo) RevokeOperator(ctx context.Context, userID string) error {
	f.revokeCalls = append(f.revokeCalls, userID)
	if _, ok := f.operators[userID]; !ok {
		return instance.ErrNotFound
	}
	if f.activeOperatorsExcept(userID) == 0 {
		return instance.ErrLastOperator
	}
	delete(f.operators, userID)
	return nil
}
func (f *fakeRepo) PromoteEarliestUserIfNone(ctx context.Context) (string, error) {
	f.promoteCalls++
	if f.promoteErr != nil {
		return "", f.promoteErr
	}
	if len(f.operators) > 0 {
		return "", nil
	}
	for _, u := range f.users { // users are in creation order
		if u.active {
			f.operators[u.id] = instance.SourceBootstrap
			return u.id, nil
		}
	}
	return "", nil
}
func (f *fakeRepo) GetSetting(ctx context.Context, key string) ([]byte, error) {
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	if f.settingErr != nil {
		return nil, f.settingErr
	}
	if v, ok := f.settings[key]; ok {
		return v, nil
	}
	if key != instance.SettingRegistration || f.setting == nil {
		return nil, instance.ErrNotFound
	}
	return f.setting, nil
}

func (f *fakeRepo) MergeSetting(ctx context.Context, key string, value []byte, actorID string) error {
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	merged := map[string]json.RawMessage{}
	if cur, ok := f.settings[key]; ok {
		if err := json.Unmarshal(cur, &merged); err != nil {
			return err
		}
	}
	var add map[string]json.RawMessage
	if err := json.Unmarshal(value, &add); err != nil {
		return err
	}
	for k, v := range add {
		merged[k] = v
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	if f.settings == nil {
		f.settings = map[string][]byte{}
	}
	f.settings[key] = raw
	return nil
}

func (f *fakeRepo) DeleteSetting(ctx context.Context, key string) error {
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	delete(f.settings, key)
	return nil
}

func (f *fakeRepo) CountPanelHostServices(ctx context.Context) (int64, error) {
	return f.panelHostServices, nil
}
func (f *fakeRepo) ListUsers(ctx context.Context) ([]instance.User, error) {
	out := make([]instance.User, 0, len(f.users))
	for _, u := range f.users {
		if u.deleted {
			continue
		}
		_, isOp := f.operators[u.id]
		var reset *time.Time
		for _, r := range f.resets {
			if r.userID == u.id && !r.used && time.Now().Before(r.expiresAt) {
				t := r.expiresAt
				reset = &t
			}
		}
		out = append(out, instance.User{ID: u.id, Email: u.email, Name: u.name, IsActive: u.active, IsOperator: isOp, ResetExpiresAt: reset, Workspaces: []string{}})
	}
	return out, nil
}
func (f *fakeRepo) SetUserActive(ctx context.Context, actorID, userID string, active bool) (*instance.UserUpdate, error) {
	f.setActiveCall = append(f.setActiveCall, userID)
	u := f.user(userID)
	if u == nil {
		return nil, instance.ErrUserNotFound
	}
	_, isOp := f.operators[userID]
	if !active && u.active && isOp && f.activeOperatorsExcept(userID) == 0 {
		return nil, instance.ErrLastOperator
	}
	u.active = active
	var revoked int64
	if !active {
		revoked, f.invitesExposed = f.invitesExposed, 0
		for h, r := range f.resets {
			if r.userID == userID && !r.used {
				delete(f.resets, h)
			}
		}
	}
	return &instance.UserUpdate{
		User:           instance.User{ID: u.id, Email: u.email, Name: u.name, IsActive: u.active, IsOperator: isOp, Workspaces: []string{}},
		InvitesRevoked: revoked,
	}, nil
}

var (
	ada   = fakeUser{id: "u-ada", email: "ada@example.com", name: "Ada", active: true}
	bob   = fakeUser{id: "u-bob", email: "bob@example.com", name: "Bob", active: true}
	carol = fakeUser{id: "u-carol", email: "carol@example.com", name: "Carol", active: true}
)

// pw is every fake account's password.
const pw = "correct horse"

type fakePasswords struct{}

func (fakePasswords) VerifyPassword(_ context.Context, _, password string) (bool, error) {
	return password == pw, nil
}

func newService(repo *fakeRepo) *instance.Service {
	return instance.NewService(repo, zap.NewNop()).WithPasswordVerifier(fakePasswords{})
}

// ─── Registration settings ────────────────────────────────────────────────────

func TestParseRegistrationSettings(t *testing.T) {
	s, err := instance.ParseRegistrationSettings([]byte(`{"mode":"invite_only"}`))
	require.NoError(t, err)
	assert.Equal(t, instance.ModeInviteOnly, s.Mode)

	_, err = instance.ParseRegistrationSettings([]byte(`{"mode":"open"}`))
	assert.Error(t, err, "unknown mode")

	_, err = instance.ParseRegistrationSettings([]byte(`{"mode":"email_domain","email_domains":["x.com"]}`))
	assert.Error(t, err, "reserved but not yet accepted")

	_, err = instance.ParseRegistrationSettings([]byte(`{not json`))
	assert.Error(t, err, "malformed JSON")
}

func TestRegistrationMode_FailsClosed(t *testing.T) {
	tests := []struct {
		name string
		repo *fakeRepo
	}{
		{"missing row", &fakeRepo{}},
		{"read error", &fakeRepo{settingErr: errors.New("relation instance_settings does not exist")}},
		{"unknown mode", &fakeRepo{setting: []byte(`{"mode":"open"}`)}},
		{"malformed", &fakeRepo{setting: []byte(`garbage`)}},
		{"explicit invite_only", &fakeRepo{setting: []byte(`{"mode":"invite_only"}`)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, instance.ModeInviteOnly, newService(tc.repo).RegistrationMode(context.Background()))
		})
	}
}

func TestRegistrationStatus(t *testing.T) {
	ctx := context.Background()

	st, err := newService(newFakeRepo()).RegistrationStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, instance.RegistrationStatus{Mode: instance.ModeInviteOnly, FirstRun: true}, st)

	st, err = newService(newFakeRepo(ada)).RegistrationStatus(ctx)
	require.NoError(t, err)
	assert.False(t, st.FirstRun)

	st, err = newService(newFakeRepo()).WithSetupToken("tok").RegistrationStatus(ctx)
	require.NoError(t, err)
	assert.True(t, st.SetupTokenRequired)

	st, err = newService(newFakeRepo(ada)).WithSetupToken("tok").RegistrationStatus(ctx)
	require.NoError(t, err)
	assert.False(t, st.SetupTokenRequired, "only reported while the instance is still unclaimed")

	_, err = newService(&fakeRepo{usersErr: errors.New("db down")}).RegistrationStatus(ctx)
	assert.Error(t, err)
}

func TestVerifySetupToken(t *testing.T) {
	open := newService(newFakeRepo())
	assert.False(t, open.SetupTokenRequired())
	assert.True(t, open.VerifySetupToken(""), "no token configured accepts anything")

	locked := newService(newFakeRepo()).WithSetupToken("s3cret")
	assert.True(t, locked.SetupTokenRequired())
	assert.True(t, locked.VerifySetupToken("s3cret"))
	assert.False(t, locked.VerifySetupToken(""))
	assert.False(t, locked.VerifySetupToken("s3cre"))
	assert.True(t, locked.VerifySetupToken("S3CRET"), "case doesn't count")

	// The installer's code: case, hyphens and spaces don't count (D-61).
	code := newService(newFakeRepo()).WithSetupToken("FJ7K-29QX")
	for _, typed := range []string{"FJ7K-29QX", "fj7k29qx", " FJ7K 29QX ", "fj7k-29qx"} {
		assert.True(t, code.VerifySetupToken(typed), typed)
	}
	assert.False(t, code.VerifySetupToken("FJ7K-29QY"))
	assert.False(t, code.VerifySetupToken(""))
}

// ─── Operator-only actions ────────────────────────────────────────────────────

func TestOperatorOnlyActions_RejectNonOperators(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	svc := newService(repo)

	_, err := svc.ListOperators(ctx, bob.id)
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	_, err = svc.GrantOperator(ctx, bob.id, pw, "", bob.email)
	assert.ErrorIs(t, err, instance.ErrNotOperator)
	assert.Empty(t, repo.grants)

	err = svc.RevokeOperator(ctx, bob.id, pw, ada.id)
	assert.ErrorIs(t, err, instance.ErrNotOperator)
	assert.Empty(t, repo.revokeCalls)

	_, _, err = svc.ListUsers(ctx, bob.id)
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	_, err = svc.SetUserActive(ctx, bob.id, pw, ada.id, false)
	assert.ErrorIs(t, err, instance.ErrNotOperator)
	assert.Empty(t, repo.setActiveCall)
}

func TestOperatorCheckError_Denies(t *testing.T) {
	repo := newFakeRepo(ada)
	repo.operators[ada.id] = instance.SourceFirstRun
	repo.isOpErr = errors.New("db down")

	_, err := newService(repo).ListOperators(context.Background(), ada.id)

	assert.Error(t, err)
	assert.NotErrorIs(t, err, instance.ErrNotOperator, "a failed check is a server error, not a 403")
}

func TestGrantOperator(t *testing.T) {
	ctx := context.Background()

	t.Run("by email, case-insensitive", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		op, err := newService(repo).GrantOperator(ctx, ada.id, pw, "", "  BOB@example.com ")
		require.NoError(t, err)
		assert.Equal(t, bob.id, op.UserID)
		require.Len(t, repo.grants, 1)
		assert.Equal(t, instance.SourceAPI, repo.grants[0].source)
		require.NotNil(t, repo.grants[0].grantedBy)
		assert.Equal(t, ada.id, *repo.grants[0].grantedBy)
	})

	t.Run("by user id", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		op, err := newService(repo).GrantOperator(ctx, ada.id, pw, bob.id, "")
		require.NoError(t, err)
		assert.Equal(t, bob.id, op.UserID)
	})

	t.Run("unknown email", func(t *testing.T) {
		repo := newFakeRepo(ada)
		repo.operators[ada.id] = instance.SourceFirstRun
		_, err := newService(repo).GrantOperator(ctx, ada.id, pw, "", "nobody@example.com")
		assert.ErrorIs(t, err, instance.ErrUserNotFound)
	})

	t.Run("unknown user id", func(t *testing.T) {
		repo := newFakeRepo(ada)
		repo.operators[ada.id] = instance.SourceFirstRun
		_, err := newService(repo).GrantOperator(ctx, ada.id, pw, "u-nobody", "")
		assert.ErrorIs(t, err, instance.ErrUserNotFound)
	})

	t.Run("ambiguous email", func(t *testing.T) {
		bobUpper := fakeUser{id: "u-bob2", email: "Bob@Example.com", active: true}
		repo := newFakeRepo(ada, bob, bobUpper)
		repo.operators[ada.id] = instance.SourceFirstRun
		_, err := newService(repo).GrantOperator(ctx, ada.id, pw, "", "bob@example.com")
		assert.ErrorIs(t, err, instance.ErrAmbiguousEmail)
		assert.Empty(t, repo.grants)
	})

	t.Run("idempotent", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		repo.operators[bob.id] = instance.SourceEnv
		op, err := newService(repo).GrantOperator(ctx, ada.id, pw, bob.id, "")
		require.NoError(t, err)
		assert.Equal(t, instance.SourceEnv, op.Source, "an existing grant is left as it was")
	})
}

func TestRevokeOperator(t *testing.T) {
	ctx := context.Background()

	t.Run("last operator", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		err := newService(repo).RevokeOperator(ctx, ada.id, pw, ada.id)
		assert.ErrorIs(t, err, instance.ErrLastOperator)
		assert.Contains(t, repo.operators, ada.id)
	})

	t.Run("self-revoke with another active operator", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		repo.operators[bob.id] = instance.SourceAPI
		require.NoError(t, newService(repo).RevokeOperator(ctx, ada.id, pw, ada.id))
		assert.NotContains(t, repo.operators, ada.id)
	})

	t.Run("other operator inactive counts as none", func(t *testing.T) {
		inactiveBob := bob
		inactiveBob.active = false
		repo := newFakeRepo(ada, inactiveBob)
		repo.operators[ada.id] = instance.SourceFirstRun
		repo.operators[bob.id] = instance.SourceAPI
		err := newService(repo).RevokeOperator(ctx, ada.id, pw, ada.id)
		assert.ErrorIs(t, err, instance.ErrLastOperator)
	})

	t.Run("not an operator", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		err := newService(repo).RevokeOperator(ctx, ada.id, pw, bob.id)
		assert.ErrorIs(t, err, instance.ErrNotFound)
	})
}

// ─── Users (A2) ───────────────────────────────────────────────────────────────

func TestListUsers(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun

	users, deleted, err := newService(repo).ListUsers(context.Background(), ada.id)
	assert.Zero(t, deleted)

	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.True(t, users[0].IsOperator)
	assert.False(t, users[1].IsOperator)
}

func TestSetUserActive(t *testing.T) {
	ctx := context.Background()

	t.Run("deactivate a member revokes the invites they hold", func(t *testing.T) {
		log, logs := observed()
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		repo.invitesExposed = 3 // bob owns a workspace with pending invite links

		u, err := instance.NewService(repo, log).WithPasswordVerifier(fakePasswords{}).SetUserActive(ctx, ada.id, pw, bob.id, false)

		require.NoError(t, err)
		assert.False(t, u.IsActive)
		assert.Equal(t, int64(3), u.InvitesRevoked, "reported so the operator can tell the workspace")
		entries := logs.FilterMessage("user deactivated").All()
		require.Len(t, entries, 1)
		assert.Equal(t, int64(3), entries[0].ContextMap()["invites_revoked"])
	})

	t.Run("reactivate", func(t *testing.T) {
		inactiveBob := bob
		inactiveBob.active = false
		repo := newFakeRepo(ada, inactiveBob)
		repo.operators[ada.id] = instance.SourceFirstRun
		u, err := newService(repo).SetUserActive(ctx, ada.id, pw, bob.id, true)
		require.NoError(t, err)
		assert.True(t, u.IsActive)
		assert.Zero(t, u.InvitesRevoked)
	})

	t.Run("cannot deactivate yourself", func(t *testing.T) {
		repo := newFakeRepo(ada, bob)
		repo.operators[ada.id] = instance.SourceFirstRun
		repo.operators[bob.id] = instance.SourceAPI
		_, err := newService(repo).SetUserActive(ctx, ada.id, pw, strings.ToUpper(ada.id), false)
		assert.ErrorIs(t, err, instance.ErrCannotDeactivateSelf)
		assert.Empty(t, repo.setActiveCall)
	})

	t.Run("cannot deactivate the last active operator", func(t *testing.T) {
		// Through the API the actor is always an active operator, so this
		// guard matters for races; here an inactive operator (bob) calls the
		// service directly to show the repository's refusal is passed through.
		inactiveBob := bob
		inactiveBob.active = false
		repo := newFakeRepo(ada, inactiveBob)
		repo.operators[ada.id] = instance.SourceFirstRun
		repo.operators[bob.id] = instance.SourceAPI
		_, err := newService(repo).SetUserActive(ctx, bob.id, pw, ada.id, false)
		assert.ErrorIs(t, err, instance.ErrLastOperator)
		assert.True(t, repo.user(ada.id).active)
	})

	t.Run("unknown user", func(t *testing.T) {
		repo := newFakeRepo(ada)
		repo.operators[ada.id] = instance.SourceFirstRun
		_, err := newService(repo).SetUserActive(ctx, ada.id, pw, "u-nobody", false)
		assert.ErrorIs(t, err, instance.ErrUserNotFound)
	})
}

// Every change to accounts and operators asks for the operator's password
// again (D-23, D-50); a wrong one changes nothing.
func TestChanges_NeedTheOperatorsPassword(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun
	repo.operators[bob.id] = instance.SourceAPI
	svc := newService(repo)

	_, err := svc.GrantOperator(ctx, ada.id, "wrong", "", carol.email)
	assert.ErrorIs(t, err, instance.ErrWrongPassword)
	assert.Empty(t, repo.grants)

	err = svc.RevokeOperator(ctx, ada.id, "", bob.id)
	assert.ErrorIs(t, err, instance.ErrWrongPassword)
	assert.Empty(t, repo.revokeCalls)

	_, err = svc.SetUserActive(ctx, ada.id, "wrong", bob.id, false)
	assert.ErrorIs(t, err, instance.ErrWrongPassword)
	assert.Empty(t, repo.setActiveCall)

	_, err = instance.NewService(repo, zap.NewNop()).SetUserActive(ctx, ada.id, pw, bob.id, false)
	assert.Error(t, err, "no verifier: refused")
	assert.Empty(t, repo.setActiveCall)
}

// ─── Bootstrap ────────────────────────────────────────────────────────────────

func observed() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	return zap.New(core), logs
}

func TestBootstrap_EnvEmailsGranted(t *testing.T) {
	log, logs := observed()
	repo := newFakeRepo(ada, bob, carol)
	svc := instance.NewService(repo, log)

	err := svc.Bootstrap(context.Background(), []string{"BOB@example.com", "nobody@example.com", "carol@example.com"})

	require.NoError(t, err)
	assert.Equal(t, instance.SourceEnv, repo.operators[bob.id])
	assert.Equal(t, instance.SourceEnv, repo.operators[carol.id])
	assert.NotContains(t, repo.operators, ada.id)
	for _, g := range repo.grants {
		assert.Nil(t, g.grantedBy)
	}
	assert.Zero(t, repo.promoteCalls, "env set → never promote the earliest user")
	unknown := logs.FilterMessageSnippet("no account").All()
	require.Len(t, unknown, 1, "unknown email logged")
	assert.Equal(t, zap.ErrorLevel, unknown[0].Level, "an unclaimed address is an armed operator grant")
}

// TOFT_OPERATOR_EMAILS is break-glass only: while any active operator exists
// it grants nothing, so a listed address registered later by someone else
// (with any invite) does not make them an operator at the next restart.
func TestBootstrap_EnvIgnoredWhileAnActiveOperatorExists(t *testing.T) {
	log, logs := observed()
	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = instance.SourceFirstRun

	require.NoError(t, instance.NewService(repo, log).Bootstrap(context.Background(), []string{bob.email}))

	assert.Empty(t, repo.grants)
	assert.NotContains(t, repo.operators, bob.id)
	assert.Contains(t, repo.operators, ada.id, "nothing is ever revoked")
	assert.Equal(t, 1, logs.FilterMessageSnippet("not applied").Len())
	assert.Zero(t, repo.promoteCalls)
}

func TestBootstrap_RevokedEnvOperatorStaysRevoked(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo(ada, bob)
	svc := newService(repo)
	env := []string{ada.email, bob.email}

	require.NoError(t, svc.Bootstrap(ctx, env), "first upgrade: no operator yet")
	require.Equal(t, instance.SourceEnv, repo.operators[bob.id])

	require.NoError(t, svc.RevokeOperator(ctx, ada.id, pw, bob.id))
	require.NoError(t, svc.Bootstrap(ctx, env), "next restart with the same env")

	assert.NotContains(t, repo.operators, bob.id, "an API revoke survives a restart")
}

func TestBootstrap_EnvAppliedWhenNoOperatorIsActive(t *testing.T) {
	inactiveAda := ada
	inactiveAda.active = false
	repo := newFakeRepo(inactiveAda, bob)
	repo.operators[ada.id] = instance.SourceFirstRun

	require.NoError(t, newService(repo).Bootstrap(context.Background(), []string{bob.email}))

	assert.Equal(t, instance.SourceEnv, repo.operators[bob.id], "break-glass")
	assert.Contains(t, repo.operators, ada.id)
}

func TestBootstrap_UnclaimedEnvEmailLoggedEvenWhenNotApplied(t *testing.T) {
	log, logs := observed()
	repo := newFakeRepo(ada)
	repo.operators[ada.id] = instance.SourceFirstRun

	require.NoError(t, instance.NewService(repo, log).Bootstrap(context.Background(), []string{"cofounder@example.com"}))

	assert.Equal(t, 1, logs.FilterLevelExact(zap.ErrorLevel).FilterMessageSnippet("no account").Len())
}

func TestBootstrap_EnvEmailAmbiguous_Skipped(t *testing.T) {
	log, logs := observed()
	bobUpper := fakeUser{id: "u-bob2", email: "Bob@Example.com", active: true}
	repo := newFakeRepo(ada, bob, bobUpper)

	require.NoError(t, instance.NewService(repo, log).Bootstrap(context.Background(), []string{"bob@example.com"}))

	assert.Empty(t, repo.grants)
	assert.Equal(t, 1, logs.FilterLevelExact(zap.ErrorLevel).FilterMessageSnippet("more than one account").Len())
}

func TestBootstrap_NoEnv_PromotesEarliestUser(t *testing.T) {
	log, logs := observed()
	repo := newFakeRepo(ada, bob)
	svc := instance.NewService(repo, log)

	require.NoError(t, svc.Bootstrap(context.Background(), nil))

	assert.Equal(t, 1, repo.promoteCalls)
	assert.Equal(t, instance.SourceBootstrap, repo.operators[ada.id])
	warn := logs.FilterLevelExact(zap.WarnLevel).FilterMessageSnippet("promoted the earliest-created user").All()
	require.Len(t, warn, 1)
	assert.Equal(t, ada.email, warn[0].ContextMap()["email"])
}

func TestBootstrap_NoEnv_OperatorsExist_NoOp(t *testing.T) {
	repo := newFakeRepo(ada, bob)
	repo.operators[bob.id] = instance.SourceAPI

	require.NoError(t, newService(repo).Bootstrap(context.Background(), nil))

	assert.NotContains(t, repo.operators, ada.id)
	assert.Len(t, repo.operators, 1)
}

func TestBootstrap_FreshInstance_NothingToDo(t *testing.T) {
	for _, env := range [][]string{nil, {"you@example.com"}} {
		log, logs := observed()
		repo := newFakeRepo()

		require.NoError(t, instance.NewService(repo, log).Bootstrap(context.Background(), env))

		assert.Empty(t, repo.operators)
		assert.Zero(t, repo.promoteCalls)
		assert.Zero(t, logs.FilterLevelExact(zap.ErrorLevel).Len(), "the first registration becomes the operator")
	}
}

func TestBootstrap_NoActiveOperator_LogsError(t *testing.T) {
	log, logs := observed()
	repo := newFakeRepo(ada)

	// The env list matches nobody, so no promotion and no operator.
	require.NoError(t, instance.NewService(repo, log).Bootstrap(context.Background(), []string{"nobody@example.com"}))

	assert.Equal(t, 1, logs.FilterLevelExact(zap.ErrorLevel).FilterMessageSnippet("this instance has no active operator").Len())
}

func TestBootstrap_RepoError_Returned(t *testing.T) {
	repo := newFakeRepo(ada)
	repo.promoteErr = errors.New(`relation "instance_operators" does not exist`)
	assert.Error(t, newService(repo).Bootstrap(context.Background(), nil))

	repo = newFakeRepo(ada)
	repo.findErr = errors.New("db down")
	assert.Error(t, newService(repo).Bootstrap(context.Background(), []string{ada.email}))
}

func TestRunBootstrap_StopsAfterSuccess(t *testing.T) {
	repo := newFakeRepo(ada)
	newService(repo).RunBootstrap(context.Background(), nil, time.Millisecond, 5)
	assert.Equal(t, 1, repo.promoteCalls)
}

func TestRunBootstrap_GivesUpAfterAttempts(t *testing.T) {
	log, logs := observed()
	repo := newFakeRepo(ada)
	repo.promoteErr = errors.New("not yet migrated")

	instance.NewService(repo, log).RunBootstrap(context.Background(), nil, time.Millisecond, 3)

	assert.Equal(t, 3, repo.promoteCalls)
	assert.Equal(t, 2, logs.FilterLevelExact(zap.WarnLevel).Len())
	assert.Equal(t, 1, logs.FilterLevelExact(zap.ErrorLevel).Len())
}

func TestRunBootstrap_StopsOnCancel(t *testing.T) {
	repo := newFakeRepo(ada)
	repo.promoteErr = errors.New("not yet migrated")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		newService(repo).RunBootstrap(ctx, nil, time.Hour, 40)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunBootstrap did not stop on cancel")
	}
}
