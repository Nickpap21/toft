package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Auth holds credentials for a container registry.
type Auth struct {
	Server   string
	Username string
	Password string
}

// TagEntry represents a single image tag with metadata.
type TagEntry struct {
	Name     string    `json:"name"`
	PushedAt time.Time `json:"pushed_at"`
	Digest   string    `json:"digest"`
	IsLatest bool      `json:"is_latest"`
}

// Client fetches image tags from Docker Hub and OCI-compatible registries.
type Client struct {
	http *http.Client
}

// NewClient returns a registry client with a sensible timeout.
// NewClient returns a client that reaches public registries only: the
// registry host comes from the image name a member typed, and the panel
// must not probe its own network with the member's credentials.
func NewClient() *Client {
	return &Client{http: netcheck.NewPublicOnlyClient(15*time.Second, true)}
}

// ParseImageRef decomposes a Docker image reference into (registryHost, repoName, currentTag).
//
// Examples:
//
//	"postgres"                   → ("hub.docker.com", "library/postgres", "latest")
//	"postgres:16"                → ("hub.docker.com", "library/postgres", "16")
//	"grafana/grafana:10.4.2"     → ("hub.docker.com", "grafana/grafana", "10.4.2")
//	"ghcr.io/myorg/app:latest"   → ("ghcr.io", "myorg/app", "latest")
//	"myregistry.com:5000/app:v1" → ("myregistry.com:5000", "app", "v1")
func ParseImageRef(ref string) (registryHost, repoName, currentTag string) {
	tag := "latest"
	lastColon := strings.LastIndex(ref, ":")
	lastSlash := strings.LastIndex(ref, "/")

	// A colon after the last slash is a tag separator.
	if lastColon > lastSlash {
		tag = ref[lastColon+1:]
		ref = ref[:lastColon]
	}

	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 1 {
		// No slash → official Docker Hub image (library/).
		return "hub.docker.com", "library/" + parts[0], tag
	}

	if isRegistryHost(parts[0]) {
		// Custom/GHCR registry: first component is a hostname.
		return parts[0], parts[1], tag
	}

	// Two-part Docker Hub image: "namespace/image".
	return "hub.docker.com", ref, tag
}

// ListTags returns up to 25 tags for the given image reference, ordered newest first.
// auth may be nil for public images.
func (c *Client) ListTags(ctx context.Context, imageRef string, auth *Auth) ([]TagEntry, error) {
	registry, repo, currentTag := ParseImageRef(imageRef)
	if registry == "hub.docker.com" {
		return c.listDockerHubTags(ctx, repo, currentTag, auth)
	}
	return c.listOCITags(ctx, registry, repo, currentTag, auth)
}

// ─── Docker Hub ───────────────────────────────────────────────────────────────

type dockerHubTagResult struct {
	Name        string    `json:"name"`
	LastUpdated time.Time `json:"last_updated"`
	Digest      string    `json:"digest"`
}

type dockerHubTagsResponse struct {
	Results []dockerHubTagResult `json:"results"`
}

func (c *Client) listDockerHubTags(ctx context.Context, repo, currentTag string, auth *Auth) ([]TagEntry, error) {
	// Docker Hub's ordering is the reverse of its name: last_updated lists the
	// most recently pushed tags first, -last_updated the oldest (checked
	// against library/nginx, 2026-10-14: 2026 tags against 2015 ones).
	apiURL := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/?page_size=25&ordering=last_updated", repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	// Docker Hub supports JWT auth for private repos.
	if auth != nil && auth.Username != "" {
		token, tokenErr := c.dockerHubToken(ctx, auth)
		if tokenErr == nil {
			req.Header.Set("Authorization", "JWT "+token)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker hub request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("docker hub API %d: %s", resp.StatusCode, body)
	}

	var parsed dockerHubTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode docker hub response: %w", err)
	}

	tags := make([]TagEntry, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		tags = append(tags, TagEntry{
			Name:     r.Name,
			PushedAt: r.LastUpdated,
			Digest:   r.Digest,
			IsLatest: r.Name == "latest",
		})
	}

	_ = currentTag // available for callers to compare against
	return tags, nil
}

