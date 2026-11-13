package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/netcheck"
)

// ─── Metadata (RFC 8414, RFC 9728) ───────────────────────────────────────────

// ServerMetadata is the authorization server's metadata for issuer, the
// panel's origin.
func ServerMetadata(issuer string) map[string]any {
	return map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/oauth/authorize",
		"token_endpoint":                                 issuer + "/api/v1/oauth/token",
		"registration_endpoint":                          issuer + "/api/v1/oauth/register",
		"revocation_endpoint":                            issuer + "/api/v1/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"scopes_supported":                               append(slices.Clone(ConsentScopes), "offline_access"),
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	}
}

// Resource is the MCP endpoint, the one resource tokens are for.
func Resource(issuer string) string { return issuer + "/mcp" }

// ResourceMetadata is the protected resource metadata of the MCP endpoint.
func ResourceMetadata(issuer string) map[string]any {
	return map[string]any{
		"resource":                 Resource(issuer),
		"authorization_servers":    []string{issuer},
		"scopes_supported":         ConsentScopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Toft",
	}
}

// ─── Clients ─────────────────────────────────────────────────────────────────

// maxRedirectURI bounds what an unauthenticated registration stores
// (oauth_clients_redirect_uris_size).
const maxRedirectURI = 2048

// validRedirect accepts https, and http only to this computer (RFC 8252:
// a native app's loopback callback).
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		return isLoopback(u.Hostname())
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redirectMatches compares a request's redirect URI with a registered one.
// A loopback URI matches on any port (RFC 8252 §7.3): native apps take a
// free port at each sign-in.
func redirectMatches(registered []string, got string) bool {
	g, err := url.Parse(got)
	if err != nil {
		return false
	}
	for _, r := range registered {
		if r == got {
			return true
		}
		u, err := url.Parse(r)
		if err != nil {
			continue
		}
		if u.Scheme == "http" && g.Scheme == "http" && isLoopback(u.Hostname()) && u.Hostname() == g.Hostname() &&
			u.Path == g.Path && u.RawQuery == g.RawQuery {
			return true
		}
	}
	return false
}

// Registration is a dynamic client registration request (RFC 7591).
type Registration struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// Register registers a public client.
func (s *Service) Register(ctx context.Context, r Registration) (map[string]any, error) {
	name := strings.TrimSpace(r.ClientName)
	if name == "" {
		name = "An MCP client"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if len(r.RedirectURIs) == 0 || len(r.RedirectURIs) > 10 {
		return nil, oauthErr(400, "invalid_redirect_uri", "give between 1 and 10 redirect_uris")
	}
	for _, u := range r.RedirectURIs {
		if len(u) > maxRedirectURI {
			return nil, oauthErr(400, "invalid_redirect_uri", fmt.Sprintf("a redirect URI is at most %d characters", maxRedirectURI))
		}
		if !validRedirect(u) {
			return nil, oauthErr(400, "invalid_redirect_uri", "redirect URIs must be https, or http to localhost: "+u)
		}
	}
	if m := r.TokenEndpointAuthMethod; m != "" && m != "none" {
		return nil, oauthErr(400, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) register here")
	}
	for _, g := range r.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return nil, oauthErr(400, "invalid_client_metadata", "grant_types may be authorization_code and refresh_token")
		}
	}
	c, err := s.repo.InsertDCRClient(ctx, newSecret("toft_client_"), name, r.RedirectURIs, MaxUnusedDCR)
	if errors.Is(err, ErrFull) {
		// Clean frees registrations a day old; an hour is a fair wait.
		return nil, &OAuthError{Status: 429, Code: "temporarily_unavailable", RetryAfter: 3600,
			Desc: "this panel holds as many unused registrations as it takes; try again later, or use a Client ID Metadata Document"}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"client_id": c.ClientID, "client_id_issued_at": time.Now().Unix(), "client_name": c.Name,
		"redirect_uris": c.RedirectURIs, "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	}, nil
}

