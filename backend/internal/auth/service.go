package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserExists         = errors.New("user already exists with this email")
	// ErrRegistrationClosed: no invite token, and the instance already has
	// users. Returned before any password hashing or email lookup, so it
	// reveals nothing about which accounts exist.
	ErrRegistrationClosed = errors.New("registration is invite-only")
	// ErrSetupTokenInvalid: first-run registration without the configured
	// TOFT_SETUP_TOKEN.
	ErrSetupTokenInvalid = errors.New("a valid setup token is required to set up this instance")
	// ErrSetupTokenExpired: the installer's setup code is past its 30
	// minutes (D-61); running the installer again prints a new one.
	ErrSetupTokenExpired = errors.New("the setup code has expired")
	// ErrAccountDisabled: correct credentials for a deactivated account.
	ErrAccountDisabled = errors.New("account is deactivated")
	// ErrWrongPassword: a signed-in user's password confirmation failed.
	ErrWrongPassword = errors.New("your password is incorrect")
)

// InstanceGate is the slice of instance.Service that auth needs.
type InstanceGate interface {
	RegistrationMode(ctx context.Context) string
	IsOperator(ctx context.Context, userID string) (bool, error)
	// VerifySetupToken is true when provided matches TOFT_SETUP_TOKEN, or
	// when no setup token is configured.
	VerifySetupToken(provided string) bool
	// SetupTokenExpired is true when the configured setup token's time is
	// up (TOFT_SETUP_TOKEN_EXPIRES, D-61).
	SetupTokenExpired() bool
}

// Service contains the business logic for authentication.
type Service struct {
	userRepo  user.Repository
	repo      Repository
	gate      InstanceGate
	log       *zap.Logger
	jwtSecret []byte
}

// NewService creates an auth Service. A nil gate means registration is
// invite-only, no setup token is required, and nobody is reported as an
// operator.
func NewService(userRepo user.Repository, repo Repository, gate InstanceGate, log *zap.Logger, jwtSecret string) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		userRepo:  userRepo,
		repo:      repo,
		gate:      gate,
		log:       log,
		jwtSecret: []byte(jwtSecret),
	}
}

// RegisterParams is the input to Register.
type RegisterParams struct {
	Email    string
	Name     string
	Password string
	// OrgName names the first workspace. Honoured only for the first account
	// on an empty instance; invited users join the invite's workspace.
	OrgName     string
	InviteToken string
	// SetupToken must match TOFT_SETUP_TOKEN, when set, for the first account.
	SetupToken string
}

// RegisterResult is returned by Register.
type RegisterResult struct {
	User       *user.User
	Token      string
	IsOperator bool
	// Reauth is the sign-in's password confirmation, until ReauthExpires.
	Reauth        string
	ReauthExpires time.Time
}

// Register creates an account. Registration is invite-only (D-21):
//
//   - With an invite token, the user, their membership and the consumed
//     invite are created in one transaction. Any failure (bad, expired or used
//     invite; duplicate email) creates nothing and consumes nothing.
//   - Without one, it succeeds only on an empty instance: the first account
//     becomes the instance operator and owner of a new workspace, in one
//     transaction. Otherwise ErrRegistrationClosed, before any hashing.
func (s *Service) Register(ctx context.Context, p RegisterParams) (*RegisterResult, error) {
	var (
		userID     string
		isOperator bool
	)

	if p.InviteToken == "" {
		exists, err := s.repo.UsersExist(ctx)
		if err != nil {
			return nil, fmt.Errorf("check users exist: %w", err)
		}
		if exists {
			return nil, s.refuseOpenRegistration(ctx)
		}
		if s.gate != nil && s.gate.SetupTokenExpired() {
			return nil, ErrSetupTokenExpired
		}
		if s.gate != nil && !s.gate.VerifySetupToken(p.SetupToken) {
			return nil, ErrSetupTokenInvalid
		}

		hashed, err := hashPassword(p.Password)
		if err != nil {
			return nil, err
		}
		orgName := strings.TrimSpace(p.OrgName)
		if orgName == "" {
			orgName = p.Name + "'s workspace"
			// A name near its 100 characters doesn't fit the workspace's.
			if utf8.RuneCountInString(orgName) > 100 {
				orgName = org.DefaultWorkspaceName
			}
		}
		userID, err = s.repo.CreateFirstUser(ctx, NewUser{Email: p.Email, Name: p.Name, PasswordHash: hashed}, orgName)
		if errors.Is(err, ErrNotFirstUser) {
			// Someone else completed first-run registration while we hashed.
			return nil, ErrRegistrationClosed
		}
		if err != nil {
			return nil, fmt.Errorf("create first user: %w", err)
		}
		isOperator = true
		s.log.Info("first account created; it is the instance operator", zap.String("user_id", userID))
	} else {
		hashed, err := hashPassword(p.Password)
		if err != nil {
			return nil, err
		}
		userID, err = s.repo.CreateUserWithInvite(ctx, NewUser{Email: p.Email, Name: p.Name, PasswordHash: hashed}, p.InviteToken)
		if err != nil {
			return nil, fmt.Errorf("create user with invite: %w", err)
		}
	}

	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load new user: %w", err)
	}
	token, proof, expires, err := s.signIn(u.ID)
	if err != nil {
		return nil, err
	}
	return &RegisterResult{User: u, Token: token, IsOperator: isOperator, Reauth: proof, ReauthExpires: expires}, nil
}

