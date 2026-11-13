// Package reauth issues and checks the proof that a session's user typed
// their password again a moment ago (D-23, D-53). Host shells and host-key
// replacement require it; one password covers them for TTL.
//
// The proof is a JWT bound to the session it was issued for (a hash of that
// session token) and signed with a key derived from the session secret, so
// it is worthless with any other session and can never pass as a session
// token itself, nor a session token as a proof.
package reauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TTL is how long one password confirmation lasts.
const TTL = 10 * time.Minute

// SignInTTL is how long a fresh sign-in counts as one (D-161).
const SignInTTL = 5 * time.Minute

// Header carries the proof; a WebSocket handshake, which cannot carry
// headers, sends ?reauth= instead.
const Header = "X-Toft-Reauth"

const audience = "toft-reauth"

// key derives the signing key from the session secret, so the two kinds of
// token never verify with each other's key.
func key(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("toft reauth v1"))
	return m.Sum(nil)
}

func sessionHash(session string) string {
	sum := sha256.Sum256([]byte(session))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Issue returns a proof for userID's session, valid for ttl, until the
// returned time.
func Issue(secret []byte, userID, session string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	expires := now.Add(ttl)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID,
		"sid": sessionHash(session),
		"aud": audience,
		"iat": now.Unix(),
		"exp": expires.Unix(),
	}).SignedString(key(secret))
	return token, expires, err
}

// Valid reports whether token is an unexpired proof for userID's session.
func Valid(secret []byte, token, userID, session string) bool {
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return key(secret), nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	sub, _ := claims.GetSubject()
	sid, _ := claims["sid"].(string)
	return sub == userID && hmac.Equal([]byte(sid), []byte(sessionHash(session)))
}
