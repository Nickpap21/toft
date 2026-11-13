package version

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	serviceManager "github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/registry"
	"github.com/Nickpap21/toft/backend/pkg/scm"
	"go.uber.org/zap"
)

const cacheTTL = 5 * time.Minute

type cacheEntry struct {
	info    *VersionInfo
	expires time.Time
}

// ServiceLookup is satisfied by *service.ServiceManager; the version service only
// needs ownership checks and single-service lookups, not the full create/update/delete surface.
type ServiceLookup interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	Get(ctx context.Context, id string) (*serviceManager.Service, error)
}

// Service fetches and caches version information for services.
type Service struct {
	svcManager ServiceLookup
	regClient  *registry.Client
	scmClient  *scm.Client
	log        *zap.Logger

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// NewService returns a version Service.
func NewService(sm ServiceLookup, reg *registry.Client, scm *scm.Client, log *zap.Logger) *Service {
	return &Service{
		svcManager: sm,
		regClient:  reg,
		scmClient:  scm,
		log:        log,
		cache:      make(map[string]cacheEntry),
	}
}

// GetVersionInfo returns cached (up to 5 min) version info for a service.
func (s *Service) GetVersionInfo(ctx context.Context, serviceID, orgID string) (*VersionInfo, error) {
	// Ownership check.
	if owned, err := s.svcManager.CheckOwnership(ctx, serviceID, orgID); err != nil || !owned {
		return nil, ErrNotFound
	}

	// Cache hit.
	s.mu.RLock()
	entry, ok := s.cache[serviceID]
	s.mu.RUnlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.info, nil
	}

	info, err := s.fetchVersionInfo(ctx, serviceID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[serviceID] = cacheEntry{info: info, expires: time.Now().Add(cacheTTL)}
	s.mu.Unlock()

	return info, nil
}

// InvalidateCache removes the cached version info for a service.
func (s *Service) InvalidateCache(serviceID string) {
	s.mu.Lock()
	delete(s.cache, serviceID)
	s.mu.Unlock()
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (s *Service) fetchVersionInfo(ctx context.Context, serviceID string) (*VersionInfo, error) {
	svc, err := s.svcManager.Get(ctx, serviceID)
	if err != nil {
		return nil, err
	}

	info := &VersionInfo{SourceType: string(svc.SourceType)}

	switch svc.SourceType {
	case serviceManager.SourceTypeDockerImage:
		return s.fetchDockerInfo(ctx, info, svc)
	case serviceManager.SourceTypeGit:
		return s.fetchGitInfo(ctx, info, svc)
	default:
		return info, nil
	}
}

func (s *Service) fetchDockerInfo(ctx context.Context, info *VersionInfo, svc *serviceManager.Service) (*VersionInfo, error) {
	info.CurrentImage = svc.DockerImage

	var auth *registry.Auth
	if svc.RegistryPassword != "" {
		auth = &registry.Auth{
			Server:   svc.RegistryServer,
			Username: svc.RegistryUsername,
			Password: svc.RegistryPassword,
		}
	}

	tags, err := s.regClient.ListTags(ctx, svc.DockerImage, auth)
	if err != nil {
		s.log.Warn("failed to fetch docker tags", zap.String("image", svc.DockerImage), zap.Error(err))
		info.RegistryError = err.Error()
		return info, nil
	}

	// The "latest" named tag may not be among the first 25 results, so
	// fetch it explicitly for Docker Hub images and prepend it if missing.
	registryHost, _, _ := registry.ParseImageRef(svc.DockerImage)
	if registryHost == "hub.docker.com" {
		hasLatest := false
		for _, t := range tags {
			if t.Name == "latest" {
				hasLatest = true
				break
			}
		}
		if !hasLatest {
			if latestEntry, fetchErr := s.regClient.FetchDockerHubSingleTag(ctx, svc.DockerImage, "latest", auth); fetchErr == nil {
				tags = append([]registry.TagEntry{latestEntry}, tags...)
			}
		}
	}

	info.Tags = tags

	if len(tags) > 0 {
		_, _, currentTag := registry.ParseImageRef(svc.DockerImage)

		// Use the "latest" named tag as the canonical LatestTag whenever it exists.
		latestName := ""
		for _, t := range tags {
			if t.Name == "latest" {
				latestName = t.Name
				break
			}
		}
		if latestName == "" {
			// No "latest" tag available — fall back to the first result.
			latestName = tags[0].Name
		}
		info.LatestTag = latestName

		// IsOutdated: only meaningful when current is not the floating "latest" tag.
		// If current == "latest" → always up to date.
		// If latestName == "latest" and current != "latest" → user is on a pinned version behind latest.
		// If neither is "latest" → plain string equality check.
		if currentTag != "latest" {
			info.IsOutdated = currentTag != latestName
		}
	}

	return info, nil
}

func (s *Service) fetchGitInfo(ctx context.Context, info *VersionInfo, svc *serviceManager.Service) (*VersionInfo, error) {
	info.CurrentBranch = svc.GitBranch

	token := svc.GitToken
	repoURL := svc.GitRepoURL
	branch := svc.GitBranch
	if branch == "" {
		branch = "main"
	}

	var (
		wg                           sync.WaitGroup
		branches                     []scm.BranchInfo
		tags                         []scm.TagInfo
		commit                       scm.CommitInfo
		branchErr, tagErr, commitErr error
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		branches, branchErr = s.scmClient.ListBranches(ctx, repoURL, token)
	}()
	go func() {
		defer wg.Done()
		tags, tagErr = s.scmClient.ListTags(ctx, repoURL, token)
	}()
	go func() {
		defer wg.Done()
		commit, commitErr = s.scmClient.GetLatestCommit(ctx, repoURL, branch, token)
	}()
	wg.Wait()

	if branchErr == nil {
		info.Branches = branches
	} else {
		s.log.Warn("failed to fetch branches", zap.String("repo", maskToken(repoURL)), zap.Error(branchErr))
	}

	if tagErr == nil {
		info.GitTags = tags
	} else {
		s.log.Warn("failed to fetch git tags", zap.String("repo", maskToken(repoURL)), zap.Error(tagErr))
	}

	if commitErr == nil {
		info.LatestCommit = &commit
	} else {
		s.log.Warn("failed to fetch latest commit", zap.String("repo", maskToken(repoURL)), zap.Error(commitErr))
	}
	if err := cmp.Or(branchErr, tagErr, commitErr); err != nil {
		info.SCMError = scmErrorText(err, token != "")
	}

	return info, nil
}

// scmErrorText is what the panel says when the repository host refused or
// didn't answer: the reason, and what to do about it. The provider's own
// message stays in the log.
func scmErrorText(err error, hasToken bool) string {
	var apiErr *scm.APIError
	if !errors.As(err, &apiErr) {
		return "The repository host didn't answer. Try again in a moment."
	}
	host := map[string]string{"github": "GitHub", "gitlab": "GitLab"}[apiErr.Provider]
	if host == "" {
		host = "The repository host"
	}
	switch {
	case apiErr.RateLimited() && !hasToken:
		return host + "'s limit for requests without a token is used up. Add a Git token in Settings → Credentials, or try again within the hour."
	case apiErr.RateLimited():
		return host + "'s request limit for the Git token is used up. Try again within the hour."
	case apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden:
		return host + " refused the Git token. Check it in Settings → Credentials."
	case apiErr.Status == http.StatusNotFound && !hasToken:
		return host + " doesn't show this repository: it is private, or the URL is wrong. A private repository needs a Git token in Settings → Credentials."
	case apiErr.Status == http.StatusNotFound:
		return host + " doesn't show this repository to the Git token: check the URL and the token's access."
	default:
		return fmt.Sprintf("%s answered HTTP %d. Try again in a moment.", host, apiErr.Status)
	}
}

// maskToken removes any embedded token from a URL for safe logging.
func maskToken(rawURL string) string {
	if idx := strings.Index(rawURL, "@"); idx >= 0 {
		return rawURL[idx+1:]
	}
	return rawURL
}
