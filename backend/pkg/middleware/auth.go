package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	userIDKey        = "userID"
	orgIDKey         = "orgID"
	memberRoleKey    = "memberRole"
	apiKeyProjectKey = "apiKeyProject"
	apiKeyScopesKey  = "apiKeyScopes"
	apiKeyServices   = "apiKeyServices"
	apiKeyKey        = "apiKey"
	apiKeyIDKey      = "apiKeyID"
	reauthKey        = "reauthenticated"
	agentKey         = "agent"
	sessionKey       = "session"
)

// Session is the session token a request carries: its sha256 and when it
// expires, what signing it out records.
type Session struct {
	Hash    string
	Expires time.Time
}

// AgentTokenPrefix starts an agent connection's access token (D-100).
const AgentTokenPrefix = "toft_oat_"

// AgentIdentity is the agent connection an access token belongs to: who it
// acts for, in which workspace, which projects (all, or ProjectIDs) and
// with which scopes. Paused is "workspace" or "instance" while agents are
// paused there.
type AgentIdentity struct {
	ConnectionID string
	Name         string
	UserID       string
	OrgID        string
	AllProjects  bool
	ProjectIDs   []string
	Scopes       []string
	Paused       string
}

// AgentTokenValidator checks an agent's access token. A key validator that
// also implements it (cmd/server's) lets agents through the same middleware
// as API keys: agents are never sessions.
type AgentTokenValidator interface {
	ValidateAgentToken(ctx context.Context, rawToken string) (AgentIdentity, error)
}

// OrgHeader names the workspace a request works in (D-52). The panel sends
// the one its tab shows, so a switch in another tab or on another device
// never moves it; a WebSocket handshake, which cannot carry headers, sends
// ?org= instead. Without it a request works in the workspace the account
// last switched to (users.active_org_id).
const OrgHeader = "X-Toft-Org"

// APIKeyIdentity is who an API key acts for. For a project-scoped key,
// ProjectID, OrgID (the project's workspace) and Scopes are set; for an
// unscoped key only UserID is.
type APIKeyIdentity struct {
	// ID is the key's own (D-97: what a pull request was opened with).
	ID        string
	UserID    string
	ProjectID string
	OrgID     string
	Scopes    []string
	// ServiceIDs limit a project key to those services (K5); empty is the
	// whole project.
	ServiceIDs []string
}

// APIKeyExpiredError is a key past its expires_at (K4). Its message is what
// the client is told, so a CI log says why and what to do.
type APIKeyExpiredError struct{ At time.Time }

func (e *APIKeyExpiredError) Error() string {
	return "this API key expired on " + e.At.UTC().Format("2006-01-02") + "; create a new one in Settings → API keys"
}

// APIKeyValidator checks a raw API key and returns who it acts for.
// Satisfied by apikey.Service.
type APIKeyValidator interface {
	ValidateAPIKey(ctx context.Context, rawKey string) (APIKeyIdentity, error)
}

// OrgLookup resolves the orgID and member role from a userID.
// Satisfied by org.Service.
type OrgLookup interface {
	GetForUser(ctx context.Context, userID string) (orgID, role string, err error)
	// RoleIn returns the user's role in orgID, or an error if they are not
	// a member.
	RoleIn(ctx context.Context, userID, orgID string) (string, error)
}

// UserStatusChecker reports whether a user may authenticate (users.is_active).
// An unknown user is inactive. Satisfied by user.Service.
type UserStatusChecker interface {
	IsActive(ctx context.Context, userID string) (bool, error)
	// RequestIdentity reads, in one query, the account's state, the moment
	// before which its session tokens are refused, and its role in orgID, or
	// in the workspace it last switched to when orgID is "" (Role "" when it
	// is not a member), and whether the session token with the sha256
	// sessionHash was signed out ("" for an API key or an agent).
	RequestIdentity(ctx context.Context, userID, orgID, sessionHash string) (Identity, error)
}

