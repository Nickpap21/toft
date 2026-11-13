package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// An invite link signs up one account: a second use, an expired link, a
// link from a deactivated account and an unknown one are refused, and a
// sign-up that fails after the claim leaves the invite usable.
func TestCreateUserWithInvite(t *testing.T) {
	db := testdb.New(t)
	repo := auth.NewPostgresRepository(db)
	orgs := org.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	ctx := context.Background()

	var ownerID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&ownerID))
	o, err := orgs.CreateWithOwner(ctx, "Acme", ownerID)
	require.NoError(t, err)
	invite := func(token string, expires time.Time) {
		t.Helper()
		_, err := orgs.CreateInvite(ctx, o.ID, ownerID, "developer", token, expires)
		require.NoError(t, err)
	}
	newUser := func(email string) auth.NewUser {
		return auth.NewUser{Email: email, Name: "New", PasswordHash: "x"}
	}

	invite("live", time.Now().Add(time.Hour))
	_, err = repo.CreateUserWithInvite(ctx, newUser("owner@toft.test"), "live")
	require.ErrorIs(t, err, auth.ErrUserExists)

	id, err := repo.CreateUserWithInvite(ctx, newUser("first@toft.test"), "live")
	require.NoError(t, err, "the failed sign-up above rolled its claim back")
	var role, activeOrg string
	require.NoError(t, db.QueryRow(ctx,
		`SELECT m.role, u.active_org_id::text FROM org_members m JOIN users u ON u.id = m.user_id WHERE m.user_id = $1 AND m.org_id = $2`,
		id, o.ID).Scan(&role, &activeOrg))
	require.Equal(t, "developer", role)
	require.Equal(t, o.ID, activeOrg)

	_, err = repo.CreateUserWithInvite(ctx, newUser("second@toft.test"), "live")
	require.ErrorIs(t, err, org.ErrInviteUsed)

	invite("expired", time.Now().Add(-time.Minute))
	_, err = repo.CreateUserWithInvite(ctx, newUser("late@toft.test"), "expired")
	require.ErrorIs(t, err, org.ErrInviteExpired)

	_, err = repo.CreateUserWithInvite(ctx, newUser("guess@toft.test"), "unknown")
	require.ErrorIs(t, err, org.ErrInviteNotFound)

	invite("orphan", time.Now().Add(time.Hour))
	_, err = db.Exec(ctx, `UPDATE users SET is_active = FALSE WHERE id = $1`, ownerID)
	require.NoError(t, err)
	_, err = repo.CreateUserWithInvite(ctx, newUser("orphan@toft.test"), "orphan")
	require.ErrorIs(t, err, org.ErrInviteExpired, "a deactivated account's invite is dead, and says only expired")

	var accounts int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&accounts))
	require.Equal(t, 2, accounts, "only the owner and the first sign-up exist")
}

// Concurrent redemptions of one link: exactly one signs up.
func TestCreateUserWithInviteConcurrently(t *testing.T) {
	db := testdb.New(t)
	repo := auth.NewPostgresRepository(db)
	orgs := org.NewPostgresRepository(db, crypto.NewKeys("test-encryption-key", ""))
	ctx := context.Background()

	var ownerID string
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('owner@toft.test', 'Owner', 'x') RETURNING id::text`,
	).Scan(&ownerID))
	o, err := orgs.CreateWithOwner(ctx, "Acme", ownerID)
	require.NoError(t, err)
	_, err = orgs.CreateInvite(ctx, o.ID, ownerID, "developer", "contested", time.Now().Add(time.Hour))
	require.NoError(t, err)

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = repo.CreateUserWithInvite(ctx, auth.NewUser{Email: fmt.Sprintf("u%d@toft.test", i), Name: "U", PasswordHash: "x"}, "contested")
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		require.ErrorIs(t, err, org.ErrInviteUsed)
	}
	require.Equal(t, 1, won)
}

// Two people open the setup URL at once (7g): the database lets one first
// account through, and every other registration is 403 registration_closed,
// which the setup screen turns into Sign in. Without the setup code nothing
// is created.
func TestFirstRunRegistrationConcurrently(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := auth.NewService(user.NewPostgresRepository(db), auth.NewPostgresRepository(db), &fakeGate{setupToken: "4K7Q-2M9X"}, zap.NewNop(), "test-secret")
	auth.NewHandler(svc, zap.NewNop()).RegisterRoutes(r.Group("/api/v1/auth"))
	count := func(table string) int {
		var n int
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		return n
	}

	w, _ := do(t, r, "/api/v1/auth/register", map[string]string{"email": "ada@toft.test", "name": "Ada", "password": "twelve-chars"}, nil)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, count("users"), "no setup code, no account")

	// The requests run concurrently; their answers are checked after, on
	// the test's own goroutine.
	const n = 8
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range n {
		body, err := json.Marshal(map[string]string{
			"email": fmt.Sprintf("u%d@toft.test", i), "name": "Ada", "password": "twelve-chars", "setup_token": "4K7Q-2M9X"})
		require.NoError(t, err)
		recs[i] = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.ServeHTTP(recs[i], req)
		}()
	}
	wg.Wait()
	won := 0
	for _, w := range recs {
		if w.Code == http.StatusCreated {
			won++
			continue
		}
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		var out struct {
			Data struct {
				Code string `json:"code"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		require.Equal(t, "registration_closed", out.Data.Code)
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, count("users"))
	require.Equal(t, 1, count("organizations"))
	require.Equal(t, 1, count("instance_operators"))
}
