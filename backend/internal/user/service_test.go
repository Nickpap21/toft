package user_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type fakeRepo struct {
	revoked   []string
	signedOut []string // session token hashes
	themes    []string
	users     map[string]*user.User
	activeErr error
}

func (f *fakeRepo) Create(ctx context.Context, email, name, passwordHash string) (*user.User, error) {
	return nil, errors.New("unused")
}
func (f *fakeRepo) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	return nil, errors.New("unused")
}
func (f *fakeRepo) GetByID(ctx context.Context, id string) (*user.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, user.ErrNotFound
}
func (f *fakeRepo) RequestIdentity(ctx context.Context, id, orgID, _ string) (user.Identity, error) {
	active, err := f.IsActive(ctx, id)
	return user.Identity{Active: active}, err
}

func (f *fakeRepo) IsActive(ctx context.Context, id string) (bool, error) {
	if f.activeErr != nil {
		return false, f.activeErr
	}
	if u, ok := f.users[id]; ok {
		return u.IsActive, nil
	}
	return false, user.ErrNotFound
}
func (f *fakeRepo) UpdateLastLogin(ctx context.Context, id string) error { return nil }
func (f *fakeRepo) UpdateProfile(ctx context.Context, id, name string) (*user.User, error) {
	u := f.users[id]
	u.Name = name
	return u, nil
}
func (f *fakeRepo) UpdatePassword(ctx context.Context, id, passwordHash string) error { return nil }
func (f *fakeRepo) UpdateTheme(ctx context.Context, id, theme string) error {
	f.themes = append(f.themes, theme)
	return nil
}
func (f *fakeRepo) RevokeSessions(ctx context.Context, id string) error {
	f.revoked = append(f.revoked, id)
	return nil
}

func (f *fakeRepo) RevokeSession(ctx context.Context, tokenHash string, expires time.Time) error {
	f.signedOut = append(f.signedOut, tokenHash)
	return nil
}

type fakeChecker struct {
	ok  bool
	err error
}

func (f fakeChecker) IsOperator(ctx context.Context, userID string) (bool, error) { return f.ok, f.err }

func newRepo() *fakeRepo {
	return &fakeRepo{users: map[string]*user.User{
		"u-1": {ID: "u-1", Email: "ada@example.com", Name: "Ada", PasswordHash: "secret-hash", Role: "admin", IsActive: true},
		"u-2": {ID: "u-2", Email: "bob@example.com", Name: "Bob", IsActive: false},
	}}
}

func TestGetMe_OperatorFlag(t *testing.T) {
	ctx := context.Background()

	p, err := user.NewService(newRepo(), zap.NewNop()).WithOperatorChecker(fakeChecker{ok: true}).GetMe(ctx, "u-1")
	require.NoError(t, err)
	assert.True(t, p.IsOperator)
	assert.Equal(t, "admin", p.Role, "role is kept for API compatibility")

	p, err = user.NewService(newRepo(), zap.NewNop()).WithOperatorChecker(fakeChecker{err: errors.New("db down")}).GetMe(ctx, "u-1")
	require.NoError(t, err, "/me must not fail because the operator check failed")
	assert.False(t, p.IsOperator)

	p, err = user.NewService(newRepo(), nil).GetMe(ctx, "u-1")
	require.NoError(t, err)
	assert.False(t, p.IsOperator, "no checker → false")
}

func TestUpdateProfile_ReturnsProfile(t *testing.T) {
	p, err := user.NewService(newRepo(), zap.NewNop()).WithOperatorChecker(fakeChecker{ok: true}).UpdateProfile(context.Background(), "u-1", "Ada L")
	require.NoError(t, err)
	assert.Equal(t, "Ada L", p.Name)
	assert.True(t, p.IsOperator)
}

func TestIsActive(t *testing.T) {
	ctx := context.Background()
	svc := user.NewService(newRepo(), zap.NewNop())

	active, err := svc.IsActive(ctx, "u-1")
	require.NoError(t, err)
	assert.True(t, active)

	active, err = svc.IsActive(ctx, "u-2")
	require.NoError(t, err)
	assert.False(t, active)

	active, err = svc.IsActive(ctx, "u-deleted")
	require.NoError(t, err, "an unknown user is inactive, not an error")
	assert.False(t, active)

	repo := newRepo()
	repo.activeErr = errors.New("db down")
	_, err = user.NewService(repo, zap.NewNop()).IsActive(ctx, "u-1")
	assert.Error(t, err)
}

func TestVerifyPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	require.NoError(t, err)
	svc := user.NewService(&fakeRepo{users: map[string]*user.User{"u1": {ID: "u1", PasswordHash: string(hash)}}}, zap.NewNop())

	ok, err := svc.VerifyPassword(context.Background(), "u1", "correct horse")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = svc.VerifyPassword(context.Background(), "u1", "wrong")
	require.NoError(t, err)
	assert.False(t, ok)

	_, err = svc.VerifyPassword(context.Background(), "u-nobody", "correct horse")
	assert.Error(t, err, "an unknown account is an error, not a wrong password")
}