// refuseOpenRegistration decides a no-invite registration on an instance that
// already has users. Every mode refuses today; D-21's email_domain and
// approval modes will branch here.
func (s *Service) refuseOpenRegistration(ctx context.Context) error {
	mode := instance.ModeInviteOnly
	if s.gate != nil {
		mode = s.gate.RegistrationMode(ctx)
	}
	switch mode {
	case instance.ModeInviteOnly:
		return ErrRegistrationClosed
	default:
		s.log.Warn("registration mode not implemented; refusing", zap.String("mode", mode))
		return ErrRegistrationClosed
	}
}

func hashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hashed), nil
}

// LoginResult is returned by Login.
type LoginResult struct {
	User       *user.User
	Token      string
	IsOperator bool
	// Reauth is the sign-in's password confirmation, until ReauthExpires.
	Reauth        string
	ReauthExpires time.Time
}

// unknownUserHash is compared against the password when the email is not
// an account, so that answer costs the same bcrypt work as a wrong password
// and the timing does not say which emails exist.
var unknownUserHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("not an account"), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("bcrypt: %v", err))
	}
	return h
}()

// Login checks credentials and issues a JWT. A deactivated account gets
// ErrAccountDisabled, but only after the password matched, so the state is
// never revealed to someone without the password. An unknown email costs a
// bcrypt compare too (unknownUserHash), for the same reason.
func (s *Service) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	u, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(unknownUserHash, []byte(password))
		return nil, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	if !u.IsActive {
		return nil, ErrAccountDisabled
	}

	if err := s.userRepo.UpdateLastLogin(ctx, u.ID); err != nil {
		s.log.Warn("failed to update last login", zap.Error(err), zap.String("user_id", u.ID))
	}

	token, proof, expires, err := s.signIn(u.ID)
	if err != nil {
		return nil, err
	}

	return &LoginResult{User: u, Token: token, IsOperator: s.isOperator(ctx, u.ID), Reauth: proof, ReauthExpires: expires}, nil
}

// signIn mints a session token for userID and, since its password was
// typed a moment ago, a password confirmation for that session lasting
// reauth.SignInTTL (D-161).
func (s *Service) signIn(userID string) (token, proof string, expires time.Time, err error) {
	token, err = s.generateJWT(userID)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate token: %w", err)
	}
	proof, expires, err = reauth.Issue(s.jwtSecret, userID, token, time.Now(), reauth.SignInTTL)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("issue reauth proof: %w", err)
	}
	return token, proof, expires, nil
}

// isOperator fails closed: a failed check reports false and is logged, so
// login never fails because of it.
func (s *Service) isOperator(ctx context.Context, userID string) bool {
	if s.gate == nil {
		return false
	}
	ok, err := s.gate.IsOperator(ctx, userID)
	if err != nil {
		s.log.Warn("operator check failed; reporting is_operator=false", zap.Error(err), zap.String("user_id", userID))
		return false
	}
	return ok
}

// Reauthenticate checks password against the signed-in account and returns
// proof that it did, bound to session and valid until the returned time
// (reauth.TTL; D-23, D-53). Host shells and host-key replacement need it.
func (s *Service) Reauthenticate(ctx context.Context, userID, session, password string) (string, time.Time, error) {
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("get user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return "", time.Time{}, ErrWrongPassword
	}
	token, expires, err := reauth.Issue(s.jwtSecret, userID, session, time.Now(), reauth.TTL)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("issue reauth proof: %w", err)
	}
	return token, expires, nil
}

// IssueToken mints a session token for userID (user.TokenIssuer): the
// password endpoint hands the caller a token issued after the change.
func (s *Service) IssueToken(userID string) (string, error) { return s.generateJWT(userID) }

func (s *Service) generateJWT(userID string) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(), // 30 days
		"iat": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}
