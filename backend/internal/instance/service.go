package instance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// Service holds the instance-level business logic. It satisfies
// auth.InstanceGate, org.OperatorChecker and user.OperatorChecker.
type Service struct {
	repo Repository
	log  *zap.Logger
	// setupTokenHash is sha256(TOFT_SETUP_TOKEN), or nil when it is unset;
	// setupTokenExpires ends it (zero: never).
	setupTokenHash    []byte
	setupTokenExpires time.Time
	panelHost         PanelHostReader
	passwords         PasswordVerifier
	// The panel's own address (panel.go) and the setup wizard (setup.go).
	routes      PanelRoutesWriter
	envDomain   string
	dashboard   bool
	panelDomain atomic.Pointer[string]
	prober      DomainProber
	setupApp    string
	probeID     string
	// A restored panel waiting for its domain (panel.go, D-163):
	// SyncPanelRoutes, one at a time, sets awaitingDomain; domainTriedAt
	// and certificateAskNone (no certificate_ask row) are the monitor
	// goroutine's own.
	routesMu           sync.Mutex
	awaitingDomain     atomic.Bool
	domainTriedAt      time.Time
	certificateAskNone bool
	// The recovery kit (kit.go).
	kitKey     string
	kitVersion string
	kitBackups KitBackupReader
	// The panel's own containers and server (panelstatus.go, D-68).
	panel        *panelMonitor
	panelShellOn bool
	gvisorBusy   atomic.Bool // a gVisor install on the panel's server (panelgvisor.go)
	gitOn        bool
	advisories   *advisory.Book // Docker's advisories (panelupdates.go, D-133)
	deploys      DeployChecker  // a deploy to the panel's server (panelupdaterun.go, D-134)
	// The panel's server's run of updates (panelupdaterun.go): one start
	// or read of it at a time, and the last read.
	panelRunMu   sync.Mutex
	panelRunSeen panelUpdateRun
	panelRunAt   time.Time
	// Upgrades from the panel (upgrade.go).
	upgradeDir   string
	upgradeMu    sync.Mutex
	upgradeAsked *upgradeAsk
	// The watchdog (watchdog.go).
	watchdogClient *http.Client
	watchdogMu     sync.Mutex
	watchdogLast   watchdogState
	alerts         UpgradeAlerter
}

// PasswordVerifier checks a user's own password; user.Service satisfies it.
type PasswordVerifier interface {
	VerifyPassword(ctx context.Context, userID, password string) (bool, error)
}

// WithPasswordVerifier sets how operator changes check the operator's
// password. Without one, every change is refused.
func (s *Service) WithPasswordVerifier(v PasswordVerifier) *Service {
	s.passwords = v
	return s
}

// PanelHostReader reads the panel host's facts: its Docker daemon, its disk
// and the panel's own name.
type PanelHostReader interface {
	PanelHost(ctx context.Context) (PanelHost, error)
}

// WithPanelHost sets where PanelHost reads from.
func (s *Service) WithPanelHost(r PanelHostReader) *Service {
	s.panelHost = r
	return s
}

// PanelHost returns the panel host's facts. Any signed-in member may read
// them: every workspace can deploy there.
func (s *Service) PanelHost(ctx context.Context) (PanelHost, error) {
	if s.panelHost == nil {
		return PanelHost{}, errors.New("panel host reader not configured")
	}
	host, err := s.panelHost.PanelHost(ctx)
	if err != nil {
		return PanelHost{}, err
	}
	if host.Role, err = s.Role(ctx); err != nil {
		return PanelHost{}, err
	}
	return host, nil
}

// NewService creates an instance Service.
func NewService(repo Repository, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{repo: repo, log: log, probeID: newProbeID()}
}

// WithSetupToken makes first-run registration require token (TOFT_SETUP_TOKEN).
// An empty token leaves first-run registration open to whoever registers first.
func (s *Service) WithSetupToken(token string) *Service {
	if token == "" {
		s.setupTokenHash = nil
		return s
	}
	h := sha256.Sum256([]byte(normalizeSetupToken(token)))
	s.setupTokenHash = h[:]
	return s
}

// normalizeSetupToken makes the installer's code forgiving to type
// (D-61): case, hyphens and spaces don't count, so "fj7k 29qx" is
// "FJ7K-29QX".
func normalizeSetupToken(t string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, t))
}

// SetupTokenRequired reports whether TOFT_SETUP_TOKEN is configured.
func (s *Service) SetupTokenRequired() bool {
	return s.setupTokenHash != nil
}

