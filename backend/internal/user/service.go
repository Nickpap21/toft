package user

import (
	"context"
	"errors"
	"fmt"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

var ErrWrongPassword = errors.New("current password is incorrect")

// OperatorChecker reports whether a user is an instance operator.
// Satisfied by instance.Service.
type OperatorChecker interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
}

type Service struct {
	repo      Repository
	operators OperatorChecker
	log       *zap.Logger
}

func NewService(repo Repository, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{repo: repo, log: log}
}

// WithOperatorChecker lets profiles report is_operator. Without one, every
// profile reports false.
func (s *Service) WithOperatorChecker(c OperatorChecker) *Service {
	s.operators = c
	return s
}

func (s *Service) GetByID(ctx context.Context, id string) (*User, error) {
	return s.repo.GetByID(ctx, id)
}

// GetMe returns the profile of the authenticated user.
func (s *Service) GetMe(ctx context.Context, id string) (*Profile, error) {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	p := NewProfile(u, s.isOperator(ctx, id))
	return &p, nil
}

// IsActive reports whether the user may authenticate. An unknown user is
// inactive, not an error. Satisfies middleware.UserStatusChecker.
// RequestIdentity satisfies middleware.UserStatusChecker. An unknown user is
// an inactive one.
func (s *Service) RequestIdentity(ctx context.Context, id, orgID, sessionHash string) (middleware.Identity, error) {
	ident, err := s.repo.RequestIdentity(ctx, id, orgID, sessionHash)
	if errors.Is(err, ErrNotFound) {
		return middleware.Identity{}, nil
	}
	if err != nil {
		return middleware.Identity{}, err
	}
	return middleware.Identity{Active: ident.Active, TokensValidFrom: ident.TokensValidFrom, OrgID: ident.OrgID, Role: ident.Role, SessionRevoked: ident.SessionRevoked}, nil
}

func (s *Service) IsActive(ctx context.Context, id string) (bool, error) {
	active, err := s.repo.IsActive(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

// isOperator fails closed: a missing checker or a failed check reports false
// and is logged, so /me never fails because of it.
func (s *Service) isOperator(ctx context.Context, id string) bool {
	if s.operators == nil {
		return false
	}
	ok, err := s.operators.IsOperator(ctx, id)
	if err != nil {
		s.log.Warn("operator check failed; reporting is_operator=false", zap.Error(err), zap.String("user_id", id))
		return false
	}
	return ok
}

func (s *Service) UpdateProfile(ctx context.Context, id, name string) (*Profile, error) {
	if name == "" {
		return nil, fmt.Errorf("name cannot be empty")
	}
	u, err := s.repo.UpdateProfile(ctx, id, name)
	if err != nil {
		return nil, err
	}
	p := NewProfile(u, s.isOperator(ctx, id))
	return &p, nil
}

// ErrUnknownTheme: not one of Themes.
var ErrUnknownTheme = errors.New("unknown theme")

// SetTheme saves the user's Settings → Appearance choice.
func (s *Service) SetTheme(ctx context.Context, id, theme string) error {
	if !Themes[theme] {
		return ErrUnknownTheme
	}
	return s.repo.UpdateTheme(ctx, id, theme)
}

// SignOutOthers ends every session of the account; the caller answers with
// a fresh token for the one that asked.
func (s *Service) SignOutOthers(ctx context.Context, id string) error {
	return s.repo.RevokeSessions(ctx, id)
}

// SignOut ends one session: the token with the sha256 tokenHash is refused
// until it expires.
func (s *Service) SignOut(ctx context.Context, tokenHash string, expires time.Time) error {
	return s.repo.RevokeSession(ctx, tokenHash, expires)
}

// VerifyPassword reports whether password is the account's own. Operator
// changes to accounts and operators ask for it again (D-23, D-50).
func (s *Service) VerifyPassword(ctx context.Context, id, password string) (bool, error) {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("get user: %w", err)
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil, nil
}

func (s *Service) UpdatePassword(ctx context.Context, id, currentPassword, newPassword string) error {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(currentPassword)); err != nil {
		return ErrWrongPassword
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.repo.UpdatePassword(ctx, id, string(hashed))
}
