package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CommitInfo holds metadata for a single git commit.
type CommitInfo struct {
	SHA       string    `json:"sha"`
	Message   string    `json:"message"`
	Author    string    `json:"author"`
	Timestamp time.Time `json:"timestamp"`
}

// BranchInfo describes a git branch and its tip commit.
type BranchInfo struct {
	Name         string     `json:"name"`
	LatestCommit CommitInfo `json:"latest_commit"`
}

// TagInfo describes a git tag.
type TagInfo struct {
	Name string     `json:"name"`
	SHA  string     `json:"sha"`
	Date *time.Time `json:"date,omitempty"`
}

// Client fetches branch/tag/commit data from GitHub and GitLab.
type Client struct {
	http *http.Client
}

// NewClient returns an SCM client.
// NewClient returns a client that reaches public hosts only: a GitLab
// host comes from the repository URL a member typed.
func NewClient() *Client {
	return &Client{http: netcheck.NewPublicOnlyClient(15*time.Second, true)}
}

// ListBranches returns the list of branches for the given repository URL.
// token is optional; provide for private repos.
func (c *Client) ListBranches(ctx context.Context, repoURL, token string) ([]BranchInfo, error) {
	owner, repo, host := parseRepoURL(repoURL)
	if owner == "" {
		return nil, fmt.Errorf("could not parse repository URL: %s", repoURL)
	}

	switch detectProvider(host) {
	case "gitlab":
		return c.gitlabBranches(ctx, host, owner, repo, token)
	default:
		return c.githubBranches(ctx, owner, repo, token)
	}
}

// ListTags returns the list of tags for the given repository URL.
func (c *Client) ListTags(ctx context.Context, repoURL, token string) ([]TagInfo, error) {
	owner, repo, host := parseRepoURL(repoURL)
	if owner == "" {
		return nil, fmt.Errorf("could not parse repository URL: %s", repoURL)
	}

	switch detectProvider(host) {
	case "gitlab":
		return c.gitlabTags(ctx, host, owner, repo, token)
	default:
		return c.githubTags(ctx, owner, repo, token)
	}
}

// GetLatestCommit returns the most recent commit on the given branch.
func (c *Client) GetLatestCommit(ctx context.Context, repoURL, branch, token string) (CommitInfo, error) {
	owner, repo, host := parseRepoURL(repoURL)
	if owner == "" {
		return CommitInfo{}, fmt.Errorf("could not parse repository URL: %s", repoURL)
	}

	switch detectProvider(host) {
	case "gitlab":
		return c.gitlabLatestCommit(ctx, host, owner, repo, branch, token)
	default:
		return c.githubLatestCommit(ctx, owner, repo, branch, token)
	}
}

// ─── GitHub ───────────────────────────────────────────────────────────────────

type githubBranchItem struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
		URL string `json:"url"`
	} `json:"commit"`
}

type githubTagItem struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type githubCommitDetail struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

func (c *Client) githubBranches(ctx context.Context, owner, repo, token string) ([]BranchInfo, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=50", owner, repo)
	var items []githubBranchItem
	if err := c.githubGet(ctx, apiURL, token, &items); err != nil {
		return nil, err
	}

	branches := make([]BranchInfo, 0, len(items))
	for _, item := range items {
		branches = append(branches, BranchInfo{
			Name: item.Name,
			LatestCommit: CommitInfo{
				SHA: item.Commit.SHA,
			},
		})
	}
	return branches, nil
}

func (c *Client) githubTags(ctx context.Context, owner, repo, token string) ([]TagInfo, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/tags?per_page=50", owner, repo)
	var items []githubTagItem
	if err := c.githubGet(ctx, apiURL, token, &items); err != nil {
		return nil, err
	}

	tags := make([]TagInfo, len(items))
	for i, item := range items {
		tags[i] = TagInfo{Name: item.Name, SHA: item.Commit.SHA}
	}

	// Fetch commit dates concurrently (max 5 in-flight) to avoid hammering the API.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i, item := range items {
		wg.Add(1)
		go func(idx int, sha string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			detail := &githubCommitDetail{}
			commitURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, sha)
			if err := c.githubGet(ctx, commitURL, token, detail); err == nil && !detail.Commit.Author.Date.IsZero() {
				t := detail.Commit.Author.Date
				tags[idx].Date = &t
			}
		}(i, item.Commit.SHA)
	}
	wg.Wait()

	return tags, nil
}

func (c *Client) githubLatestCommit(ctx context.Context, owner, repo, branch, token string) (CommitInfo, error) {
	ref := url.PathEscape(branch)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, ref)
	var detail githubCommitDetail
	if err := c.githubGet(ctx, apiURL, token, &detail); err != nil {
		return CommitInfo{}, err
	}
	return CommitInfo{
		SHA:       detail.SHA,
		Message:   firstLine(detail.Commit.Message),
		Author:    detail.Commit.Author.Name,
		Timestamp: detail.Commit.Author.Date,
	}, nil
}

func (c *Client) githubGet(ctx context.Context, apiURL, token string, dest any) error {
	return c.apiGet(ctx, "github", apiURL, dest, func(req *http.Request) {
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
	})
}

// ─── GitLab ───────────────────────────────────────────────────────────────────