// WithSetupTokenExpiry ends the setup token at expires (TOFT_SETUP_TOKEN_EXPIRES,
// D-61); zero leaves it valid.
func (s *Service) WithSetupTokenExpiry(expires time.Time) *Service {
	s.setupTokenExpires = expires
	return s
}

// SetupTokenExpired reports whether a configured setup token has expired.
func (s *Service) SetupTokenExpired() bool {
	return s.setupTokenHash != nil && !s.setupTokenExpires.IsZero() && time.Now().After(s.setupTokenExpires)
}

// VerifySetupToken reports whether provided matches TOFT_SETUP_TOKEN. It is
// always true when no setup token is configured. The comparison is constant
// time over fixed-length hashes, so it leaks neither content nor length.
func (s *Service) VerifySetupToken(provided string) bool {
	if s.setupTokenHash == nil {
		return true
	}
	h := sha256.Sum256([]byte(normalizeSetupToken(provided)))
	return subtle.ConstantTimeCompare(h[:], s.setupTokenHash) == 1
}

// IsOperator reports whether userID is an instance operator. Read from the
// database on every call, never from the JWT, so a revoke takes effect at once.
func (s *Service) IsOperator(ctx context.Context, userID string) (bool, error) {
	return s.repo.IsOperator(ctx, userID)
}

// RegistrationMode returns the configured registration mode. A missing row,
// a read error and an unknown or malformed value all give ModeInviteOnly:
// registration fails closed. Errors are logged.
func (s *Service) RegistrationMode(ctx context.Context) string {
	raw, err := s.repo.GetSetting(ctx, SettingRegistration)
	if errors.Is(err, ErrNotFound) {
		return ModeInviteOnly
	}
	if err != nil {
		s.log.Error("read registration setting; falling back to invite_only", zap.Error(err))
		return ModeInviteOnly
	}
	settings, err := ParseRegistrationSettings(raw)
	if err != nil {
		s.log.Error("invalid registration setting; falling back to invite_only", zap.Error(err))
		return ModeInviteOnly
	}
	return settings.Mode
}

// RegistrationStatus is the public registration state for the sign-up page.
func (s *Service) RegistrationStatus(ctx context.Context) (RegistrationStatus, error) {
	exists, err := s.repo.UsersExist(ctx)
	if err != nil {
		return RegistrationStatus{}, fmt.Errorf("check users exist: %w", err)
	}
	return RegistrationStatus{
		Mode:               s.RegistrationMode(ctx),
		FirstRun:           !exists,
		SetupTokenRequired: !exists && s.SetupTokenRequired(),
	}, nil
}

// requireOperator returns ErrNotOperator unless actorID is an operator. A
// failed check denies too; it is returned as an error rather than
// ErrNotOperator so an operator sees a server error, not a misleading 403.
func (s *Service) requireOperator(ctx context.Context, actorID string) error {
	ok, err := s.repo.IsOperator(ctx, actorID)
	if err != nil {
		return fmt.Errorf("check operator: %w", err)
	}
	if !ok {
		return ErrNotOperator
	}
	return nil
}

// reauthenticate is requireOperator plus the operator's own password, asked
// again for every change to accounts and operators (D-23, D-50).
func (s *Service) reauthenticate(ctx context.Context, actorID, password string) error {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return err
	}
	if s.passwords == nil {
		return errors.New("password verifier not configured")
	}
	ok, err := s.passwords.VerifyPassword(ctx, actorID, password)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return ErrWrongPassword
	}
	return nil
}

// ListOperators returns every instance operator. Operator-only.
func (s *Service) ListOperators(ctx context.Context, actorID string) ([]Operator, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.repo.ListOperators(ctx)
}

// GrantOperator makes a user an operator, identified by targetEmail
// (case-insensitive, exactly one match) or else targetUserID. Idempotent.
// Operator-only, with the operator's password.
func (s *Service) GrantOperator(ctx context.Context, actorID, password, targetUserID, targetEmail string) (*Operator, error) {
	if err := s.reauthenticate(ctx, actorID, password); err != nil {
		return nil, err
	}

	userID, err := s.resolveUser(ctx, targetUserID, targetEmail)
	if err != nil {
		return nil, err
	}

	grantedBy := actorID
	granted, err := s.repo.GrantOperator(ctx, userID, &grantedBy, SourceAPI)
	if err != nil {
		return nil, fmt.Errorf("grant operator: %w", err)
	}
	if granted {
		s.log.Info("instance operator granted",
			zap.String("user_id", userID), zap.String("granted_by", actorID), zap.String("source", SourceAPI))
		s.audit(ctx, actorID, "operator.granted", userID, nil)
	}

	ops, err := s.repo.ListOperators(ctx)
	if err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	for i := range ops {
		if ops[i].UserID == userID {
			return &ops[i], nil
		}
	}
	return nil, fmt.Errorf("operator %s not found after grant", userID)
}

