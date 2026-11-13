package webhook

import (
	"context"
	"time"
)

// Repository stores each project's webhook secret, encrypted.
type Repository interface {
	// Secret returns the project's encrypted secret, or ErrUnknownWebhook.
	Secret(ctx context.Context, projectID string) (string, error)
	// Ensure stores encrypted unless the project has a secret, and returns
	// the encrypted secret it has.
	Ensure(ctx context.Context, projectID, encrypted string) (string, error)
	// Replace stores encrypted as the project's secret.
	Replace(ctx context.Context, projectID, encrypted string) error
	// LastPush is the project's last signed branch push; zero before one.
	LastPush(ctx context.Context, projectID string) (LastPush, error)
	// MarkPushed records a signed branch push from repo (owner/name) now.
	MarkPushed(ctx context.Context, projectID, repo string) error
}

// LastPush is when GitHub last delivered a signed branch push, and from
// which repository.
type LastPush struct {
	At   *time.Time
	Repo string
}