// Identity is what every authenticated request needs to know about its
// user, read in one query. The user package cannot be imported here (it
// imports this one), so the type lives here, like APIKeyIdentity.
type Identity struct {
	Active bool
	// TokensValidFrom: a session token issued before it is refused. A
	// password change moves it. Zero means no such moment is recorded.
	TokensValidFrom time.Time
	// OrgID and Role are the workspace the request works in and the user's
	// role there; both "" when there is none or the user is not a member.
	OrgID string
	Role  string
	// SessionRevoked: the session token was signed out (SessionFrom).
	SessionRevoked bool
}

// activeRecheckInterval is how often a request that is still running is
// re-checked against users.is_active. A deactivated user's open PTY sessions
// and SSE streams end within this interval. Shorter requests never re-check.
var activeRecheckInterval = 15 * time.Second

// JWT returns middleware that validates only JWT tokens.
// Kept for backward compatibility; internally delegates to Auth.
func JWT(secret string) gin.HandlerFunc {
	return Auth(secret, nil)
}

// Auth returns middleware that validates JWT tokens, and also API keys
// (tokens starting with "paas_") when keyValidator is non-nil.
func Auth(secret string, keyValidator APIKeyValidator) gin.HandlerFunc {
	return authMiddleware(secret, keyValidator, nil, nil)
}

// AuthWithOrg validates JWT/API key, rejects deactivated users when users is
// non-nil, AND injects orgID + memberRole into the Gin context. This should
// be used on all protected routes that need org-scoped access control.
func AuthWithOrg(secret string, keyValidator APIKeyValidator, orgSvc OrgLookup, users UserStatusChecker) gin.HandlerFunc {
	return authMiddleware(secret, keyValidator, orgSvc, users)
}