// metadataFetcher fetches a Client ID Metadata Document.
type metadataFetcher func(ctx context.Context, clientID string) (name string, redirectURIs []string, err error)

// isCIMD: a client ID that is an https URL with a path is a metadata
// document's address.
func isCIMD(clientID string) bool {
	u, err := url.Parse(clientID)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Path != "" && u.Path != "/"
}

// client resolves a client ID: a registered client, or a metadata document,
// fetched again once a day.
func (s *Service) client(ctx context.Context, clientID string) (*Client, error) {
	c, err := s.repo.GetClient(ctx, clientID)
	if err == nil && (c.Kind == "dcr" || time.Since(c.FetchedAt) < 24*time.Hour) {
		return c, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !isCIMD(clientID) {
		return nil, oauthErr(400, "invalid_client", "this client isn't registered with Toft")
	}
	name, uris, ferr := s.fetch(ctx, clientID)
	if ferr != nil {
		if c != nil {
			return c, nil // keep using the copy we have
		}
		return nil, oauthErr(400, "invalid_client", "Toft couldn't read the client's metadata document: "+ferr.Error())
	}
	return s.repo.UpsertCIMDClient(ctx, clientID, name, uris)
}

// fetchClientMetadata reads a metadata document: https only, public
// addresses only (netcheck: never the panel's own network), 5 KB, 3
// seconds, no redirects.
func fetchClientMetadata(ctx context.Context, clientID string) (string, []string, error) {
	client := netcheck.NewPublicOnlyClient(3*time.Second, false)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("it answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 5<<10+1))
	if err != nil {
		return "", nil, err
	}
	if len(raw) > 5<<10 {
		return "", nil, errors.New("it is larger than 5 KB")
	}
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, errors.New("it isn't JSON")
	}
	if doc.ClientID != clientID {
		return "", nil, errors.New("its client_id isn't its own address")
	}
	if len(doc.RedirectURIs) == 0 || len(doc.RedirectURIs) > 10 {
		return "", nil, errors.New("it lists no redirect URIs, or more than 10")
	}
	for _, u := range doc.RedirectURIs {
		if !validRedirect(u) {
			return "", nil, errors.New("a redirect URI isn't https, or http to localhost")
		}
	}
	name := strings.TrimSpace(doc.ClientName)
	if name == "" {
		name = req.URL.Hostname()
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	return name, doc.RedirectURIs, nil
}

// ─── Authorization ───────────────────────────────────────────────────────────

// AuthRequest is an authorization request's parameters.
type AuthRequest struct {
	ResponseType        string `json:"response_type" form:"response_type"`
	ClientID            string `json:"client_id" form:"client_id"`
	RedirectURI         string `json:"redirect_uri" form:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge" form:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method" form:"code_challenge_method"`
	State               string `json:"state" form:"state"`
	Scope               string `json:"scope" form:"scope"`
	Resource            string `json:"resource" form:"resource"`
}

// ClientView is who asks, as the consent screen shows them.
type ClientView struct {
	Name string `json:"name"`
	// Kind: "cimd" (Verified is its document's host) or "dcr" (a name it
	// gave itself).
	Kind         string   `json:"kind"`
	Verified     string   `json:"verified"`
	RedirectHost string   `json:"redirect_host"`
	Scopes       []string `json:"scopes"`
	// Requested are the scopes the client named, if it named any.
	Requested []string `json:"requested"`
}

// CheckRequest validates an authorization request for the consent screen.
// An *OAuthError means the screen shows "this sign-in link doesn't work".
func (s *Service) CheckRequest(ctx context.Context, issuer string, r AuthRequest) (*ClientView, *Client, error) {
	if r.ClientID == "" || r.RedirectURI == "" {
		return nil, nil, oauthErr(400, "invalid_request", "the link has no client_id or redirect_uri")
	}
	c, err := s.client(ctx, r.ClientID)
	if err != nil {
		return nil, nil, err
	}
	if !redirectMatches(c.RedirectURIs, r.RedirectURI) {
		return nil, nil, oauthErr(400, "invalid_request", "the link returns somewhere the client didn't register")
	}
	if r.ResponseType != "code" {
		return nil, nil, oauthErr(400, "unsupported_response_type", "only response_type=code is supported")
	}
	if r.CodeChallenge == "" || r.CodeChallengeMethod != "S256" {
		return nil, nil, oauthErr(400, "invalid_request", "PKCE with S256 is required")
	}
	if r.Resource != "" && r.Resource != Resource(issuer) {
		return nil, nil, oauthErr(400, "invalid_target", "tokens are only for "+Resource(issuer))
	}
	u, _ := url.Parse(r.RedirectURI)
	v := &ClientView{Name: c.Name, Kind: c.Kind, RedirectHost: u.Host, Scopes: ConsentScopes, Requested: []string{}}
	if c.Kind == "cimd" {
		cu, _ := url.Parse(c.ClientID)
		v.Verified = cu.Hostname()
	}
	for _, sc := range strings.Fields(r.Scope) {
		if slices.Contains(ConsentScopes, sc) {
			v.Requested = append(v.Requested, sc)
		}
	}
	return v, c, nil
}

// Consent is the person's answer on the consent screen.
type Consent struct {
	AuthRequest
	Allow       bool     `json:"allow"`
	OrgID       string   `json:"org_id"`
	AllProjects bool     `json:"all_projects"`
	ProjectIDs  []string `json:"project_ids"`
	Scopes      []string `json:"scopes"`
}

// Authorize turns the person's answer into the address their browser goes
// back to: with a code, or with access_denied.
func (s *Service) Authorize(ctx context.Context, issuer, userID string, in Consent) (string, error) {
	_, c, err := s.CheckRequest(ctx, issuer, in.AuthRequest)
	if err != nil {
		return "", err
	}
	back, _ := url.Parse(in.RedirectURI)
	q := back.Query()
	if in.State != "" {
		q.Set("state", in.State)
	}
	q.Set("iss", issuer)
	if !in.Allow {
		q.Set("error", "access_denied")
		q.Set("error_description", "the person denied access")
		back.RawQuery = q.Encode()
		return back.String(), nil
	}
	role, err := s.roles.RoleIn(ctx, userID, in.OrgID)
	if err != nil || role == "" {
		return "", ErrForbidden
	}
	scopes := []string{ScopeRead}
	for _, sc := range in.Scopes {
		if !slices.Contains(ConsentScopes, sc) {
			return "", &ErrInvalid{Msg: "unknown scope " + sc}
		}
		if sc == ScopeRead || slices.Contains(scopes, sc) {
			continue
		}
		if role == "viewer" {
			return "", &ErrInvalid{Msg: "viewers can only let an agent read"}
		}
		scopes = append(scopes, sc)
	}
	if !in.AllProjects && len(in.ProjectIDs) == 0 {
		return "", &ErrInvalid{Msg: "choose every project or at least one"}
	}
	if in.AllProjects {
		in.ProjectIDs = nil
	}
	code := newSecret("")
	if err := s.repo.InsertCode(ctx, Code{Hash: hashToken(code), ClientID: c.ID, UserID: userID, OrgID: in.OrgID,
		AllProjects: in.AllProjects, ProjectIDs: in.ProjectIDs, Scopes: scopes, RedirectURI: in.RedirectURI,
		CodeChallenge: in.CodeChallenge, ExpiresAt: time.Now().Add(CodeTTL)}); err != nil {
		return "", err
	}
	q.Set("code", code)
	back.RawQuery = q.Encode()
	return back.String(), nil
}

// ─── Tokens (RFC 6749 §4.1.3, §6; RFC 7009) ──────────────────────────────────

// TokenRequest is the token endpoint's form.
type TokenRequest struct {
	GrantType    string `form:"grant_type"`
	Code         string `form:"code"`
	RedirectURI  string `form:"redirect_uri"`
	ClientID     string `form:"client_id"`
	CodeVerifier string `form:"code_verifier"`
	RefreshToken string `form:"refresh_token"`
	Resource     string `form:"resource"`
}

// Token answers the token endpoint.
func (s *Service) Token(ctx context.Context, issuer string, r TokenRequest) (map[string]any, error) {
	if r.Resource != "" && r.Resource != Resource(issuer) {
		return nil, oauthErr(400, "invalid_target", "tokens are only for "+Resource(issuer))
	}
	access, refresh := newSecret(AccessTokenPrefix), newSecret(RefreshTokenPrefix)
	accessExp := time.Now().Add(AccessTTL)
	var scopes []string
	switch r.GrantType {
	case "authorization_code":
		code, err := s.repo.UseCode(ctx, hashToken(r.Code))
		if errors.Is(err, ErrNotFound) {
			return nil, oauthErr(400, "invalid_grant", "the code is unknown, used or expired")
		}
		if err != nil {
			return nil, err
		}
		c, err := s.repo.GetClientByID(ctx, code.ClientID)
		if err != nil || c.ClientID != r.ClientID {
			return nil, oauthErr(400, "invalid_grant", "the code was issued to another client")
		}
		if r.RedirectURI != code.RedirectURI {
			return nil, oauthErr(400, "invalid_grant", "redirect_uri differs from the authorization request's")
		}
		sum := sha256.Sum256([]byte(r.CodeVerifier))
		if r.CodeVerifier == "" || subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(code.CodeChallenge)) != 1 {
			return nil, oauthErr(400, "invalid_grant", "the code_verifier doesn't match the code_challenge")
		}
		if role, err := s.roles.RoleIn(ctx, code.UserID, code.OrgID); err != nil || role == "" {
			return nil, oauthErr(400, "invalid_grant", "the person is no longer a member of the workspace")
		}
		id, err := s.repo.CreateConnection(ctx, NewConnection{Code: *code, Name: c.Name, ExpiresAt: time.Now().Add(ConnectionTTL),
			AccessHash: hashToken(access), AccessExp: accessExp, RefreshHash: hashToken(refresh)})
		if errors.Is(err, ErrForbidden) {
			return nil, oauthErr(400, "invalid_grant", "a chosen project is no longer in the workspace")
		}
		if err != nil {
			return nil, err
		}
		scopes = code.Scopes
		s.audit.WriteAuditLogDetail(ctx, code.OrgID, code.UserID, "agent.connected", "agent:"+id,
			map[string]any{"name": c.Name, "scopes": code.Scopes, "all_projects": code.AllProjects, "projects": len(code.ProjectIDs)})
	case "refresh_token":
		// A refresh token is a 256-bit secret bound to its connection; the
		// client_id a public client sends adds nothing to check.
		_, sc, err := s.repo.RotateRefresh(ctx, hashToken(r.RefreshToken), hashToken(access), accessExp, hashToken(refresh))
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			return nil, oauthErr(400, "invalid_grant", "the refresh token is unknown, used, expired or revoked")
		}
		if err != nil {
			return nil, err
		}
		scopes = sc
	default:
		return nil, oauthErr(400, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
	return map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(AccessTTL.Seconds()),
		"refresh_token": refresh, "scope": strings.Join(scopes, " "),
	}, nil
}

// RevokeToken revokes a token (RFC 7009): a refresh token ends its
// connection, an access token only itself. Unknown tokens are fine.
func (s *Service) RevokeToken(ctx context.Context, raw string) error {
	conn, kind, err := s.repo.TokenConnection(ctx, hashToken(raw))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if kind == "access" {
		return s.repo.DeleteToken(ctx, hashToken(raw))
	}
	c, err := s.repo.RevokeConnection(ctx, conn, "", "The agent signed out, so nothing was done.")
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	s.audit.WriteAuditLogDetail(ctx, c.OrgID, c.UserID, "agent.revoked", "agent:"+conn, map[string]any{"name": c.Name, "by_agent": true})
	return nil
}