func (s *Service) resolveUser(ctx context.Context, userID, email string) (string, error) {
	if email = strings.TrimSpace(email); email != "" {
		ids, err := s.repo.FindUserIDsByEmail(ctx, email)
		if err != nil {
			return "", fmt.Errorf("find user by email: %w", err)
		}
		switch len(ids) {
		case 0:
			return "", ErrUserNotFound
		case 1:
			return ids[0], nil
		default:
			return "", ErrAmbiguousEmail
		}
	}
	exists, err := s.repo.UserExists(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("check user exists: %w", err)
	}
	if !exists {
		return "", ErrUserNotFound
	}
	return userID, nil
}

// RevokeOperator removes targetUserID's operator status. Operators may revoke
// themselves, but never the last active operator (ErrLastOperator).
// Operator-only, with the operator's password.
func (s *Service) RevokeOperator(ctx context.Context, actorID, password, targetUserID string) error {
	if err := s.reauthenticate(ctx, actorID, password); err != nil {
		return err
	}
	if err := s.repo.RevokeOperator(ctx, targetUserID); err != nil {
		return err
	}
	s.log.Info("instance operator revoked", zap.String("user_id", targetUserID), zap.String("revoked_by", actorID))
	s.audit(ctx, actorID, "operator.revoked", targetUserID, nil)
	return nil
}

// ListUsers returns every account on the instance. Operator-only.
//
// ListUsers also reports how many deleted accounts the list leaves out.
func (s *Service) ListUsers(ctx context.Context, actorID string) ([]User, int64, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, 0, err
	}
	users, err := s.repo.ListUsers(ctx)
	if err != nil {
		return nil, 0, err
	}
	deleted, err := s.repo.CountDeletedUsers(ctx)
	if err != nil {
		return nil, 0, err
	}
	return users, deleted, nil
}

// DeleteUser turns a deactivated, non-operator account into a tombstone
// (D-57): its email is freed, its name and password replaced, its
// memberships, API keys and reset links removed; projects, servers and audit
// rows keep pointing at the row and read "Deleted account". Irreversible.
// Operator-only, with the operator's password; never yourself.
func (s *Service) DeleteUser(ctx context.Context, actorID, password, targetUserID string) error {
	if err := s.reauthenticate(ctx, actorID, password); err != nil {
		return err
	}
	if strings.EqualFold(actorID, targetUserID) {
		return ErrCannotDeleteSelf
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate password: %w", err)
	}
	hashed, err := bcrypt.GenerateFromPassword(raw, bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	email, err := s.repo.DeleteUser(ctx, actorID, targetUserID, string(hashed))
	if err != nil {
		return err
	}
	s.log.Info("user deleted", zap.String("user_id", targetUserID), zap.String("actor_id", actorID))
	s.audit(ctx, actorID, "user.deleted", targetUserID, map[string]any{"email": email})
	return nil
}

// ListAudit returns a page of the instance audit log, newest first (D-59:
// limit 50 by default, 200 at most; cursor from the previous page). It
// returns the cursor of the next page, or "" on the last. kind is "panel",
// "accounts" or "" for both. Operator-only.
func (s *Service) ListAudit(ctx context.Context, actorID string, limit int, cursor, kind string) ([]AuditEntry, string, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, "", err
	}
	if kind != "" && kind != "panel" && kind != "accounts" {
		return nil, "", ErrBadKind
	}
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	at, beforeID, err := respond.ParseCursor(cursor)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrBadCursor, err)
	}
	var before *time.Time
	if beforeID != "" {
		before = &at
	}
	entries, err := s.repo.ListAudit(ctx, int32(limit), before, beforeID, kind)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if n := len(entries); n > 0 && n == limit {
		next = respond.Cursor(entries[n-1].CreatedAt, entries[n-1].ID)
	}
	return entries, next, nil
}

// WriteAudit appends an instance audit row for another package's change
// (agents paused on the panel, D-100), best effort as audit.
func (s *Service) WriteAudit(ctx context.Context, actorID, action string, detail map[string]any) {
	s.audit(ctx, actorID, action, "", detail)
}

