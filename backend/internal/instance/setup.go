package instance

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/reservation"
	"go.uber.org/zap"
)

// The setup wizard's settings (D-62). SettingSetup is written with the
// first account, so an install set up before the wizard existed is never
// sent through it; SettingServerRole and SettingPanel are read by every
// install, a missing row being today's behaviour.
const (
	SettingSetup      = "setup"
	SettingServerRole = "server_role"
)

// Server roles of the panel host (D-26, D-62).
const (
	// RoleApps is "Control plane and apps": the panel host runs apps too.
	RoleApps = "apps"
	// RoleControlPlane is "Control plane only": nothing deploys there.
	RoleControlPlane = "control_plane"
)

var (
	ErrInvalidDomain   = errors.New("the domain must be a host name such as panel.example.com")
	ErrInvalidRole     = errors.New("the role must be apps or control_plane")
	ErrInvalidPatch    = errors.New("give one of kit_acknowledged or finished")
	ErrSetupIncomplete = errors.New("choose the server role and store the recovery kit before finishing setup")
	// ErrKitFingerprintRequired and ErrKitChanged refuse an acknowledgement
	// that doesn't name the kit as it is now (D-163): what was stored is
	// then not what a restore needs.
	ErrKitFingerprintRequired = errors.New("kit_acknowledged needs the fingerprint of the kit that was read")
	ErrKitChanged             = errors.New("the recovery kit changed after it was read; read it again and store that one")
	// ErrPanelDomainUnreachable refuses ending the panel's answer on its IP
	// address for a domain this server can't reach the panel through: finishing
	// setup (SetupPatch.DomainChecked) or setting the domain after it.
	ErrPanelDomainUnreachable = errors.New("that domain doesn't reach this panel yet, and on a domain the panel stops answering on its IP address")
)

// PanelHostTooSmallError refuses Control plane and apps on a server below
// reservation.Fits (D-66, D-132), which the wizard greys out.
type PanelHostTooSmallError struct {
	CPUs        int
	MemoryBytes int64
}

func (e *PanelHostTooSmallError) Error() string {
	// The host's figure rounds down, so one just under the floor never
	// reads as the floor itself.
	return fmt.Sprintf("Run apps here too needs at least %d vCPU and %.1f GiB of memory, which a server sold as 4 GB passes; this server has %d vCPU and %.1f GiB",
		reservation.MinCPUs, float64(reservation.MinMemory)/(1<<30), e.CPUs, math.Floor(float64(e.MemoryBytes)/(1<<30)*10)/10)
}

// PanelHostInUseError refuses Control plane only while services are set to
// run on the panel host.
type PanelHostInUseError struct{ Services int64 }

func (e *PanelHostInUseError) Error() string {
	return fmt.Sprintf("%d services are set to run on this server; move them to another server before choosing Only Toft", e.Services)
}

// Setup is where the wizard stands, as GET /instance/setup returns it.
type Setup struct {
	InProgress        bool       `json:"in_progress"`
	Domain            string     `json:"domain"`
	DomainSet         bool       `json:"domain_set"`
	Role              string     `json:"role"`
	RoleSet           bool       `json:"role_set"`
	KitAcknowledgedAt *time.Time `json:"kit_acknowledged_at"`
	// KitOutOfDate: the encryption key or the panel host's backup settings
	// changed after the kit was stored (D-163).
	KitOutOfDate bool       `json:"kit_out_of_date"`
	FinishedAt   *time.Time `json:"finished_at"`
	App          string     `json:"app"`
	// AwaitingDomain: a restored panel that still answers on its IP address
	// beside its domain, until the domain comes back to it (D-163).
	AwaitingDomain bool `json:"awaiting_domain"`
}

// SetupPatch is the body of PATCH /instance/setup: exactly one change.
type SetupPatch struct {
	KitAcknowledged bool `json:"kit_acknowledged"`
	// Fingerprint, with KitAcknowledged: RecoveryKit.Fingerprint of the kit
	// the operator stored.
	Fingerprint string `json:"fingerprint"`
	Finished    bool   `json:"finished"`
	// DomainChecked, with Finished: the operator opened the panel's domain
	// and it works, so it isn't probed.
	DomainChecked bool `json:"domain_checked"`
}

