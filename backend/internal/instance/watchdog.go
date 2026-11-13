package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"go.uber.org/zap"
)

// The panel's own health, for its alerts and its watchdog (D-68).

// PanelProblems says what is wrong with the panel now, in a sentence each:
// its containers ("" when every one is well) and, with cert, its
// certificate ("" when it is fine, or when there is none to check).
// certRead is false when the certificate wasn't asked for or couldn't be
// read: that is no answer, so its alert stays as it is. It inspects the
// containers without their stats, so it is cheap.
func (s *Service) PanelProblems(ctx context.Context, cert bool) (containers, certificate string, certRead bool, err error) {
	if s.panel == nil {
		return "", "", false, errors.New("the panel's monitor is not configured")
	}
	cs, err := s.panel.probe.Containers(ctx, s.panelContainerNames(), false)
	if err != nil {
		return "", "", false, fmt.Errorf("the panel's containers: %w", err)
	}
	var bad []string
	for _, c := range cs {
		switch {
		case c.State == "missing":
			bad = append(bad, c.Name+" is missing")
		case c.State == "restarting":
			bad = append(bad, fmt.Sprintf("%s keeps restarting (%d restarts)", c.Name, c.Restarts))
		case c.State != "running":
			bad = append(bad, c.Name+" has stopped ("+c.State+")")
		case c.Health == "unhealthy":
			bad = append(bad, c.Name+" fails its health check")
		}
	}
	containers = strings.Join(bad, "; ")
	if cert {
		c := s.panelCertificate(ctx)
		certRead = c != nil || s.PanelDomain() == ""
		certificate = certProblem(c, time.Now())
	}
	return containers, certificate, certRead, nil
}

// certProblem: Let's Encrypt renews at 30 days left, so under 14 renewal is
// failing; Traefik's own certificate means it never issued one.
func certProblem(c *PanelCertificate, now time.Time) string {
	if c == nil {
		return ""
	}
	if traefikCert(c) {
		return c.Domain + " serves Traefik's self-signed certificate. " + certificateAskProblem(c.Domain, c.Ask, now)
	}
	days := int(c.NotAfter.Sub(now).Hours() / 24)
	switch {
	case days < 0:
		return fmt.Sprintf("%s's certificate expired on %s: Let's Encrypt couldn't renew it.", c.Domain, c.NotAfter.Format("2006-01-02"))
	case days < 14:
		return fmt.Sprintf("%s's certificate expires in %d days, on %s: Let's Encrypt renews at 30, so renewal is failing.", c.Domain, days, c.NotAfter.Format("2006-01-02"))
	}
	return ""
}

// ── The watchdog ─────────────────────────────────────────────────────────────

// SettingWatchdog is the instance_settings key holding the watchdog's URL.
const SettingWatchdog = "watchdog"

// watchdogEvery is how often the panel pings the watchdog while healthy.
const watchdogEvery = 5 * time.Minute

// Watchdog is the URL the panel pings while it is healthy, and the last
// ping. A service outside the panel (healthchecks.io, Better Stack, Uptime
// Kuma) alerts when the pings stop, which the panel can't do when it is down.
type Watchdog struct {
	URL        string     `json:"url"`
	LastPingAt *time.Time `json:"last_ping_at"`
	// LastStatus is the HTTP status the URL answered; null without one.
	LastStatus *int   `json:"last_status"`
	LastError  string `json:"last_error"`
	// Held: the last ping was held back because the panel wasn't healthy,
	// so the watchdog notices.
	Held bool `json:"held"`
}

// ErrWatchdogURL is a ping URL that is not an absolute https:// URL.
var ErrWatchdogURL = errors.New("the ping URL must be an https:// URL")

type watchdogState struct {
	at     *time.Time
	status *int
	err    string
	held   bool
	tried  time.Time
}

// WithWatchdog sets the client that pings, which refuses private addresses
// at connect time; nil uses netcheck's.
func (s *Service) WithWatchdog(c *http.Client) *Service {
	s.watchdogClient = c
	return s
}

func (s *Service) watchdogURL(ctx context.Context) (string, error) {
	raw, err := s.repo.GetSetting(ctx, SettingWatchdog)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var w struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &w)
	return w.URL, nil
}