// audit appends an instance audit row, best effort: a failed write is
// logged, never a failed change (as org.Service.WriteAuditLogDetail).
func (s *Service) audit(ctx context.Context, actorID, action, targetUserID string, detail map[string]any) {
	var raw []byte
	if len(detail) > 0 {
		var err error
		if raw, err = json.Marshal(detail); err != nil {
			s.log.Warn("instance audit detail not encoded", zap.Error(err), zap.String("action", action))
			raw = nil
		}
	}
	if err := s.repo.WriteAudit(ctx, actorID, action, targetUserID, raw); err != nil {
		s.log.Warn("instance audit write failed", zap.Error(err), zap.String("action", action))
	}
}

// SetUserActive deactivates or reactivates an account. Deactivation takes
// effect at once for new requests: the user cannot log in, and their JWTs and
// API keys are refused. Requests already in flight (terminal sessions, event
// streams) are cut by the auth middleware's periodic re-check. Every pending
// invite the user created, or can read as a workspace owner, is expired so
// they cannot register a fresh account from it. An operator cannot deactivate
// themselves, nor the last active operator. Operator-only, with the
// operator's password.
func (s *Service) SetUserActive(ctx context.Context, actorID, password, targetUserID string, active bool) (*UserUpdate, error) {
	if err := s.reauthenticate(ctx, actorID, password); err != nil {
		return nil, err
	}
	if !active && strings.EqualFold(actorID, targetUserID) {
		return nil, ErrCannotDeactivateSelf
	}
	u, err := s.repo.SetUserActive(ctx, actorID, targetUserID, active)
	if err != nil {
		return nil, err
	}
	if active {
		s.log.Info("user reactivated", zap.String("user_id", targetUserID), zap.String("actor_id", actorID))
		s.audit(ctx, actorID, "user.reactivated", targetUserID, nil)
	} else {
		s.log.Info("user deactivated", zap.String("user_id", targetUserID), zap.String("actor_id", actorID),
			zap.Int64("invites_revoked", u.InvitesRevoked))
		s.audit(ctx, actorID, "user.deactivated", targetUserID, map[string]any{"invites_revoked": u.InvitesRevoked})
	}
	return u, nil
}

// PasswordResetTTL is how long a reset link works.
const PasswordResetTTL = time.Hour

// IssuePasswordReset creates a one-time reset link for an active account
// (D-56) and returns its raw token once; only its sha256 is stored. One link
// is live per account: issuing replaces the earlier unused ones. The link
// holder sets the password; the operator never learns it. Operator-only,
// with the operator's password.
func (s *Service) IssuePasswordReset(ctx context.Context, actorID, password, targetUserID string) (*PasswordReset, error) {
	if err := s.reauthenticate(ctx, actorID, password); err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := time.Now().Add(PasswordResetTTL)
	if err := s.repo.CreatePasswordReset(ctx, targetUserID, actorID, hashResetToken(token), expiresAt); err != nil {
		return nil, err
	}
	s.log.Info("password reset issued", zap.String("user_id", targetUserID), zap.String("actor_id", actorID))
	s.audit(ctx, actorID, "password_reset.issued", targetUserID, nil)
	return &PasswordReset{Token: token, ExpiresAt: expiresAt}, nil
}

// GetPasswordReset describes a live link to its holder.
func (s *Service) GetPasswordReset(ctx context.Context, token string) (*PasswordResetTarget, error) {
	return s.repo.GetPasswordReset(ctx, hashResetToken(token))
}

