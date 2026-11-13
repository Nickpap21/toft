package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SettingPanel is the instance_settings key holding PanelSettings (D-62).
const SettingPanel = "panel"

// SettingRestore is the instance_settings key install.sh --restore writes
// once the backup is loaded (D-163): {"at": <time>}. A panel that was never
// restored has no such row.
const SettingRestore = "restore"

// restoreMark is the JSON at instance_settings['restore'].
type restoreMark struct {
	At time.Time `json:"at"`
	// DomainReachedAt is when the panel's domain came back to this panel:
	// seen with a trusted certificate (certificateTick), or its address
	// saved after setup (SetPanelDomain). Until then the panel also answers
	// on its IP address. A new restore's mark has none.
	DomainReachedAt *time.Time `json:"domain_reached_at,omitempty"`
}

// RestoredAt is when this panel was restored from a backup; nil when it
// never was.
func (s *Service) RestoredAt(ctx context.Context) (*time.Time, error) {
	var r restoreMark
	ok, err := s.readJSON(ctx, SettingRestore, &r)
	if err != nil || !ok || r.At.IsZero() {
		return nil, err
	}
	return &r.At, nil
}

// PanelSettings is the JSON at instance_settings['panel'], set in the setup
// wizard. A missing row means the panel's domain is BASE_DOMAIN.
type PanelSettings struct {
	// Domain is the panel's host name, or "" when it answers on the
	// server's IP address only.
	Domain string `json:"domain"`
}

// PanelRoutes is what Traefik routes to the panel: the web app, and the API
// under /api on the same origin (D-62).
type PanelRoutes struct {
	// Domain is "" until one is set: then every request that no app claims
	// reaches the panel, which is how the setup URL works on an IP address.
	Domain string
	// LegacyAPIHost is api.<domain> on installs whose domain comes from
	// BASE_DOMAIN, where API clients and webhooks use it; "" otherwise.
	LegacyAPIHost string
	// AnyHost keeps the panel answering on any host no app claims, the
	// server's IP included, beside Domain: while setup is in progress, so
	// the browser that set the domain can follow its certificate, and on a
	// restored panel until its domain comes back to it (D-163).
	AnyHost bool
	// Dashboard routes traefik.<Domain> to Traefik's dashboard, behind its
	// basic auth: only with a domain and TRAEFIK_DASHBOARD_USERS set.
	Dashboard bool
	// CertificateAsk changes each time the panel has Traefik ask for its
	// certificate again (certificateTick): Traefik asks Let's Encrypt only
	// when its configuration changes. 0: no ask.
	CertificateAsk int64
}

// PanelRoutesWriter puts the panel's routes where Traefik reads them.
type PanelRoutesWriter interface {
	WritePanelRoutes(PanelRoutes) error
}

// WithPanelRoutes sets where the panel's routes are written, the domain
// BASE_DOMAIN gives when no domain was set in the wizard, and whether
// Traefik's dashboard has users (TRAEFIK_DASHBOARD_USERS).
func (s *Service) WithPanelRoutes(w PanelRoutesWriter, envDomain string, dashboard bool) *Service {
	s.routes = w
	s.envDomain = envDomain
	s.dashboard = dashboard
	return s
}

// PanelDomain is the panel's host name, "" while it has none. It is read
// from the database by SyncPanelRoutes; until then it is BASE_DOMAIN.
func (s *Service) PanelDomain() string {
	if d := s.panelDomain.Load(); d != nil {
		return *d
	}
	return s.envDomain
}

// SyncPanelRoutes reads the panel's domain and writes its routes. The
// server runs it at startup, so the routes always match the setting.
func (s *Service) SyncPanelRoutes(ctx context.Context) error {
	// One at a time: the last to run has read every write before it, so
	// the routes and awaitingDomain agree with the database.
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	domain := s.envDomain
	raw, err := s.repo.GetSetting(ctx, SettingPanel)
	switch {
	case err == nil:
		var p PanelSettings
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("decode panel settings: %w", err)
		}
		domain = p.Domain
	case !errors.Is(err, ErrNotFound):
		return fmt.Errorf("read panel settings: %w", err)
	}
	s.panelDomain.Store(&domain)
	if s.routes == nil {
		return nil
	}
	inSetup, err := s.setupInProgress(ctx)
	if err != nil {
		return err
	}
	// A restored panel's domain still points at the old server, or at
	// nothing: it answers on its IP until the domain arrives (D-163).
	var mark restoreMark
	if _, err := s.readJSON(ctx, SettingRestore, &mark); err != nil {
		return err
	}
	awaiting := !mark.At.IsZero() && mark.DomainReachedAt == nil
	r := PanelRoutes{Domain: domain, AnyHost: domain == "" || inSetup || awaiting, Dashboard: domain != "" && s.dashboard}
	if domain != "" && domain == s.envDomain {
		r.LegacyAPIHost = "api." + domain
	}
	if r.CertificateAsk, err = s.certificateAskN(ctx, domain); err != nil {
		return err
	}
	if err := s.routes.WritePanelRoutes(r); err != nil {
		return fmt.Errorf("write panel routes: %w", err)
	}
	// Set once the routes say so: a failed write leaves the tick looking.
	s.awaitingDomain.Store(awaiting)
	return nil
}

// markDomainReached records that a restored panel's domain came back to it.
// Only this field is written: the merge keeps the mark's "at".
func (s *Service) markDomainReached(ctx context.Context, actorID string, at time.Time) error {
	return s.writeJSON(ctx, actorID, SettingRestore, map[string]time.Time{"domain_reached_at": at.UTC()})
}

// domainNotReached, merged into the restore mark, takes back
// markDomainReached: the panel waits for its domain again.
var domainNotReached = []byte(`{"domain_reached_at":null}`)

// ErrNeverRestored: the panel was not restored from a backup, so it has no
// answer on its IP address to bring back.
var ErrNeverRestored = errors.New("this panel was not restored from a backup")

// AnswerOnIP is `server answer-on-ip` (toft answer-on-ip): a restored panel
// waits for its domain again, so from its next start it answers on its IP
// address too, until the domain reaches it with a trusted certificate. The
// way back for an operator who stopped the IP answer while the domain
// pointed elsewhere. It reports whether the panel was already waiting.
func AnswerOnIP(ctx context.Context, repo Repository) (already bool, err error) {
	raw, err := repo.GetSetting(ctx, SettingRestore)
	if errors.Is(err, ErrNotFound) {
		return false, ErrNeverRestored
	}
	if err != nil {
		return false, fmt.Errorf("read the restore mark: %w", err)
	}
	var mark restoreMark
	if err := json.Unmarshal(raw, &mark); err != nil {
		return false, fmt.Errorf("decode the restore mark: %w", err)
	}
	if mark.At.IsZero() {
		return false, ErrNeverRestored
	}
	if mark.DomainReachedAt == nil {
		return true, nil
	}
	if err := repo.MergeSetting(ctx, SettingRestore, domainNotReached, ""); err != nil {
		return false, fmt.Errorf("write the restore mark: %w", err)
	}
	// No actor: it was run on the server.
	if err := repo.WriteAudit(ctx, "", "panel.ip_answer_restored", "", nil); err != nil {
		return false, fmt.Errorf("record it: %w", err)
	}
	return false, nil
}