func (s *Service) watchdog(ctx context.Context) (*Watchdog, error) {
	u, err := s.watchdogURL(ctx)
	if err != nil {
		return nil, err
	}
	s.watchdogMu.Lock()
	defer s.watchdogMu.Unlock()
	st := s.watchdogLast
	return &Watchdog{URL: u, LastPingAt: st.at, LastStatus: st.status, LastError: st.err, Held: st.held}, nil
}

// WatchdogStatus is the watchdog's URL and its last ping. Operators only.
func (s *Service) WatchdogStatus(ctx context.Context, actorID string) (*Watchdog, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.watchdog(ctx)
}

// ValidateWatchdogURL accepts an absolute https:// URL that isn't on a
// literal private address; a hostname is checked when the ping connects.
func ValidateWatchdogURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || len(raw) > 2048 {
		return ErrWatchdogURL
	}
	if err := netcheck.CheckHost(u.Hostname()); err != nil {
		return fmt.Errorf("the ping URL must be on a public address: %w", netcheck.ErrPrivateAddress)
	}
	return nil
}

// SetWatchdog sets the ping URL, or clears it with "", and pings it at once
// so the page shows whether it answers. Operators only.
func (s *Service) SetWatchdog(ctx context.Context, actorID, raw string) (*Watchdog, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if err := ValidateWatchdogURL(raw); err != nil {
			return nil, err
		}
	}
	b, _ := json.Marshal(map[string]string{"url": raw})
	if err := s.repo.MergeSetting(ctx, SettingWatchdog, b, actorID); err != nil {
		return nil, fmt.Errorf("save the watchdog: %w", err)
	}
	s.watchdogMu.Lock()
	s.watchdogLast = watchdogState{}
	s.watchdogMu.Unlock()
	if raw == "" {
		s.audit(ctx, actorID, "panel.watchdog_cleared", "", nil)
		return s.watchdog(ctx)
	}
	u, _ := url.Parse(raw)
	s.audit(ctx, actorID, "panel.watchdog_set", "", map[string]any{"host": u.Hostname()})
	s.ping(ctx, raw, time.Now())
	return s.watchdog(ctx)
}

// TestWatchdog pings the watchdog now, whatever the panel's health.
// Operators only.
func (s *Service) TestWatchdog(ctx context.Context, actorID string) (*Watchdog, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	raw, err := s.watchdogURL(ctx)
	if err != nil {
		return nil, err
	}
	if raw != "" {
		s.ping(ctx, raw, time.Now())
	}
	return s.watchdog(ctx)
}

// watchdogTick pings every watchdogEvery while the panel is healthy, and
// holds the ping back while it isn't, so the watchdog alerts.
func (s *Service) watchdogTick(ctx context.Context, now time.Time) {
	s.watchdogMu.Lock()
	due := now.Sub(s.watchdogLast.tried) >= watchdogEvery
	s.watchdogMu.Unlock()
	if !due {
		return
	}
	raw, err := s.watchdogURL(ctx)
	if err != nil || raw == "" {
		// No database, no ping: the watchdog notices that too.
		s.watchdogMu.Lock()
		s.watchdogLast.tried = now
		s.watchdogMu.Unlock()
		return
	}
	if containers, _, _, err := s.PanelProblems(ctx, false); err != nil || containers != "" {
		s.watchdogMu.Lock()
		s.watchdogLast.tried, s.watchdogLast.held = now, true
		s.watchdogMu.Unlock()
		return
	}
	s.ping(ctx, raw, now)
}

func (s *Service) ping(ctx context.Context, raw string, now time.Time) {
	c := s.watchdogClient
	if c == nil {
		c = netcheck.NewPublicOnlyClient(10*time.Second, false)
	}
	st := watchdogState{tried: now}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err == nil {
		req.Header.Set("User-Agent", "Toft/"+buildinfo.Version+" watchdog")
		var res *http.Response
		if res, err = c.Do(req); err == nil {
			res.Body.Close()
			code := res.StatusCode
			st.status = &code
			if code >= 300 {
				err = fmt.Errorf("the URL answered %d", code)
			}
		}
	}
	at := now.UTC()
	st.at = &at
	if err != nil {
		st.err = err.Error()
		s.log.Info("watchdog ping failed", zap.Error(err))
	}
	s.watchdogMu.Lock()
	s.watchdogLast = st
	s.watchdogMu.Unlock()
}
