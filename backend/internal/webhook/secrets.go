package webhook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/google/uuid"
)

// ErrUnknownWebhook: the project has no webhook secret (or does not exist).
var ErrUnknownWebhook = errors.New("unknown webhook")

// Secrets hands out each project's webhook secret, encrypted at rest.
type Secrets struct {
	repo Repository
	keys *crypto.Keys
}

func NewSecrets(repo Repository, keys *crypto.Keys) *Secrets {
	return &Secrets{repo: repo, keys: keys}
}

// secretAD is where a project's webhook secret is sealed for (D-126).
func secretAD(projectID string) string {
	if id, err := uuid.Parse(projectID); err == nil {
		projectID = id.String()
	}
	return crypto.AD("project_webhooks.secret", projectID)
}

// Get returns the project's secret, or ErrUnknownWebhook when it has none:
// no webhook can have been set up for it.
func (s *Secrets) Get(ctx context.Context, projectID string) (string, error) {
	encrypted, err := s.repo.Secret(ctx, projectID)
	if err != nil {
		return "", err
	}
	return s.decrypt(projectID, encrypted)
}

// Ensure returns the project's secret, creating it the first time.
func (s *Secrets) Ensure(ctx context.Context, projectID string) (string, error) {
	_, encrypted, err := s.generate(projectID)
	if err != nil {
		return "", err
	}
	stored, err := s.repo.Ensure(ctx, projectID, encrypted)
	if err != nil {
		return "", err
	}
	return s.decrypt(projectID, stored)
}

// Rotate replaces the project's secret and returns the new one. Pushes
// signed with the old one are refused from now on.
func (s *Secrets) Rotate(ctx context.Context, projectID string) (string, error) {
	plain, encrypted, err := s.generate(projectID)
	if err != nil {
		return "", err
	}
	if err := s.repo.Replace(ctx, projectID, encrypted); err != nil {
		return "", err
	}
	return plain, nil
}

// LastPush is the project's last signed branch push.
func (s *Secrets) LastPush(ctx context.Context, projectID string) (LastPush, error) {
	return s.repo.LastPush(ctx, projectID)
}

// MarkPushed records a signed branch push from repo.
func (s *Secrets) MarkPushed(ctx context.Context, projectID, repo string) error {
	return s.repo.MarkPushed(ctx, projectID, repo)
}

func (s *Secrets) generate(projectID string) (plain, encrypted string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate webhook secret: %w", err)
	}
	plain = hex.EncodeToString(b)
	encrypted, err = s.keys.Seal(secretAD(projectID), plain)
	if err != nil {
		return "", "", fmt.Errorf("encrypt webhook secret: %w", err)
	}
	return plain, encrypted, nil
}

func (s *Secrets) decrypt(projectID, encrypted string) (string, error) {
	plain, err := s.keys.Open(secretAD(projectID), encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt webhook secret: %w", err)
	}
	return plain, nil
}