func authMiddleware(secret string, keyValidator APIKeyValidator, orgSvc OrgLookup, users UserStatusChecker) gin.HandlerFunc {
	recheckEvery := activeRecheckInterval
	keyFunc := func(*jwt.Token) (any, error) { return []byte(secret), nil }

	return func(c *gin.Context) {
		// Extract token: the Authorization header, or ?token= on a WebSocket
		// handshake (the host PTY), since browsers cannot set headers on one.
		// A token in any other URL is ignored: URLs end up in access logs and
		// browser history.
		tokenString := ""
		header := c.GetHeader("Authorization")
		if strings.HasPrefix(header, "Bearer ") {
			tokenString = strings.TrimPrefix(header, "Bearer ")
		} else if websocket.IsWebSocketUpgrade(c.Request) {
			tokenString = c.Query("token")
		}

		if tokenString == "" {
			respond.Error(c, http.StatusUnauthorized, "missing or invalid authorization")
			c.Abort()
			return
		}

		var userID string
		var key APIKeyIdentity
		var agent AgentIdentity
		var issuedAt time.Time // a JWT's iat; zero for an API key
		var session Session    // a JWT's; zero for an API key
		viaJWT := false

		agents, _ := keyValidator.(AgentTokenValidator)
		if strings.HasPrefix(tokenString, AgentTokenPrefix) && agents != nil {
			a, err := agents.ValidateAgentToken(c.Request.Context(), tokenString)
			if err != nil {
				respond.ErrorData(c, http.StatusUnauthorized, "invalid, expired or revoked agent token", gin.H{"code": "invalid_token"})
				c.Abort()
				return
			}
			if a.Paused != "" {
				msg := "agents are paused in this workspace"
				if a.Paused == "instance" {
					msg = "agents are paused on this panel"
				}
				respond.ErrorData(c, http.StatusForbidden, msg, gin.H{"code": "agents_paused"})
				c.Abort()
				return
			}
			userID, agent = a.UserID, a
			// An agent is never a session: everything that refuses API keys
			// (session_required) refuses it too.
			c.Set(apiKeyKey, true)
			c.Set(agentKey, a)
		} else if strings.HasPrefix(tokenString, "paas_") && keyValidator != nil {
			// API key path — tokens prefixed with "paas_"
			k, err := keyValidator.ValidateAPIKey(c.Request.Context(), tokenString)
			var expired *APIKeyExpiredError
			if errors.As(err, &expired) {
				respond.ErrorData(c, http.StatusUnauthorized, expired.Error(), gin.H{"code": "api_key_expired"})
				c.Abort()
				return
			}
			if err != nil {
				respond.Error(c, http.StatusUnauthorized, "invalid or revoked api key")
				c.Abort()
				return
			}
			userID, key = k.UserID, k
			c.Set(apiKeyKey, true)
			c.Set(apiKeyIDKey, k.ID)
			if key.ProjectID != "" {
				// Set before anything else, so the scope is enforced
				// (apikey.EnforceScopes) whatever happens below.
				c.Set(apiKeyProjectKey, key.ProjectID)
				c.Set(apiKeyScopesKey, key.Scopes)
				c.Set(apiKeyServices, key.ServiceIDs)
			}
		} else {
			// JWT path
			viaJWT = true
			// Only what auth.Service signs: HS256, with an expiry, in
			// canonical base64: a session is its token's hash, so another
			// spelling of the same signature would undo its sign-out.
			token, err := jwt.Parse(tokenString, keyFunc,
				jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
				jwt.WithExpirationRequired(),
				jwt.WithStrictDecoding())
			if err != nil || !token.Valid {
				respond.Error(c, http.StatusUnauthorized, "invalid or expired token")
				c.Abort()
				return
			}
			sub, err := token.Claims.GetSubject()
			if err != nil || sub == "" {
				respond.Error(c, http.StatusUnauthorized, "invalid token claims")
				c.Abort()
				return
			}
			userID = sub
			if iat, err := token.Claims.GetIssuedAt(); err == nil && iat != nil {
				issuedAt = iat.Time
			}
			sum := sha256.Sum256([]byte(tokenString))
			session = Session{Hash: hex.EncodeToString(sum[:])}
			if exp, err := token.Claims.GetExpirationTime(); err == nil && exp != nil {
				session.Expires = exp.Time
			}
			// A proof that this session confirmed its password a moment ago
			// (reauth, D-53); RequireReauth reads the result. A WebSocket
			// handshake carries it as ?reauth=, as it does the token.
			proof := c.GetHeader(reauth.Header)
			if proof == "" && websocket.IsWebSocketUpgrade(c.Request) {
				proof = c.Query("reauth")
			}
			if proof != "" && reauth.Valid([]byte(secret), proof, userID, tokenString) {
				c.Set(reauthKey, true)
			}
		}

		// The workspace the request names, if any (OrgHeader), in canonical
		// form so it compares with the IDs the database returns.
		requested := c.GetHeader(OrgHeader)
		if requested == "" && websocket.IsWebSocketUpgrade(c.Request) {
			requested = c.Query("org")
		}
		if requested != "" {
			id, err := uuid.Parse(requested)
			if err != nil {
				respond.ErrorData(c, http.StatusBadRequest, OrgHeader+" must be a workspace ID", gin.H{"code": "invalid_workspace"})
				c.Abort()
				return
			}
			requested = id.String()
		}

		// A deactivated (or deleted) user is locked out at once, whichever
		// credential they present: JWTs live 30 days and API keys forever. A
		// JWT issued before the account's tokens_valid_from (a password
		// change) is refused too; API keys are revoked by deleting them.
		var ident Identity
		if users != nil {
			var err error
			ident, err = users.RequestIdentity(c.Request.Context(), userID, requested, session.Hash)
			if err != nil {
				respond.Error(c, http.StatusInternalServerError, "failed to verify account status")
				c.Abort()
				return
			}
			if !ident.Active {
				respond.Error(c, http.StatusUnauthorized, "this account has been deactivated")
				c.Abort()
				return
			}
			// iat has second precision: a token minted in the same second as
			// the change is the one the change answered with.
			// A signed-out session (SessionFrom) is refused the same way.
			signedOut := func(ident Identity) bool {
				return viaJWT && (ident.SessionRevoked || !ident.TokensValidFrom.IsZero() &&
					(issuedAt.IsZero() || issuedAt.Before(ident.TokensValidFrom.Truncate(time.Second))))
			}
			if signedOut(ident) {
				respond.Error(c, http.StatusUnauthorized, "this session was signed out; sign in again")
				c.Abort()
				return
			}

			// The checks above only cover the start of the request. Long-lived
			// ones (PTY WebSocket, SSE streams) get a context that is cancelled
			// if the user is deactivated or the session signed out while they
			// are open.
			ctx, stop := watchActive(c.Request.Context(), recheckEvery, func(ctx context.Context) bool {
				ident, err := users.RequestIdentity(ctx, userID, requested, session.Hash)
				return err != nil || ident.Active && !signedOut(ident)
			})
			defer stop()
			c.Request = c.Request.WithContext(ctx)
		}

		c.Set(userIDKey, userID)
		if viaJWT {
			c.Set(sessionKey, session)
		}
		// Who acts beyond the user, for use cases and the audit log (D-100).
		if agent.ConnectionID != "" {
			c.Request = c.Request.WithContext(actor.WithAgent(c.Request.Context(),
				actor.Agent{ConnectionID: agent.ConnectionID, Name: agent.Name, UserID: agent.UserID, OrgID: agent.OrgID}))
		} else if key.ID != "" {
			c.Request = c.Request.WithContext(actor.WithAPIKey(c.Request.Context(), key.ID))
		}

		// Inject org context when an OrgLookup is provided. A named workspace
		// is never swapped for another: the request works there or nowhere,
		// so what a tab shows is always where it acts.
		if orgSvc != nil && (key.ProjectID != "" || agent.ConnectionID != "") {
			// A project-scoped key acts in the project's workspace, and an
			// agent in its connection's, with its owner's role there,
			// whichever workspace the owner has open.
			orgID := key.OrgID
			if agent.ConnectionID != "" {
				orgID = agent.OrgID
			}
			if requested != "" && requested != orgID {
				workspaceUnavailable(c)
				return
			}
			role, err := orgSvc.RoleIn(c.Request.Context(), userID, orgID)
			if err != nil {
				respond.Error(c, http.StatusUnauthorized, "the api key's owner is no longer a member of its project's workspace")
				c.Abort()
				return
			}
			c.Set(orgIDKey, orgID)
			c.Set(memberRoleKey, role)
		} else if orgSvc != nil && requested != "" {
			role := ""
			if users != nil {
				if ident.OrgID == requested {
					role = ident.Role
				}
			} else if r, err := orgSvc.RoleIn(c.Request.Context(), userID, requested); err == nil {
				role = r
			}
			if role == "" {
				workspaceUnavailable(c)
				return
			}
			c.Set(orgIDKey, requested)
			c.Set(memberRoleKey, role)
		} else if ident.OrgID != "" {
			// The identity query already found the open workspace and the
			// role in it.
			c.Set(orgIDKey, ident.OrgID)
			c.Set(memberRoleKey, ident.Role)
		} else if orgSvc != nil {
			// No workspace open, or no longer a member of it: GetForUser
			// falls back to the earliest membership and heals active_org_id.
			orgID, role, err := orgSvc.GetForUser(c.Request.Context(), userID)
			if err == nil {
				c.Set(orgIDKey, orgID)
				c.Set(memberRoleKey, role)
			}
			// If no org found (e.g. invite-accept flow for a brand-new user),
			// orgID will be "". Handlers must guard against this where needed.
		}

		c.Next()
	}
}

