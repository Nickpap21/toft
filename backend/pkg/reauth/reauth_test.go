package reauth_test

import (
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestAProofHoldsForItsSessionUntilItExpires(t *testing.T) {
	token, expires, err := reauth.Issue(secret, "u-ada", "session-1", time.Now(), reauth.TTL)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(reauth.TTL), expires, time.Second)

	assert.True(t, reauth.Valid(secret, token, "u-ada", "session-1"))
	assert.False(t, reauth.Valid(secret, token, "u-ada", "session-2"), "another session of the same account")
	assert.False(t, reauth.Valid(secret, token, "u-bob", "session-1"), "another account")
	assert.False(t, reauth.Valid([]byte("another secret, 32 bytes long ..."), token, "u-ada", "session-1"))
	assert.False(t, reauth.Valid(secret, token+"x", "u-ada", "session-1"), "tampered")
	assert.False(t, reauth.Valid(secret, "", "u-ada", "session-1"))

	old, _, err := reauth.Issue(secret, "u-ada", "session-1", time.Now().Add(-reauth.TTL-time.Second), reauth.TTL)
	require.NoError(t, err)
	assert.False(t, reauth.Valid(secret, old, "u-ada", "session-1"), "expired")
}

// A session token is no proof, and a proof is no session token: they are
// signed with different keys.
func TestSessionTokensAndProofsDoNotMix(t *testing.T) {
	session, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u-ada", "aud": "toft-reauth", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(secret)
	require.NoError(t, err)
	assert.False(t, reauth.Valid(secret, session, "u-ada", session), "a session token signed with the session secret")

	proof, _, err := reauth.Issue(secret, "u-ada", "session-1", time.Now(), reauth.TTL)
	require.NoError(t, err)
	_, err = jwt.Parse(proof, func(*jwt.Token) (any, error) { return secret, nil })
	assert.Error(t, err, "a proof does not verify as a session token")
}