// dockerHubSingleTagResponse is the response shape for the single-tag Docker Hub endpoint.
type dockerHubSingleTagResponse struct {
	Name        string    `json:"name"`
	LastUpdated time.Time `json:"last_updated"`
	Digest      string    `json:"digest"`
}

// FetchDockerHubSingleTag fetches metadata for a specific named tag (e.g. "latest") directly.
// "latest" may not be among the first page of tags (an image that stopped
// publishing it, or a busy repository). Returns an error when the tag does not exist.
func (c *Client) FetchDockerHubSingleTag(ctx context.Context, imageRef, tagName string, auth *Auth) (TagEntry, error) {
	_, repo, _ := ParseImageRef(imageRef)

	apiURL := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/%s/", repo, url.PathEscape(tagName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return TagEntry{}, err
	}

	if auth != nil && auth.Username != "" {
		token, tokenErr := c.dockerHubToken(ctx, auth)
		if tokenErr == nil {
			req.Header.Set("Authorization", "JWT "+token)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return TagEntry{}, fmt.Errorf("docker hub single-tag request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return TagEntry{}, fmt.Errorf("tag %q not found for %s", tagName, imageRef)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return TagEntry{}, fmt.Errorf("docker hub API %d: %s", resp.StatusCode, body)
	}

	var parsed dockerHubSingleTagResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return TagEntry{}, fmt.Errorf("decode docker hub tag response: %w", err)
	}

	return TagEntry{
		Name:     parsed.Name,
		PushedAt: parsed.LastUpdated,
		Digest:   parsed.Digest,
		IsLatest: tagName == "latest",
	}, nil
}

// dockerHubToken exchanges username/password for a Docker Hub JWT.
func (c *Client) dockerHubToken(ctx context.Context, auth *Auth) (string, error) {
	// json.Marshal, not %q: %q writes control characters as \x.., which is not JSON.
	body, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{auth.Username, auth.Password})
	if err != nil {
		return "", fmt.Errorf("encode docker hub login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://hub.docker.com/v2/users/login/", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Token, nil
}

// ─── OCI Distribution Spec ───────────────────────────────────────────────────

type ociTagsResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

func (c *Client) listOCITags(ctx context.Context, registry, repo, currentTag string, auth *Auth) ([]TagEntry, error) {
	// Step 1: get bearer token if the registry requires auth.
	token, err := c.ociAuthToken(ctx, registry, repo, auth)
	if err != nil {
		return nil, fmt.Errorf("oci auth: %w", err)
	}

	apiURL := fmt.Sprintf("https://%s/v2/%s/tags/list", registry, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oci tags request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("oci registry %d: %s", resp.StatusCode, body)
	}

	var parsed ociTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode oci response: %w", err)
	}

	// OCI tags/list does not return push dates; order is not guaranteed.
	tags := make([]TagEntry, 0, len(parsed.Tags))
	for i, name := range parsed.Tags {
		if isArtifactTag(name) {
			continue
		}
		tags = append(tags, TagEntry{
			Name:     name,
			IsLatest: i == 0 || name == "latest",
		})
	}

	// GHCR: enrich with creation dates from the GitHub Packages API using the stored PAT.
	if registry == "ghcr.io" && auth != nil && auth.Password != "" {
		dates := c.fetchGHCRDates(ctx, repo, auth.Password)
		for i := range tags {
			if t, ok := dates[tags[i].Name]; ok {
				tags[i].PushedAt = t
			}
		}
	}

	return tags, nil
}

