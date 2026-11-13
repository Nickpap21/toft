package instance

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

// The panel's certificate, while Let's Encrypt hasn't issued it.
//
// Traefik asks Let's Encrypt when its configuration changes (its start, a
// route written) and not again after a failure. A panel whose port 80 was
// opened, or whose domain arrived, after Traefik's last try never gets its
// certificate by itself. So every minute, while the domain serves Traefik's
// own certificate (or a restored panel waits for its domain), the panel
// probes the domain, reads Let's Encrypt's answers in Traefik's log, and,
// while the domain reaches it, has Traefik ask again by writing its routes
// with an unused middleware whose value changes (toft-certificate-ask).

// SettingCertificateAsk is the instance_settings key holding certificateAsk.
// It is there only while the panel's domain has no trusted certificate, so
// a restart neither asks again nor forgets when it last asked.
const SettingCertificateAsk = "certificate_ask"

// certificateTickEvery is how often the panel looks at its domain.
const certificateTickEvery = time.Minute

// The first ask comes certificateAskEvery after Traefik's last try, the wait
// doubling after each ask up to certificateAskMax. An ask fails while port
// 80 doesn't reach this server, and Let's Encrypt counts failures per host
// name: 5 an hour, one given back every 12 minutes, and 1,152 in a row, one
// given back a day, after which it refuses the name until someone unpauses
// it. A day apart, these asks never run out either. A try made while the
// domain didn't reach this panel says nothing of the next: once the domain
// arrives, Toft asks certificateAskArrived after it.
const (
	certificateAskEvery   = 15 * time.Minute
	certificateAskMax     = 24 * time.Hour
	certificateAskArrived = time.Minute
	// rateLimitedFor is a rate limit's end when Let's Encrypt gave none.
	rateLimitedFor = time.Hour
)

// traefikContainer is the panel's Traefik (panelContainers).
const traefikContainer = "paas-traefik"

// PanelCertificateAsk is why the panel's domain has no trusted certificate
// yet, and when Toft has Traefik ask for one again.
type PanelCertificateAsk struct {
	ReachesPanel bool       `json:"reaches_panel"`
	Addresses    []string   `json:"addresses"`
	LastTryAt    *time.Time `json:"last_try_at"`
	// NextAskAt is nil while the domain doesn't reach this panel.
	NextAskAt *time.Time           `json:"next_ask_at"`
	Error     *CertificateAskError `json:"error"`
}