type gitlabBranchItem struct {
	Name   string `json:"name"`
	Commit struct {
		ID            string    `json:"id"`
		Message       string    `json:"message"`
		AuthorName    string    `json:"author_name"`
		CommittedDate time.Time `json:"committed_date"`
	} `json:"commit"`
}

type gitlabTagItem struct {
	Name   string `json:"name"`
	Commit struct {
		ID            string    `json:"id"`
		CommittedDate time.Time `json:"committed_date"`
	} `json:"commit"`
}

// gitlabFlatCommit matches the flat object returned by /repository/commits/:ref
// (no wrapping .commit field, unlike branch items).
type gitlabFlatCommit struct {
	ID            string    `json:"id"`
	Message       string    `json:"message"`
	AuthorName    string    `json:"author_name"`
	CommittedDate time.Time `json:"committed_date"`
}

func (c *Client) gitlabBranches(ctx context.Context, host, owner, repo, token string) ([]BranchInfo, error) {
	projectPath := url.PathEscape(owner + "/" + repo)
	apiURL := fmt.Sprintf("https://%s/api/v4/projects/%s/repository/branches?per_page=50", host, projectPath)
	var items []gitlabBranchItem
	if err := c.gitlabGet(ctx, apiURL, token, &items); err != nil {
		return nil, err
	}

	branches := make([]BranchInfo, 0, len(items))
	for _, item := range items {
		branches = append(branches, BranchInfo{
			Name: item.Name,
			LatestCommit: CommitInfo{
				SHA:       item.Commit.ID,
				Message:   firstLine(item.Commit.Message),
				Author:    item.Commit.AuthorName,
				Timestamp: item.Commit.CommittedDate,
			},
		})
	}
	return branches, nil
}

func (c *Client) gitlabTags(ctx context.Context, host, owner, repo, token string) ([]TagInfo, error) {
	projectPath := url.PathEscape(owner + "/" + repo)
	apiURL := fmt.Sprintf("https://%s/api/v4/projects/%s/repository/tags?per_page=50", host, projectPath)
	var items []gitlabTagItem
	if err := c.gitlabGet(ctx, apiURL, token, &items); err != nil {
		return nil, err
	}

	tags := make([]TagInfo, 0, len(items))
	for _, item := range items {
		t := item.Commit.CommittedDate
		var date *time.Time
		if !t.IsZero() {
			date = &t
		}
		tags = append(tags, TagInfo{
			Name: item.Name,
			SHA:  item.Commit.ID,
			Date: date,
		})
	}
	return tags, nil
}

func (c *Client) gitlabLatestCommit(ctx context.Context, host, owner, repo, branch, token string) (CommitInfo, error) {
	projectPath := url.PathEscape(owner + "/" + repo)
	ref := url.PathEscape(branch)
	apiURL := fmt.Sprintf("https://%s/api/v4/projects/%s/repository/commits/%s", host, projectPath, ref)

	var item gitlabFlatCommit
	if err := c.gitlabGet(ctx, apiURL, token, &item); err != nil {
		return CommitInfo{}, err
	}
	return CommitInfo{
		SHA:       item.ID,
		Message:   firstLine(item.Message),
		Author:    item.AuthorName,
		Timestamp: item.CommittedDate,
	}, nil
}

func (c *Client) gitlabGet(ctx context.Context, apiURL, token string, dest any) error {
	return c.apiGet(ctx, "gitlab", apiURL, dest, func(req *http.Request) {
		if token != "" {
			req.Header.Set("PRIVATE-TOKEN", token)
		}
	})
}

// APIError is a provider's answer other than 200: callers tell a rate limit
// from a missing repository by its status and body.
type APIError struct {
	Provider string // "github" | "gitlab"
	Status   int
	Body     string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API %d: %s", e.Provider, e.Status, e.Body)
}

// RateLimited reports whether the provider refused for its request limit.
func (e *APIError) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests ||
		(e.Status == http.StatusForbidden && strings.Contains(strings.ToLower(e.Body), "rate limit"))
}

// apiGet performs a GET against a provider's REST API and decodes the JSON body into dest.
// setAuth sets provider-specific headers (Accept/Authorization for GitHub, PRIVATE-TOKEN for GitLab).
func (c *Client) apiGet(ctx context.Context, provider, apiURL string, dest any, setAuth func(*http.Request)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}
	setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s request: %w", provider, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &APIError{Provider: provider, Status: resp.StatusCode, Body: string(body)}
	}
	return json.NewDecoder(resp.Body).Decode(dest)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// parseRepoURL extracts (owner, repo, host) from a GitHub/GitLab URL.
// e.g., "https://github.com/myorg/myrepo.git" → ("myorg", "myrepo", "github.com")
func parseRepoURL(rawURL string) (owner, repo, host string) {
	rawURL = strings.TrimSuffix(rawURL, ".git")

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", ""
	}

	host = u.Hostname()
	path := strings.Trim(u.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	return parts[0], parts[1], host
}

// detectProvider maps a hostname to "github", "gitlab", or "github" (default).
func detectProvider(host string) string {
	if strings.Contains(host, "gitlab") {
		return "gitlab"
	}
	return "github"
}

// firstLine returns only the first line of a multi-line string.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
