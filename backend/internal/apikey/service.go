package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"go.uber.org/zap"
)

// Service contains the business logic for managing API keys.
type Service struct {
	repo Repository
	log  *zap.Logger
	// lastUsedWritten is key ID → when last_used_at was last written, so a
	// client polling with a key costs one write a minute, not one a request.
	lastUsedWritten sync.Map
	now             func() time.Time
}

// lastUsedEvery is how often a key's last_used_at is written at most.
const lastUsedEvery = time.Minute

func NewService(repo Repository, log *zap.Logger) *Service {
	return &Service{repo: repo, log: log, now: time.Now}
}

// Create generates a new API key for the given user.
// The full raw key is returned only here and never again.
func (s *Service) Create(ctx context.Context, userID string, input CreateInput) (*CreatedResponse, error) {
	rawKey, keyHash, prefix, err := generateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	if err := validateScope(input.ProjectID, input.Scopes); err != nil {
		return nil, err
	}
	services, err := s.keyServices(ctx, input)
	if err != nil {
		return nil, err
	}
	now := s.now()
	expires := input.ExpiresAt.Time
	switch {
	case !input.ExpiresAt.Set:
		t := now.Add(DefaultExpiry)
		expires = &t
	case expires != nil && !expires.After(now.Add(time.Minute)): // a minute of clock skew
		return nil, ErrInvalidExpiry
	}
	key, err := s.repo.Create(ctx, NewKey{
		UserID: userID, Name: input.Name, KeyHash: keyHash, Prefix: prefix,
		ProjectID: input.ProjectID, Scopes: input.Scopes, ServiceIDs: input.ServiceIDs, ExpiresAt: expires,
	})
	if err != nil {
		return nil, fmt.Errorf("store api key: %w", err)
	}
	key.Services = services
	return &CreatedResponse{APIKey: *key, Key: rawKey}, nil
}

// keyServices checks a key's service_ids: none, or each once a service of
// its project, with only the deploy and variables scopes. It returns them
// named.
func (s *Service) keyServices(ctx context.Context, input CreateInput) ([]KeyService, error) {
	if len(input.ServiceIDs) == 0 {
		return []KeyService{}, nil
	}
	if input.ProjectID == "" {
		return nil, ErrInvalidServices
	}
	for _, sc := range input.Scopes {
		if sc != ScopeDeploy && sc != ScopeVariables {
			return nil, ErrInvalidServices
		}
	}
	seen := map[string]bool{}
	for _, id := range input.ServiceIDs {
		if seen[id] {
			return nil, ErrInvalidServices
		}
		seen[id] = true
	}
	found, err := s.repo.ListServices(ctx, input.ServiceIDs)
	if err != nil {
		return nil, fmt.Errorf("read the key's services: %w", err)
	}
	if len(found) != len(input.ServiceIDs) {
		return nil, ErrInvalidServices
	}
	for _, svc := range found {
		if svc.ProjectID != input.ProjectID {
			return nil, ErrInvalidServices
		}
	}
	return found, nil
}

// List returns all API keys for the user (no secret values).
func (s *Service) List(ctx context.Context, userID string) ([]APIKey, error) {
	return s.repo.ListByUser(ctx, userID)
}

// ListForProject returns up to limit of the keys scoped to projectID, newest
// first and older than (beforeAt, beforeID) when beforeID is set: an owner
// sees every member's, anyone else their own. more says a page follows.
func (s *Service) ListForProject(ctx context.Context, projectID, callerID, role string, beforeAt time.Time, beforeID string, limit int) (keys []ProjectKey, more bool, err error) {
	owner := callerID
	if role == "owner" {
		owner = ""
	}
	keys, err = s.repo.ListByProject(ctx, projectID, owner, beforeAt, beforeID, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(keys) > limit {
		return keys[:limit], true, nil
	}
	return keys, false, nil
}

// Delete revokes an API key owned by the user.
func (s *Service) Delete(ctx context.Context, id, userID string) (*RevokedKey, error) {
	return s.repo.Delete(ctx, id, userID)
}

// validateScope accepts a full-access key (no project, no scopes) or a
// project-scoped one (a project and one or more known scopes, no repeats).
func validateScope(projectID string, scopes []string) error {
	if projectID == "" && len(scopes) == 0 {
		return nil
	}
	if projectID == "" || len(scopes) == 0 {
		return ErrInvalidScope
	}
	seen := map[string]bool{}
	for _, sc := range scopes {
		if !knownScopes[sc] || seen[sc] {
			return ErrInvalidScope
		}
		seen[sc] = true
	}
	return nil
}

// ValidateAPIKey checks a raw key, returns who it acts for, and updates
// last_used_at. Satisfies middleware.APIKeyValidator.
func (s *Service) ValidateAPIKey(ctx context.Context, rawKey string) (middleware.APIKeyIdentity, error) {
	hash := hashKey(rawKey)
	key, err := s.repo.GetForAuth(ctx, hash)
	if err != nil {
		return middleware.APIKeyIdentity{}, fmt.Errorf("invalid api key")
	}
	if key.ProjectID != "" && key.ProjectOrgID == "" {
		// A project from before workspaces existed: there is no workspace
		// to scope the key to.
		return middleware.APIKeyIdentity{}, fmt.Errorf("api key's project has no workspace")
	}
	if key.ExpiresAt != nil && !s.now().Before(*key.ExpiresAt) {
		return middleware.APIKeyIdentity{}, &middleware.APIKeyExpiredError{At: *key.ExpiresAt}
	}
	// Update last_used_at asynchronously — don't block the request — and at
	// most once a minute per key.
	if s.shouldWriteLastUsed(key.ID, time.Now()) {
		go func() {
			if err := s.repo.UpdateLastUsed(context.Background(), key.ID); err != nil {
				s.log.Warn("failed to update api key last_used_at", zap.Error(err), zap.String("key_id", key.ID))
			}
		}()
	}
	return middleware.APIKeyIdentity{ID: key.ID, UserID: key.UserID, ProjectID: key.ProjectID, OrgID: key.ProjectOrgID, Scopes: key.Scopes,
		ServiceIDs: key.ServiceIDs}, nil
}

// ─── Key generation helpers ───────────────────────────────────────────────────

// generateKey creates a new random API key.
// Format: "paas_" + 32 URL-safe base64 chars (from 24 random bytes).
// Returns: (rawKey, sha256Hash, displayPrefix, error).
func generateKey() (rawKey, keyHash, prefix string, err error) {
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		return
	}
	rawKey = "paas_" + base64.RawURLEncoding.EncodeToString(b)
	keyHash = hashKey(rawKey)
	// Prefix = first 13 chars ("paas_" + 8 random chars) — safe to display
	prefix = rawKey[:13]
	return
}

func hashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return fmt.Sprintf("%x", sum)
}

// shouldWriteLastUsed reports whether keyID's last_used_at is due a write
// at now, and records now as the last write when it is.
func (s *Service) shouldWriteLastUsed(keyID string, now time.Time) bool {
	if prev, ok := s.lastUsedWritten.Load(keyID); ok && now.Sub(prev.(time.Time)) < lastUsedEvery {
		return false
	}
	s.lastUsedWritten.Store(keyID, now)
	return true
}