// CertificateAskError is Let's Encrypt's answer to a try that failed, as
// Traefik logged it.
type CertificateAskError struct {
	// Type is connection, unauthorized, dns, caa, rate_limited or other.
	Type   string    `json:"type"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
	// RetryAfter, with rate_limited, is when Let's Encrypt takes the domain
	// again.
	RetryAfter *time.Time `json:"retry_after"`
}

// certificateAsk is the JSON at instance_settings['certificate_ask']. Every
// field is written each time, so the merge replaces it whole.
type certificateAsk struct {
	Domain string `json:"domain"`
	// Since is when the panel first saw Domain without a trusted certificate.
	Since time.Time `json:"since"`
	// N is the toft-certificate-ask middleware's value, raised by each ask.
	N       int64      `json:"n"`
	AskedAt *time.Time `json:"asked_at"`
	// Wait, after the last try, before the next ask.
	Wait time.Duration `json:"wait"`
	// TriedAt is the last try Toft knows of (lastTry), and Reached whether
	// the domain reached this panel then.
	TriedAt *time.Time           `json:"tried_at"`
	Reached bool                 `json:"reached"`
	Error   *CertificateAskError `json:"error"`
	// LogAt: Traefik's log is read from here.
	LogAt time.Time `json:"log_at"`
}

// lastTry is the last time Traefik asked that Toft knows of: Toft's own ask,
// Traefik's start, or a failure in its log. Zero when it knows of none.
func (a certificateAsk) lastTry(traefikStart *time.Time) time.Time {
	var t time.Time
	for _, at := range []*time.Time{a.AskedAt, traefikStart} {
		if at != nil && at.After(t) {
			t = *at
		}
	}
	if a.Error != nil && a.Error.At.After(t) {
		t = a.Error.At
	}
	return t
}

// nextAsk is when Toft may have Traefik ask again, given the last try.
// Never during a rate limit: an ask then extends it.
func (a certificateAsk) nextAsk(last time.Time) time.Time {
	at := last
	switch {
	case last.IsZero():
	case !a.Reached:
		at = last.Add(certificateAskArrived)
	default:
		at = last.Add(max(a.Wait, certificateAskEvery))
	}
	if a.Error != nil && a.Error.RetryAfter != nil && a.Error.RetryAfter.After(at) {
		at = *a.Error.RetryAfter
	}
	return at
}

func traefikCert(c *PanelCertificate) bool {
	return strings.Contains(strings.ToLower(c.Issuer), "traefik")
}

// certificateTick is the panel monitor's, every tick; it acts at most every
// certificateTickEvery. On a panel with a trusted certificate it only reads
// that certificate, which is cached. On a restored panel waiting for its
// domain (D-163) it also ends the answer on the IP address once the domain
// reaches this panel with a trusted certificate: only that lets in a browser
// that has seen the real panel, whose HSTS refuses Traefik's own.
func (s *Service) certificateTick(ctx context.Context, now time.Time) {
	if s.prober == nil || s.panel == nil || now.Sub(s.domainTriedAt) < certificateTickEvery {
		return
	}
	s.domainTriedAt = now
	domain := s.PanelDomain()
	if domain == "" {
		return
	}
	awaiting := s.awaitingDomain.Load()
	cert := s.panelCertificate(ctx)
	if !awaiting && (cert == nil || !traefikCert(cert)) {
		if cert != nil {
			s.dropCertificateAsk(ctx)
		}
		return
	}
	// One tick's time, so the monitor loses no reading while the domain
	// points at a server that is gone.
	pctx, cancel := context.WithTimeout(ctx, panelTick)
	check, err := s.prober.ProbeDomain(pctx, domain, s.probeID, true)
	cancel()
	if err != nil {
		return
	}
	if check.ReachesPanel && check.Certificate.State == CertIssued {
		if awaiting {
			s.domainArrived(ctx, now, domain)
		}
		return
	}
	s.askCertificate(ctx, now, domain, check)
}

// domainArrived: the restored panel's domain reached it with a trusted
// certificate, so the mark says so and the routes are written again.
func (s *Service) domainArrived(ctx context.Context, now time.Time, domain string) {
	if err := s.markDomainReached(ctx, "", now); err != nil {
		s.log.Warn("the restored panel's domain arrived, and recording it failed", zap.Error(err))
		return
	}
	s.dropCertificateAsk(ctx)
	s.log.Info("the restored panel's domain reached it: the panel no longer answers on its IP address", zap.String("domain", domain))
}

// dropCertificateAsk forgets the asks once the certificate is trusted, and
// writes the routes without the middleware.
func (s *Service) dropCertificateAsk(ctx context.Context) {
	if s.certificateAskNone && !s.awaitingDomain.Load() {
		return
	}
	s.setCertificateAskView("", nil)
	if err := s.repo.DeleteSetting(ctx, SettingCertificateAsk); err != nil {
		s.log.Warn("the panel's certificate asks not forgotten", zap.Error(err))
		return
	}
	if err := s.SyncPanelRoutes(ctx); err != nil {
		s.log.Warn("panel routes not rewritten after its certificate was issued", zap.Error(err))
		return
	}
	s.certificateAskNone = true
}

// askCertificate brings the asks up to date with Traefik's log and its
// start, and has Traefik ask again once the domain reaches this panel and
// the wait is over.
func (s *Service) askCertificate(ctx context.Context, now time.Time, domain string, check PanelDomainCheck) {
	now = now.UTC()
	var st certificateAsk
	if _, err := s.readJSON(ctx, SettingCertificateAsk, &st); err != nil {
		s.log.Warn("the panel's certificate asks not read", zap.Error(err))
		return
	}
	saved, _ := json.Marshal(st)
	restored, err := s.RestoredAt(ctx)
	if err != nil {
		s.log.Warn("the panel's certificate asks not checked", zap.Error(err))
		return
	}
	start := s.traefikStartedAt()
	// A new domain starts over, and so does a restore: the asks it brought
	// were the old server's Traefik's. N only grows, so the next value is
	// still a change.
	if st.Domain != domain || (restored != nil && st.Since.Before(*restored)) {
		st = certificateAsk{Domain: domain, Since: now, N: st.N, LogAt: now.Add(-24 * time.Hour)}
		if start != nil && start.After(st.LogAt) {
			st.LogAt = *start
		}
	}
	s.readCertificateLog(ctx, &st, now)
	if last := st.lastTry(start); !last.IsZero() && (st.TriedAt == nil || !st.TriedAt.Equal(last)) {
		// The failure of Toft's own ask was a try the domain reached, whatever
		// the probe says now: a probe that flips doesn't shorten the wait.
		ours := st.AskedAt != nil && !last.Before(*st.AskedAt) && last.Sub(*st.AskedAt) < certificateAskEvery
		st.TriedAt, st.Reached = &last, check.ReachesPanel || ours
	}
	s.certificateAskNone = false
	// Without routes of its own to write, Toft can't make Traefik ask.
	if s.routes != nil && check.ReachesPanel && !now.Before(st.nextAsk(st.lastTry(start))) {
		asked := st
		asked.N++
		asked.AskedAt, asked.TriedAt, asked.Reached = &now, &now, true
		asked.Wait = min(max(2*st.Wait, certificateAskEvery), certificateAskMax)
		// Saved first: SyncPanelRoutes writes the value it reads.
		err := s.saveCertificateAsk(ctx, asked)
		if err == nil {
			if err = s.SyncPanelRoutes(ctx); err != nil {
				// Not asked: the state says so again, and the next probe tries.
				if rerr := s.saveCertificateAsk(ctx, st); rerr != nil {
					s.log.Warn("the panel's certificate ask not taken back", zap.Error(rerr))
				}
			}
		}
		if err != nil {
			s.log.Warn("the panel's certificate not asked for again", zap.String("domain", domain), zap.Error(err))
		} else {
			st = asked
			s.log.Info("the panel's domain reaches it without a trusted certificate: Traefik asks for one again", zap.String("domain", domain))
		}
	} else if b, _ := json.Marshal(st); !bytes.Equal(b, saved) {
		if err := s.saveCertificateAsk(ctx, st); err != nil {
			s.log.Warn("the panel's certificate asks not saved", zap.Error(err))
		}
	}
	view := &PanelCertificateAsk{ReachesPanel: check.ReachesPanel, Addresses: check.Addresses, LastTryAt: st.TriedAt, Error: st.Error}
	if view.Addresses == nil {
		view.Addresses = []string{}
	}
	if check.ReachesPanel {
		next := st.nextAsk(st.lastTry(start))
		view.NextAskAt = &next
	}
	s.setCertificateAskView(domain, view)
}

func (s *Service) saveCertificateAsk(ctx context.Context, st certificateAsk) error {
	return s.writeJSON(ctx, "", SettingCertificateAsk, st)
}

// certificateAskN is the toft-certificate-ask middleware's value for
// domain: 0, no middleware, without asks.
func (s *Service) certificateAskN(ctx context.Context, domain string) (int64, error) {
	var st certificateAsk
	if _, err := s.readJSON(ctx, SettingCertificateAsk, &st); err != nil {
		return 0, err
	}
	if st.Domain != domain {
		return 0, nil
	}
	return st.N, nil
}

func (s *Service) setCertificateAskView(domain string, a *PanelCertificateAsk) {
	s.panel.mu.Lock()
	s.panel.ask, s.panel.askDomain = a, domain
	s.panel.mu.Unlock()
}

// traefikStartedAt is when the panel's Traefik started, from the monitor's
// last reading of its containers; nil when it isn't known.
func (s *Service) traefikStartedAt() *time.Time {
	s.panel.mu.Lock()
	defer s.panel.mu.Unlock()
	for _, c := range s.panel.containers {
		if c.Name == traefikContainer && c.State == "running" {
			return c.StartedAt
		}
	}
	return nil
}

// readCertificateLog reads Traefik's log from st.LogAt for Let's Encrypt's
// last answer about st.Domain. Traefik logs JSON (docker-compose.yml); its
// access log, in the common text format, shares the stream.
func (s *Service) readCertificateLog(ctx context.Context, st *certificateAsk, now time.Time) {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var last string
	err := s.panel.probe.LogsSince(lctx, traefikContainer, st.LogAt, func(line string) {
		last = line
		if e := acmeFailure(line, st.Domain); e != nil && (st.Error == nil || !e.At.Before(st.Error.At)) {
			st.Error = e
		}
	})
	if err == nil {
		st.LogAt = now
		return
	}
	// A long log read in part goes on from where it stopped, not over again.
	ts, _, _ := strings.Cut(last, " ")
	if at, perr := time.Parse(time.RFC3339Nano, ts); perr == nil && at.After(st.LogAt) {
		st.LogAt = at
	}
	s.log.Warn("Traefik's log not read to the end", zap.Time("from", st.LogAt), zap.Error(err))
}

const acmeURN = "urn:ietf:params:acme:error:"

var retryAfter = regexp.MustCompile(`retry after (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC`)

// acmeFailure reads one line of Traefik's log, led by Docker's timestamp:
// nil unless it is Traefik failing to get domain's certificate. The error
// names each domain's problem as "[domain] … urn:…:<type> :: <detail>";
// an order refused as a whole (a rate limit) has no such prefix.
func acmeFailure(line, domain string) *CertificateAskError {
	i := strings.IndexByte(line, '{')
	if i < 0 || !strings.Contains(line, "Unable to obtain ACME certificate") {
		return nil
	}
	var l struct {
		Message string   `json:"message"`
		Error   string   `json:"error"`
		Domains []string `json:"domains"`
	}
	if json.Unmarshal([]byte(line[i:]), &l) != nil || !strings.HasPrefix(l.Message, "Unable to obtain ACME certificate") ||
		!slices.ContainsFunc(l.Domains, func(d string) bool { return strings.EqualFold(d, domain) }) {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(line[:i]))
	if err != nil {
		return nil
	}
	text := l.Error
	if j := strings.Index(strings.ToLower(text), "["+strings.ToLower(domain)+"] "); j >= 0 {
		text = text[j:]
		if k := strings.IndexByte(text, '\n'); k >= 0 {
			text = text[:k]
		}
	}
	e := &CertificateAskError{Type: "other", At: at.UTC()}
	// The first: a later one is a sub-problem, or the page that answered.
	if j := strings.Index(text, acmeURN); j >= 0 {
		urn, detail, _ := strings.Cut(text[j+len(acmeURN):], " :: ")
		switch urn = strings.Trim(urn, ` "`); urn {
		case "connection", "unauthorized", "dns", "caa":
			e.Type = urn
		case "rateLimited":
			e.Type = "rate_limited"
		}
		text = detail
	}
	if k := strings.IndexByte(text, '\n'); k >= 0 {
		text = text[:k]
	}
	e.Detail = clip(strings.TrimSpace(text), 300)
	if e.Type == "rate_limited" {
		until := e.At.Add(rateLimitedFor)
		if m := retryAfter.FindStringSubmatch(text); m != nil {
			if t, err := time.Parse(time.DateTime, m[1]); err == nil {
				until = t.UTC()
			}
		}
		e.RetryAfter = &until
	}
	return e
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// certificateAskProblem says why domain's certificate is still Traefik's own.
func certificateAskProblem(domain string, a *PanelCertificateAsk, now time.Time) string {
	switch {
	case a == nil:
		return "Let's Encrypt hasn't issued one yet."
	case !a.ReachesPanel && len(a.Addresses) == 0:
		return domain + " has no DNS record, so Let's Encrypt can't check it."
	case !a.ReachesPanel:
		return domain + " points at " + strings.Join(a.Addresses, ", ") + ", which doesn't lead to this panel: another server, or a router that won't send this server's own requests back in."
	case a.Error == nil:
		return "Let's Encrypt hasn't issued one yet; Toft has Traefik ask again."
	}
	e := a.Error
	switch e.Type {
	case "rate_limited":
		limit, _, _ := strings.Cut(e.Detail, ", retry after")
		if limit != "" {
			limit = " (" + limit + ")"
		}
		if e.RetryAfter != nil && e.RetryAfter.After(now) {
			return "Let's Encrypt has rate-limited " + domain + " until " + e.RetryAfter.UTC().Format("2006-01-02 15:04") + " UTC" + limit + "; Toft asks again then."
		}
		return "Let's Encrypt rate-limited " + domain + limit + "; Toft asks again."
	case "connection":
		return "Port 80 doesn't reach this server from the internet: check the server's firewall, your provider's firewall (such as Hetzner Cloud Firewall), or a router in front."
	case "unauthorized":
		return "Something other than this panel answered Let's Encrypt's check on port 80: an IPv6 address pointing elsewhere or a port 80 forward to another machine is the usual cause."
	case "dns":
		return "Let's Encrypt couldn't look " + domain + " up: " + e.Detail
	case "caa":
		return domain + "'s CAA record doesn't allow Let's Encrypt."
	}
	return "Let's Encrypt refused it: " + e.Detail
}