// setupState is the JSON at instance_settings['setup'].
type setupState struct {
	StartedAt         *time.Time `json:"started_at,omitempty"`
	KitAcknowledgedAt *time.Time `json:"kit_acknowledged_at,omitempty"`
	// KitFingerprint is kitFingerprint of the kit acknowledged then, and
	// KitKeyFingerprint that of its key with no backup.
	KitFingerprint    string     `json:"kit_fingerprint,omitempty"`
	KitKeyFingerprint string     `json:"kit_key_fingerprint,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

type roleSetting struct {
	Role string `json:"role"`
}

// PanelDomainCheck is what POST /instance/setup/domain-check answers.
type PanelDomainCheck struct {
	Domain       string           `json:"domain"`
	Addresses    []string         `json:"addresses"`
	ReachesPanel bool             `json:"reaches_panel"`
	Certificate  CertificateState `json:"certificate"`
}

// CertificateState is the certificate a domain serves: none while it is
// not the panel's domain, pending until a trusted one is served.
type CertificateState struct {
	State     string     `json:"state"`
	ExpiresAt *time.Time `json:"expires_at"`
	Issuer    string     `json:"issuer"`
}

// CertIssued is CertificateState.State once the domain serves a certificate
// this server trusts.
const CertIssued = "issued"

// DomainProber looks a domain up and fetches this panel's probe through it.
// The probe is implemented in cmd/server: DNS and HTTPS are infrastructure.
type DomainProber interface {
	// ProbeDomain resolves domain and fetches https://domain/api/v1/instance/probe,
	// reporting whether the answer is probeID. With withCert it also reports
	// the certificate the domain serves, verified against the system roots.
	ProbeDomain(ctx context.Context, domain, probeID string, withCert bool) (PanelDomainCheck, error)
}

// WithSetup sets what the wizard needs besides the database: how domains
// are probed, and the catalog template the installer preselected (--app).
func (s *Service) WithSetup(p DomainProber, app string) *Service {
	s.prober = p
	s.setupApp = app
	return s
}

// newProbeID is minted once per process: the domain check compares what it
// fetches through a domain with it.
func newProbeID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error()) // never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

// ProbeID is this process's probe value (GET /instance/probe).
func (s *Service) ProbeID() string { return s.probeID }

func (s *Service) readJSON(ctx context.Context, key string, v any) (bool, error) {
	raw, err := s.repo.GetSetting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s setting: %w", key, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("decode %s setting: %w", key, err)
	}
	return true, nil
}

func (s *Service) writeJSON(ctx context.Context, actorID, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s setting: %w", key, err)
	}
	if err := s.repo.MergeSetting(ctx, key, raw, actorID); err != nil {
		return fmt.Errorf("write %s setting: %w", key, err)
	}
	return nil
}

func (s *Service) loadSetupState(ctx context.Context) (setupState, bool, error) {
	var st setupState
	ok, err := s.readJSON(ctx, SettingSetup, &st)
	return st, ok, err
}

// setupInProgress: the first account started setup and it hasn't finished.
func (s *Service) setupInProgress(ctx context.Context) (bool, error) {
	st, ok, err := s.loadSetupState(ctx)
	return ok && st.FinishedAt == nil, err
}

// Role is the panel host's server role; RoleApps until one is chosen.
func (s *Service) Role(ctx context.Context) (string, error) {
	var r roleSetting
	ok, err := s.readJSON(ctx, SettingServerRole, &r)
	if err != nil || !ok {
		return RoleApps, err
	}
	return r.Role, nil
}

// PanelHostRunsApps reports whether deploys may target the panel host
// (deployment.PanelRole). A failed read answers yes: the role only ever
// narrows what an install did before it existed.
func (s *Service) PanelHostRunsApps(ctx context.Context) bool {
	role, err := s.Role(ctx)
	if err != nil {
		s.log.Warn("server role not read; deploys to the panel host are allowed", zap.Error(err))
		return true
	}
	return role != RoleControlPlane
}

// Setup returns where the wizard stands. Operators only.
func (s *Service) Setup(ctx context.Context, actorID string) (*Setup, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.setup(ctx)
}

func (s *Service) setup(ctx context.Context) (*Setup, error) {
	st, started, err := s.loadSetupState(ctx)
	if err != nil {
		return nil, err
	}
	var p PanelSettings
	domainSet, err := s.readJSON(ctx, SettingPanel, &p)
	if err != nil {
		return nil, err
	}
	var r roleSetting
	roleSet, err := s.readJSON(ctx, SettingServerRole, &r)
	if err != nil {
		return nil, err
	}
	out := &Setup{
		InProgress:        started && st.FinishedAt == nil,
		Domain:            s.PanelDomain(),
		DomainSet:         domainSet,
		Role:              RoleApps,
		RoleSet:           roleSet,
		KitAcknowledgedAt: st.KitAcknowledgedAt,
		FinishedAt:        st.FinishedAt,
		App:               s.setupApp,
	}
	if roleSet {
		out.Role = r.Role
	}
	// In setup and with no domain the IP answers anyway: nothing to wait for.
	out.AwaitingDomain = s.awaitingDomain.Load() && out.Domain != "" && !out.InProgress
	out.KitOutOfDate = s.kitOutOfDate(ctx, st)
	return out, nil
}

// PatchSetup records that the recovery kit was stored, or finishes setup, which needs the role and the kit. Operators only.
func (s *Service) PatchSetup(ctx context.Context, actorID string, p SetupPatch) (*Setup, error) {
	n := 0
	for _, set := range []bool{p.KitAcknowledged, p.Finished} {
		if set {
			n++
		}
	}
	if n != 1 {
		return nil, ErrInvalidPatch
	}
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	var err error
	now := time.Now().UTC()
	switch {
	case p.KitAcknowledged:
		if p.Fingerprint == "" {
			return nil, ErrKitFingerprintRequired
		}
		if s.kitKey == "" {
			return nil, fmt.Errorf("recovery kit not configured")
		}
		backup, berr := s.kitBackup(ctx)
		if berr != nil {
			return nil, berr
		}
		current := kitFingerprint(s.kitKey, backup)
		if !hmac.Equal([]byte(p.Fingerprint), []byte(current)) {
			return nil, ErrKitChanged
		}
		if err = s.writeJSON(ctx, actorID, SettingSetup, setupState{KitAcknowledgedAt: &now, KitFingerprint: current, KitKeyFingerprint: kitFingerprint(s.kitKey, nil)}); err == nil {
			// The claim a restore later depends on, audited as reading the kit is.
			s.audit(ctx, actorID, "recovery_kit.stored", "", nil)
		}
	case p.Finished:
		cur, cerr := s.setup(ctx)
		if cerr != nil {
			return nil, cerr
		}
		if !cur.RoleSet || cur.KitAcknowledgedAt == nil {
			return nil, ErrSetupIncomplete
		}
		// Finishing ends the panel's answer on its IP address, so the domain
		// must reach this panel first, or the operator must say it works.
		if cur.Domain != "" && !p.DomainChecked {
			if perr := s.requireReachesPanel(ctx, cur.Domain); perr != nil {
				return nil, perr
			}
		}
		if err = s.writeJSON(ctx, actorID, SettingSetup, setupState{FinishedAt: &now}); err == nil {
			s.audit(ctx, actorID, "setup.finished", "", nil)
			// The panel stops answering on its IP address once it has a domain.
			if serr := s.SyncPanelRoutes(ctx); serr != nil {
				s.log.Warn("panel routes not rewritten after setup", zap.Error(serr))
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return s.setup(ctx)
}

// hostname is a lowercase DNS name of at least two labels.
var hostname = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// normalizeDomain lowercases and trims domain; "" stays "" (the IP). A
// wildcard or an IP address is not a domain.
func normalizeDomain(domain string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if d == "" {
		return "", nil
	}
	if len(d) > 253 || !hostname.MatchString(d) || net.ParseIP(d) != nil {
		return "", ErrInvalidDomain
	}
	return d, nil
}

// requireReachesPanel fetches this panel's probe through domain, before
// something ends the panel's answer on its IP address.
func (s *Service) requireReachesPanel(ctx context.Context, domain string) error {
	if s.prober == nil {
		return nil
	}
	check, err := s.prober.ProbeDomain(ctx, domain, s.probeID, false)
	if err != nil {
		return fmt.Errorf("probe %s: %w", domain, err)
	}
	if !check.ReachesPanel {
		return ErrPanelDomainUnreachable
	}
	return nil
}

// SetPanelDomain stores the panel's domain ("" keeps it on the IP) and
// rewrites its routes. Operators only; the handler asks for a recent
// password confirmation first (D-23). After setup the IP stops answering at
// once, on a restored panel that was waiting for its domain too (D-163), so
// the domain must reach this panel first, or checked must carry the
// operator's word that it works.
func (s *Service) SetPanelDomain(ctx context.Context, actorID, domain string, checked bool) (*Setup, error) {
	d, err := normalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	afterSetup := false
	if d != "" {
		inSetup, err := s.setupInProgress(ctx)
		if err != nil {
			return nil, err
		}
		afterSetup = !inSetup
		if afterSetup && !checked {
			if err := s.requireReachesPanel(ctx, d); err != nil {
				return nil, err
			}
		}
	}
	from := s.PanelDomain()
	before, err := s.repo.GetSetting(ctx, SettingPanel)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("read panel settings: %w", err)
	}
	stopped := afterSetup && s.awaitingDomain.Load()
	save := func() error {
		if err := s.writeJSON(ctx, actorID, SettingPanel, PanelSettings{Domain: d}); err != nil {
			return err
		}
		if stopped {
			if err := s.markDomainReached(ctx, actorID, time.Now()); err != nil {
				return err
			}
		}
		return s.SyncPanelRoutes(ctx)
	}
	if err := save(); err != nil {
		s.undoPanelDomain(ctx, actorID, before, stopped, from, d)
		return nil, err
	}
	switch {
	case d != from:
		s.audit(ctx, actorID, "panel.domain_changed", "", map[string]any{"from": from, "to": d})
	case stopped:
		// "Stop answering on the IP…" (D-164): the address stayed.
		s.audit(ctx, actorID, "panel.ip_answer_stopped", "", map[string]any{"domain": d})
		// Neither: the same address saved again, as by a Stop pressed after
		// the domain arrived by itself. Nothing changed, so nothing is recorded.
	}
	return s.setup(ctx)
}

// undoPanelDomain puts the panel's address back as it was (before; nil: it
// had none of its own) after a save whose routes were not written. Left
// saved, the next start would apply it, and on a restored panel end the IP
// answer, when nobody expects it. It is recorded either way.
func (s *Service) undoPanelDomain(ctx context.Context, actorID string, before []byte, stopped bool, from, to string) {
	ctx = context.WithoutCancel(ctx)
	var err error
	if before == nil {
		err = s.repo.DeleteSetting(ctx, SettingPanel)
	} else {
		err = s.repo.MergeSetting(ctx, SettingPanel, before, actorID)
	}
	if err == nil && stopped {
		err = s.repo.MergeSetting(ctx, SettingRestore, domainNotReached, actorID)
	}
	if err != nil {
		s.log.Error("the panel's address is saved, its routes are not written and putting it back failed: the next start applies it",
			zap.String("from", from), zap.String("to", to), zap.Error(err))
	}
	// What the service remembers follows the database again.
	if serr := s.SyncPanelRoutes(ctx); serr != nil {
		s.log.Warn("panel routes not written after putting the panel's address back", zap.Error(serr))
	}
	s.audit(ctx, actorID, "panel.domain_change_failed", "", map[string]any{"from": from, "to": to, "undone": err == nil})
}

// CheckPanelDomain looks domain up and checks it reaches this panel; once it
// is the panel's domain, also its certificate. Changes nothing. Operators only.
func (s *Service) CheckPanelDomain(ctx context.Context, actorID, domain string) (*PanelDomainCheck, error) {
	d, err := normalizeDomain(domain)
	if err != nil || d == "" {
		return nil, ErrInvalidDomain
	}
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if s.prober == nil {
		return nil, errors.New("domain prober not configured")
	}
	check, err := s.prober.ProbeDomain(ctx, d, s.probeID, d == s.PanelDomain())
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", d, err)
	}
	if d != s.PanelDomain() {
		check.Certificate = CertificateState{State: "none"}
	}
	if check.Addresses == nil {
		check.Addresses = []string{}
	}
	return &check, nil
}

// SetServerRole sets the panel host's role. Control plane only is refused
// while services are set to run there. Operators only.
func (s *Service) SetServerRole(ctx context.Context, actorID, role string) (*Setup, error) {
	if role != RoleApps && role != RoleControlPlane {
		return nil, ErrInvalidRole
	}
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if role == RoleApps && s.panelHost != nil {
		host, err := s.panelHost.PanelHost(ctx)
		if err != nil {
			return nil, fmt.Errorf("read the panel host: %w", err)
		}
		if !reservation.Fits(host.CPUCores, host.MemoryBytes) {
			return nil, &PanelHostTooSmallError{CPUs: host.CPUCores, MemoryBytes: host.MemoryBytes}
		}
	}
	if role == RoleControlPlane {
		n, err := s.repo.CountPanelHostServices(ctx)
		if err != nil {
			return nil, fmt.Errorf("count panel host services: %w", err)
		}
		if n > 0 {
			return nil, &PanelHostInUseError{Services: n}
		}
	}
	from, err := s.Role(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.writeJSON(ctx, actorID, SettingServerRole, roleSetting{Role: role}); err != nil {
		return nil, err
	}
	s.audit(ctx, actorID, "panel.role_changed", "", map[string]any{"from": from, "to": role})
	return s.setup(ctx)
}