// ghcrVersion is a single version entry from the GitHub Packages API.
type ghcrVersion struct {
	CreatedAt time.Time `json:"created_at"`
	Metadata  struct {
		Container struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

// fetchGHCRDates calls the GitHub Packages API to get a tag→date map.
// It tries the org endpoint first (most common for team repos), then the user endpoint.
func (c *Client) fetchGHCRDates(ctx context.Context, repo, token string) map[string]time.Time {
	// repo is "owner/image" or "owner/sub/image"; split on first slash.
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	owner, packageName := parts[0], parts[1]

	for _, apiURL := range []string{
		fmt.Sprintf("https://api.github.com/orgs/%s/packages/container/%s/versions?per_page=100", owner, url.PathEscape(packageName)),
		fmt.Sprintf("https://api.github.com/user/packages/container/%s/versions?per_page=100", url.PathEscape(packageName)),
	} {
		dates, err := c.ghcrVersionDates(ctx, apiURL, token)
		if err == nil && len(dates) > 0 {
			return dates
		}
	}
	return nil
}

func (c *Client) ghcrVersionDates(ctx context.Context, apiURL, token string) (map[string]time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github packages API %d", resp.StatusCode)
	}

	var versions []ghcrVersion
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return nil, err
	}

	dates := make(map[string]time.Time, len(versions))
	for _, v := range versions {
		for _, tag := range v.Metadata.Container.Tags {
			dates[tag] = v.CreatedAt
		}
	}
	return dates, nil
}

// isArtifactTag returns true for cosign supply-chain artifacts that appear as tags
// (e.g. sha256-abc123.sig, sha256-abc123.att, sha256-abc123.sbom).
// These are not deployable images and should be hidden from the version picker.
func isArtifactTag(name string) bool {
	return strings.HasSuffix(name, ".sig") ||
		strings.HasSuffix(name, ".att") ||
		strings.HasSuffix(name, ".sbom")
}

// ociAuthToken performs the OCI Bearer token exchange.
// Returns "" if the registry does not require auth.
func (c *Client) ociAuthToken(ctx context.Context, registry, repo string, auth *Auth) (string, error) {
	// Probe /v2/ to get the Www-Authenticate challenge.
	probeURL := fmt.Sprintf("https://%s/v2/", registry)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return "", nil // no auth required
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("unexpected probe status %d", resp.StatusCode)
	}

	// Parse Www-Authenticate header: Bearer realm="...",service="...",scope="..."
	challenge := resp.Header.Get("Www-Authenticate")
	realm, service, scope := parseBearer(challenge)
	if realm == "" {
		// Fall back to a sensible scope if header is missing.
		scope = fmt.Sprintf("repository:%s:pull", repo)
	}

	tokenURL, _ := url.Parse(realm)
	q := tokenURL.Query()
	if service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	} else {
		q.Set("scope", fmt.Sprintf("repository:%s:pull", repo))
	}
	tokenURL.RawQuery = q.Encode()

	tokenReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if auth != nil && auth.Password != "" {
		// GHCR (and many OCI registries) accept any non-empty username with a PAT.
		// Fall back to "oauth2" when the caller only supplied a token without a username.
		username := auth.Username
		if username == "" {
			username = "oauth2"
		}
		tokenReq.SetBasicAuth(username, auth.Password)
	}

	tokenResp, err := c.http.Do(tokenReq)
	if err != nil {
		return "", err
	}
	defer tokenResp.Body.Close()

	var result struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"` // GitHub uses this field
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Token != "" {
		return result.Token, nil
	}
	return result.AccessToken, nil
}

// parseBearer extracts realm, service, scope from a Bearer challenge string.
func parseBearer(header string) (realm, service, scope string) {
	header = strings.TrimPrefix(header, "Bearer ")
	for _, kv := range strings.Split(header, ",") {
		kv = strings.TrimSpace(kv)
		if strings.HasPrefix(kv, "realm=") {
			realm = strings.Trim(strings.TrimPrefix(kv, "realm="), `"`)
		} else if strings.HasPrefix(kv, "service=") {
			service = strings.Trim(strings.TrimPrefix(kv, "service="), `"`)
		} else if strings.HasPrefix(kv, "scope=") {
			scope = strings.Trim(strings.TrimPrefix(kv, "scope="), `"`)
		}
	}
	return
}

// isRegistryHost returns true if s looks like a hostname (contains "." or ":") or is "localhost".
func isRegistryHost(s string) bool {
	return s == "localhost" || strings.Contains(s, ".") || strings.Contains(s, ":")
}
