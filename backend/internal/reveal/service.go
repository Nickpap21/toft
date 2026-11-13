// Package reveal keeps a catalog app's admin password (or temporary login)
// for the person who created the service or reset it, sealed, until they
// dismiss it or Keep passes; then the row is deleted (D-160, the one
// exception to write-once secrets). Only that person reads it, while an
// owner or developer of the service's workspace; the transport adds a
// signed-in session and a confirmed password, so neither an API key nor an
// agent ever reads it.
package reveal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
)

// Keep is how long a copy lives without a dismissal.
const Keep = 7 * 24 * time.Hour

var (
	// ErrNotFound: the service isn't in the caller's workspace.
	ErrNotFound = errors.New("service not found")
	// ErrForbidden: a viewer; only owners and developers see a copy.
	ErrForbidden = errors.New("only owners and developers see an admin password")
	// ErrNone: the caller has no copy for the service (dismissed, expired,
	// or never theirs).
	ErrNone = errors.New("no admin password to show")
)

// Reveal is a person's copy. Key is the service's secret it was stored as,
// "" for the temporary login (basic auth). Password is set only by Open.
type Reveal struct {
	Key       string
	Reason    string // created, reset
	CreatedAt time.Time
	Password  string
}

// ExpiresAt is when the clean-up deletes it.
func (r Reveal) ExpiresAt() time.Time { return r.CreatedAt.Add(Keep) }

// Repository stores the copies, sealed. Get returns the opened copy made
// after since, or nil.
type Repository interface {
	Keep(ctx context.Context, serviceID, userID, key, value, reason string) error
	Get(ctx context.Context, serviceID, userID string, since time.Time) (*Reveal, error)
	Delete(ctx context.Context, serviceID, userID string) (bool, error)
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
	// Forget deletes every person's copy of key ("" the temporary login).
	Forget(ctx context.Context, serviceID, key string) error
}

// Logins reads a service's admin login and resets it. Satisfied by
// *service.ServiceManager.
type Logins interface {
	AdminLogin(ctx context.Context, serviceID string) (service.AdminLogin, error)
	ResetAdminPassword(ctx context.Context, userID, serviceID string) (*service.AdminPasswordReset, error)
}

// Owners tells whether a service is in a workspace. Satisfied by
// *service.ServiceManager.
type Owners interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// AuditWriter records reveals and dismissals, never the value; a reveal
// whose entry fails is refused. Satisfied by *org.Service.
type AuditWriter interface {
	RecordAuditLog(ctx context.Context, orgID, actorID, action, resource string) error
}

type Service struct {
	repo   Repository
	logins Logins
	owners Owners
	audit  AuditWriter
	log    *zap.Logger
	now    func() time.Time
}

func NewService(repo Repository, owners Owners, audit AuditWriter, log *zap.Logger) *Service {
	return &Service{repo: repo, owners: owners, audit: audit, log: log, now: time.Now}
}

// WithLogins lets the service page show a copy's login user and Reset it.
// Called from main.go.
func (s *Service) WithLogins(l Logins) { s.logins = l }

func (s *Service) check(ctx context.Context, orgID, role, serviceID string) error {
	if role != "owner" && role != "developer" {
		return ErrForbidden
	}
	return s.owned(ctx, orgID, serviceID)
}

func (s *Service) owned(ctx context.Context, orgID, serviceID string) error {
	owned, err := s.owners.CheckOwnership(ctx, serviceID, orgID)
	if err != nil {
		return fmt.Errorf("check the service's workspace: %w", err)
	}
	if !owned {
		return ErrNotFound
	}
	return nil
}

func (s *Service) get(ctx context.Context, orgID, userID, role, serviceID string) (*Reveal, error) {
	if err := s.check(ctx, orgID, role, serviceID); err != nil {
		return nil, err
	}
	r, err := s.repo.Get(ctx, serviceID, userID, s.now().Add(-Keep))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, ErrNone
	}
	return r, nil
}

// Pending is the caller's copy for a service without its value, for the
// service page to offer it; ErrNone when there is none.
func (s *Service) Pending(ctx context.Context, orgID, userID, role, serviceID string) (*Reveal, error) {
	r, err := s.get(ctx, orgID, userID, role, serviceID)
	if err != nil {
		return nil, err
	}
	r.Password = ""
	return r, nil
}

// Open is the caller's copy with its value, audited without it.
func (s *Service) Open(ctx context.Context, orgID, userID, role, serviceID string) (*Reveal, error) {
	r, err := s.get(ctx, orgID, userID, role, serviceID)
	if err != nil {
		return nil, err
	}
	if err := s.audit.RecordAuditLog(ctx, orgID, userID, "service.admin_password_revealed", "service:"+serviceID); err != nil {
		return nil, err
	}
	return r, nil
}

// Login is the service's admin login: its user and how Reset applies, for
// owners and developers; the zero value without one.
func (s *Service) Login(ctx context.Context, orgID, role, serviceID string) (service.AdminLogin, error) {
	if err := s.check(ctx, orgID, role, serviceID); err != nil {
		return service.AdminLogin{}, err
	}
	if s.logins == nil {
		return service.AdminLogin{}, nil
	}
	return s.logins.AdminLogin(ctx, serviceID)
}

// Reset gives the service's app login a new password, kept for the caller
// (D-160), audited without it. Owners and developers; the transport adds
// a signed-in session and a confirmed password.
func (s *Service) Reset(ctx context.Context, orgID, userID, role, serviceID string) (*service.AdminPasswordReset, error) {
	if err := s.check(ctx, orgID, role, serviceID); err != nil {
		return nil, err
	}
	if s.logins == nil {
		return nil, service.ErrNoReset
	}
	r, err := s.logins.ResetAdminPassword(ctx, userID, serviceID)
	// Audited once the new password is stored, also when the app's answer
	// never came; a refusal changed nothing.
	if err == nil || errors.Is(err, service.ErrResetUnconfirmed) {
		if aerr := s.audit.RecordAuditLog(context.WithoutCancel(ctx), orgID, userID, "service.admin_password_reset", "service:"+serviceID); aerr != nil {
			s.log.Warn("audit a reset", zap.String("service_id", serviceID), zap.Error(aerr)) // stored already
		}
	}
	return r, err
}

// Forget deletes every copy of key for the service: the secret it was
// stored as was replaced or deleted ("" the temporary login: basic auth
// changed), so the copy no longer opens the app.
func (s *Service) Forget(ctx context.Context, serviceID, key string) error {
	return s.repo.Forget(ctx, serviceID, key)
}

// Dismiss deletes the caller's copy, audited; ErrNone when there was none.
// Any role: it is their own, and a viewer (demoted since) may still delete it.
func (s *Service) Dismiss(ctx context.Context, orgID, userID, serviceID string) error {
	if err := s.owned(ctx, orgID, serviceID); err != nil {
		return err
	}
	deleted, err := s.repo.Delete(ctx, serviceID, userID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNone
	}
	if err := s.audit.RecordAuditLog(ctx, orgID, userID, "service.admin_password_dismissed", "service:"+serviceID); err != nil {
		s.log.Warn("audit a dismissal", zap.Error(err))
	}
	return nil
}

// RunCleaner deletes the copies older than Keep, every hour until ctx ends.
func (s *Service) RunCleaner(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if _, err := s.repo.DeleteBefore(ctx, s.now().Add(-Keep)); err != nil && ctx.Err() == nil {
			s.log.Warn("delete old admin passwords", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