// RedeemPasswordReset sets the account's password from a live link, which
// is then used up, and returns the account's email so the client can sign
// in. Every other session of the account ends (tokens_valid_from); with
// revokeAccess, its API keys, SSH keys and agent connections too.
func (s *Service) RedeemPasswordReset(ctx context.Context, token, newPassword string, revokeAccess bool) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	who, err := s.repo.RedeemPasswordReset(ctx, hashResetToken(token), string(hashed), revokeAccess)
	if err != nil {
		return "", err
	}
	s.log.Info("password reset used", zap.String("user_id", who.ID), zap.Bool("revoked_access", revokeAccess))
	var detail map[string]any
	if revokeAccess {
		detail = map[string]any{"revoked_access": true}
	}
	s.audit(ctx, who.ID, "password_reset.used", who.ID, detail)
	return who.Email, nil
}

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Bootstrap makes sure the instance has an active operator. It is break-glass
// only and changes nothing while an active operator exists, so an operator
// revoked through the API stays revoked across restarts.
//
//  1. While there is no active operator (the first upgrade of an existing
//     install, or every operator was lost), each account listed in emails
//     (TOFT_OPERATOR_EMAILS) is granted operator, source env.
//  2. When emails is empty and there are no operators at all, the
//     earliest-created active user is promoted, source bootstrap, with a WARN.
//
// A listed email that matches no account, or several, is logged at ERROR on
// every run, applied or not: whoever registers that address (any invite will
// do) would become an operator the next time the list is applied.
//
// It then logs the operator list. It returns an error only for database
// failures, which RunBootstrap retries.
func (s *Service) Bootstrap(ctx context.Context, emails []string) error {
	exists, err := s.repo.UsersExist(ctx)
	if err != nil {
		return fmt.Errorf("check users exist: %w", err)
	}
	if !exists {
		s.log.Info("no accounts yet; the first registration becomes the instance operator")
		return nil
	}

	before, err := s.repo.ListOperators(ctx)
	if err != nil {
		return fmt.Errorf("list operators: %w", err)
	}
	apply := countActive(before) == 0

	for _, email := range emails {
		ids, err := s.repo.FindUserIDsByEmail(ctx, email)
		if err != nil {
			return fmt.Errorf("find user %q: %w", email, err)
		}
		switch len(ids) {
		case 0:
			s.log.Error("TOFT_OPERATOR_EMAILS lists an email with no account. Whoever registers it, "+
				"with any invite, becomes an instance operator the next time the list is applied "+
				"(a restart while there is no active operator). Remove it, or create that account now",
				zap.String("email", email))
			continue
		case 1:
		default:
			s.log.Error("TOFT_OPERATOR_EMAILS email matches more than one account (case-insensitive); skipped",
				zap.String("email", email), zap.Int("matches", len(ids)))
			continue
		}
		if !apply {
			continue
		}
		granted, err := s.repo.GrantOperator(ctx, ids[0], nil, SourceEnv)
		if err != nil {
			return fmt.Errorf("grant operator to %q: %w", email, err)
		}
		if granted {
			s.log.Warn("instance operator granted from TOFT_OPERATOR_EMAILS (break-glass: there was no active operator)",
				zap.String("email", email))
		}
	}
	if len(emails) > 0 && !apply {
		s.log.Info("TOFT_OPERATOR_EMAILS not applied: the instance has an active operator. " +
			"It is break-glass only, applied at startup while no active operator exists; " +
			"manage operators through /api/v1/instance/operators")
	}

	var promoted string
	if len(emails) == 0 {
		promoted, err = s.repo.PromoteEarliestUserIfNone(ctx)
		if err != nil {
			return fmt.Errorf("promote earliest user: %w", err)
		}
	}

	ops, err := s.repo.ListOperators(ctx)
	if err != nil {
		return fmt.Errorf("list operators: %w", err)
	}

	if promoted != "" {
		email := promoted
		for _, op := range ops {
			if op.UserID == promoted {
				email = op.Email
			}
		}
		s.log.Warn("no instance operator existed; promoted the earliest-created user. "+
			"If that is the wrong person, they can grant the right ones (POST /api/v1/instance/operators) "+
			"and then revoke themselves (DELETE /api/v1/instance/operators/:userID)",
			zap.String("email", email), zap.String("user_id", promoted))
	}

	emailsOut := make([]string, 0, len(ops))
	for _, op := range ops {
		emailsOut = append(emailsOut, op.Email)
	}
	s.log.Info("instance operators", zap.Strings("emails", emailsOut))

	if countActive(ops) == 0 {
		s.log.Error("this instance has no active operator, so nobody can create workspaces or manage operators. " +
			"Set TOFT_OPERATOR_EMAILS to an existing account's email and restart")
	}
	return nil
}

func countActive(ops []Operator) int {
	n := 0
	for _, op := range ops {
		if op.IsActive {
			n++
		}
	}
	return n
}

// RunBootstrap runs Bootstrap, retrying every interval up to attempts times.
// A fresh local stack starts the backend before `make migrate` creates the
// instance tables, so the first attempts can fail; operator checks fail closed meanwhile.
func (s *Service) RunBootstrap(ctx context.Context, emails []string, every time.Duration, attempts int) {
	for attempt := 1; ; attempt++ {
		err := s.Bootstrap(ctx, emails)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if attempt >= attempts {
			s.log.Error("instance operator bootstrap failed; giving up until the next restart",
				zap.Int("attempts", attempt), zap.Error(err))
			return
		}
		s.log.Warn("instance operator bootstrap failed; retrying",
			zap.Int("attempt", attempt), zap.Duration("retry_in", every), zap.Error(err))

		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