// workspaceUnavailable refuses a request that names a workspace the user is
// not a member of (removed from it, or it was deleted). The panel then opens
// the account's default workspace and says why.
func workspaceUnavailable(c *gin.Context) {
	respond.ErrorData(c, http.StatusForbidden, "you are not a member of this workspace", gin.H{"code": "workspace_unavailable"})
	c.Abort()
}

// watchActive returns a context derived from parent that is cancelled once
// allowed reports false, checking every interval until the returned stop
// function is called. allowed reports true when its check fails, so a failed
// check leaves the request running; the next tick tries again.
func watchActive(parent context.Context, every time.Duration, allowed func(context.Context) bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !allowed(ctx) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// SessionFrom returns the request's session token; false for an API key or
// an agent.
func SessionFrom(c *gin.Context) (Session, bool) {
	s, ok := c.Get(sessionKey)
	session, _ := s.(Session)
	return session, ok
}

// UserIDFrom extracts the authenticated user ID from the Gin context.
func UserIDFrom(c *gin.Context) string {
	id, _ := c.Get(userIDKey)
	s, _ := id.(string)
	return s
}

// OrgIDFrom extracts the org ID injected by AuthWithOrg from the Gin context.
func OrgIDFrom(c *gin.Context) string {
	id, _ := c.Get(orgIDKey)
	s, _ := id.(string)
	return s
}

// APIKeyScopeFrom returns the project and scopes of the project-scoped API
// key the request was authenticated with; ok is false for a JWT or an
// unscoped key.
func APIKeyScopeFrom(c *gin.Context) (projectID string, scopes []string, ok bool) {
	p, _ := c.Get(apiKeyProjectKey)
	projectID, _ = p.(string)
	if projectID == "" {
		return "", nil, false
	}
	sc, _ := c.Get(apiKeyScopesKey)
	scopes, _ = sc.([]string)
	return projectID, scopes, true
}

// APIKeyServicesFrom returns the services the request's project key is
// limited to; empty for the whole project, another kind of key or a JWT.
func APIKeyServicesFrom(c *gin.Context) []string {
	v, _ := c.Get(apiKeyServices)
	ids, _ := v.([]string)
	return ids
}

// ViaAPIKey reports whether the request was authenticated with an API key,
// scoped or not, rather than a session.
func ViaAPIKey(c *gin.Context) bool {
	return c.GetBool(apiKeyKey)
}

// AgentFrom returns the agent connection the request was authenticated
// with; ok is false for a session or an API key.
func AgentFrom(c *gin.Context) (AgentIdentity, bool) {
	a, ok := c.Get(agentKey)
	if !ok {
		return AgentIdentity{}, false
	}
	id, ok := a.(AgentIdentity)
	return id, ok
}

// APIKeyIDFrom returns the ID of the API key the request was authenticated
// with, "" for a session.
func APIKeyIDFrom(c *gin.Context) string {
	return c.GetString(apiKeyIDKey)
}

// RequireReauth lets the request through only when its session confirmed
// its password within reauth.TTL (D-23, D-53), and otherwise answers 403:
// reauth_required, or session_required for an API key, which has no password
// to confirm. Call it after the authorization check, so a caller who may not
// act at all is never asked for a password.
func RequireReauth(c *gin.Context) bool {
	switch {
	case ViaAPIKey(c):
		respond.ErrorData(c, http.StatusForbidden, "this needs a signed-in session and your password, not an API key", gin.H{"code": "session_required"})
	case !c.GetBool(reauthKey):
		respond.ErrorData(c, http.StatusForbidden, "confirm your password to continue", gin.H{"code": "reauth_required"})
	default:
		return true
	}
	c.Abort()
	return false
}

// Reauthenticated reports whether the session confirmed its password within
// reauth.TTL, for a check that asks for it only sometimes (loosening the
// agent policy, D-100). RequireReauth answers for the usual case.
func Reauthenticated(c *gin.Context) bool {
	return !ViaAPIKey(c) && c.GetBool(reauthKey)
}

// MemberRoleFrom extracts the member role injected by AuthWithOrg from the Gin context.
func MemberRoleFrom(c *gin.Context) string {
	role, _ := c.Get(memberRoleKey)
	s, _ := role.(string)
	return s
}
