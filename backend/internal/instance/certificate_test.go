package instance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"go.uber.org/zap"
)

// Traefik's log lines as Docker gives them (--log.format=json), from the
// 2026-11-11 restore drill and Let's Encrypt's documented rate limit answer.
func acmeLine(at, domain, errText string) string {
	b, _ := json.Marshal(map[string]any{
		"level": "error", "providerName": "letsencrypt.acme", "routerName": "toft-panel@file",
		"domains": []string{domain}, "error": errText, "time": at, "message": "Unable to obtain ACME certificate for domains",
	})
	return at + " " + string(b)
}

const (
	unauthorizedErr = "unable to generate a certificate for the domains [panel.example]: error: one or more domains had a problem:\n" +
		"[panel.example] invalid authorization: acme: error: 403 :: urn:ietf:params:acme:error:unauthorized :: 192.0.2.30: Invalid response from http://panel.example/.well-known/acme-challenge/x1: 404\n"
	connectionErr = "unable to generate a certificate for the domains [panel.example]: error: one or more domains had a problem:\n" +
		"[panel.example] invalid authorization: acme: error: 400 :: urn:ietf:params:acme:error:connection :: 198.51.100.20: Fetching http://panel.example/.well-known/acme-challenge/x2: Timeout during connect (likely firewall problem)\n"
	rateLimitedErr = "unable to generate a certificate for the domains [panel.example]: acme: error: 429 :: POST :: https://acme-v02.api.letsencrypt.org/acme/new-order :: " +
		"urn:ietf:params:acme:error:rateLimited :: too many failed authorizations (5) for \"panel.example\" in the last 1h0m0s, retry after 2026-11-11 14:00:00 UTC: see https://letsencrypt.org/docs/rate-limits/"
)

func TestAcmeFailure(t *testing.T) {
	at := "2026-11-11T12:34:46.123456789Z"
	when := time.Date(2026, 11, 11, 12, 34, 46, 123456789, time.UTC)
	cases := []struct {
		name, line, typ, detail string
		retry                   *time.Time
	}{
		{"unauthorized", acmeLine(at, "panel.example", unauthorizedErr), "unauthorized", "192.0.2.30: Invalid response from http://panel.example/.well-known/acme-challenge/x1: 404", nil},
		{"connection", acmeLine(at, "panel.example", connectionErr), "connection", "198.51.100.20: Fetching http://panel.example/.well-known/acme-challenge/x2: Timeout during connect (likely firewall problem)", nil},
		{"rate limited, until its time", acmeLine(at, "panel.example", rateLimitedErr), "rate_limited", "", ptr(time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC))},
		{"rate limited, no time: an hour", acmeLine(at, "panel.example", "acme: error: 429 :: urn:ietf:params:acme:error:rateLimited :: too many certificates"), "rate_limited", "too many certificates", ptr(when.Add(time.Hour))},
		{"no urn", acmeLine(at, "panel.example", "context deadline exceeded"), "other", "context deadline exceeded", nil},
		// The first URN is the problem: a later one is the page that answered,
		// or a sub-problem.
		{"a page that names a rate limit", acmeLine(at, "panel.example", strings.Replace(unauthorizedErr, ": 404", `: 200: "urn:ietf:params:acme:error:rateLimited :: retry after 2099-01-01 00:00:00 UTC"`, 1)), "unauthorized", "", nil},
		{"a sub-problem", acmeLine(at, "panel.example", `acme: error: 400 :: urn:ietf:params:acme:error:dns :: no valid A records; problem: "urn:ietf:params:acme:error:dns" :: NXDOMAIN`), "dns", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := instance.AcmeFailure(c.line, "Panel.Example")
			if e == nil || e.Type != c.typ || !e.At.Equal(when) {
				t.Fatalf("got %+v, want %s at %s", e, c.typ, when)
			}
			if c.detail != "" && e.Detail != c.detail {
				t.Errorf("detail = %q, want %q", e.Detail, c.detail)
			}
			if (c.retry == nil) != (e.RetryAfter == nil) || (c.retry != nil && !e.RetryAfter.Equal(*c.retry)) {
				t.Errorf("retry after = %v, want %v", e.RetryAfter, c.retry)
			}
		})
	}
	for name, line := range map[string]string{
		"another domain": acmeLine(at, "shop.example", unauthorizedErr),
		"an access log":  at + ` {"ClientHost":"203.0.113.9","RequestPath":"/","message":"Unable to obtain ACME certificate"}`,
		// Traefik's access log is the common text format; a client chooses
		// its User-Agent.
		"a forged access log line": at + ` 203.0.113.9 - - [11/Nov/2026:12:34:46 +0000] "GET / HTTP/1.1" 404 19 "-" "` +
			strings.TrimPrefix(acmeLine(at, "panel.example", rateLimitedErr), at+" ") + `" 1 "-" "-" 0ms`,
		"not JSON": at + " Unable to obtain ACME certificate for domains",
	} {
		if e := instance.AcmeFailure(line, "panel.example"); e != nil {
			t.Errorf("%s: got %+v, want nothing", name, e)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// certPanel is a panel never restored whose domain serves Traefik's own
// certificate, its Traefik started at traefikStart.
func certPanel(t *testing.T, repo *fakeRepo, traefikStart time.Time) (*instance.Service, *fakeProbe, *probeSpy, *routesSpy) {
	t.Helper()
	if repo.settings == nil {
		repo.settings = map[string][]byte{}
	}
	repo.settings[instance.SettingPanel] = []byte(`{"domain":"panel.example"}`)
	probe := &fakeProbe{
		cert:       &instance.PanelCertificate{Domain: "panel.example", Issuer: "TRAEFIK DEFAULT CERT", NotAfter: traefikStart.Add(365 * 24 * time.Hour)},
		containers: []instance.PanelContainer{{Name: "paas-traefik", State: "running", StartedAt: &traefikStart}},
	}
	prober := &probeSpy{certPending: true}
	spy := &routesSpy{}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false).WithSetup(prober, "").
		WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())
	if err := svc.SyncPanelRoutes(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc, probe, prober, spy
}

func certTick(svc *instance.Service, now time.Time) {
	svc.PanelContainersTick(context.Background(), now)
	svc.CertificateTick(context.Background(), now)
}

func lastAsk(spy *routesSpy) int64 { return spy.got[len(spy.got)-1].CertificateAsk }

func TestAPanelWithoutItsCertificateHasTraefikAskAgain(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)

	t.Run("never restored: asked once Traefik's own try is a quarter hour old", func(t *testing.T) {
		repo := &fakeRepo{}
		svc, _, prober, spy := certPanel(t, repo, start.Add(-2*time.Minute))
		certTick(svc, start)
		if prober.calls != 1 || len(spy.got) != 1 {
			t.Fatalf("probes = %d, routes = %+v: Traefik's start is its try, so no ask on top of it", prober.calls, spy.got)
		}
		_, problem, _, err := svc.PanelProblems(ctx, true)
		if err != nil || !strings.Contains(problem, "Traefik's self-signed certificate. Let's Encrypt hasn't issued one yet; Toft has Traefik ask again.") {
			t.Fatalf("problem = %q, %v", problem, err)
		}
		certTick(svc, start.Add(13*time.Minute))
		if len(spy.got) != 2 || lastAsk(spy) != 1 || spy.got[1].AnyHost {
			t.Fatalf("routes = %+v: want an ask 15 minutes after Traefik started, the domain only", spy.got)
		}

		// A backend restart reads the asks back: the same routes, no ask
		// before the wait is over.
		spy2 := &routesSpy{}
		svc2 := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy2, "", false).WithSetup(prober, "").
			WithPanelMonitor(&fakeProbe{cert: &instance.PanelCertificate{Domain: "panel.example", Issuer: "TRAEFIK DEFAULT CERT"},
				containers: []instance.PanelContainer{{Name: "paas-traefik", State: "running", StartedAt: ptr(start.Add(-2 * time.Minute))}}}, &fakeStore{}, t.TempDir(), t.TempDir())
		if err := svc2.SyncPanelRoutes(ctx); err != nil || lastAsk(spy2) != 1 {
			t.Fatalf("routes after a restart = %+v, %v: want the same value", spy2.got, err)
		}
		certTick(svc2, start.Add(14*time.Minute))
		certTick(svc2, start.Add(27*time.Minute))
		if len(spy2.got) != 1 {
			t.Fatalf("routes = %+v: asked again within the quarter hour after a restart", spy2.got)
		}
		certTick(svc2, start.Add(28*time.Minute))
		if len(spy2.got) != 2 || lastAsk(spy2) != 2 {
			t.Fatalf("routes = %+v: want the second ask a quarter hour after the first", spy2.got)
		}
	})

	t.Run("a restart of Traefik is a try", func(t *testing.T) {
		svc, probe, _, spy := certPanel(t, &fakeRepo{}, start.Add(-time.Hour))
		certTick(svc, start)
		if lastAsk(spy) != 1 {
			t.Fatalf("routes = %+v", spy.got)
		}
		probe.containers[0].StartedAt = ptr(start.Add(10 * time.Minute))
		certTick(svc, start.Add(16*time.Minute))
		if len(spy.got) != 2 {
			t.Fatalf("routes = %+v: Traefik asked at its start 6 minutes ago", spy.got)
		}
	})

	t.Run("a new domain starts over", func(t *testing.T) {
		repo := &fakeRepo{settings: map[string][]byte{instance.SettingCertificateAsk: []byte(
			`{"domain":"old.example","since":"2026-11-01T00:00:00Z","n":4,"asked_at":"2026-11-11T14:59:00Z","wait":86400000000000,"tried_at":"2026-11-11T14:59:00Z","reached":true}`)}}
		svc, _, _, spy := certPanel(t, repo, start.Add(-time.Hour))
		if lastAsk(spy) != 0 {
			t.Fatalf("routes = %+v: another domain's asks are not this one's", spy.got)
		}
		certTick(svc, start)
		if lastAsk(spy) != 5 {
			t.Fatalf("routes = %+v: want an ask, the value still a change", spy.got)
		}
	})

	t.Run("while Let's Encrypt has rate-limited the domain, nothing is asked", func(t *testing.T) {
		// From the clock: the Problem says "until" only while the limit's end
		// is still ahead of the real time.
		start := time.Now().UTC().Truncate(time.Minute)
		until := start.Add(time.Hour)
		svc, probe, _, spy := certPanel(t, &fakeRepo{}, start.Add(-time.Hour))
		probe.logs = []string{acmeLine(start.Add(-10*time.Minute).Format(time.RFC3339), "panel.example",
			strings.Replace(rateLimitedErr, "2026-11-11 14:00:00", until.Format(time.DateTime), 1))}
		certTick(svc, start)
		certTick(svc, start.Add(59*time.Minute))
		if len(spy.got) != 1 {
			t.Fatalf("routes = %+v: asked during a rate limit", spy.got)
		}
		_, problem, _, _ := svc.PanelProblems(ctx, true)
		if !strings.Contains(problem, "until "+until.Format("2006-01-02 15:04")+" UTC") {
			t.Fatalf("problem = %q: want the limit's end", problem)
		}
		certTick(svc, start.Add(60*time.Minute))
		if lastAsk(spy) != 1 {
			t.Fatalf("routes = %+v: want an ask once the limit is over", spy.got)
		}
	})

	t.Run("the reason from Traefik's log, and the certificate issued at last", func(t *testing.T) {
		repo := &fakeRepo{}
		svc, probe, prober, spy := certPanel(t, repo, start.Add(-time.Hour))
		probe.logs = []string{acmeLine("2026-11-11T14:00:30Z", "panel.example", connectionErr)}
		certTick(svc, start)
		_, problem, _, _ := svc.PanelProblems(ctx, true)
		if !strings.Contains(problem, "Port 80 doesn't reach this server from the internet") || !strings.Contains(problem, "Hetzner Cloud Firewall") {
			t.Fatalf("problem = %q", problem)
		}
		if len(probe.logsSince) != 1 || !probe.logsSince[0].Equal(start.Add(-time.Hour)) {
			t.Fatalf("log read from %v, want Traefik's start", probe.logsSince)
		}
		certTick(svc, start.Add(time.Minute))
		if len(probe.logsSince) != 2 || !probe.logsSince[1].Equal(start) {
			t.Fatalf("log read from %v, want where the last read stopped", probe.logsSince)
		}

		probe.cert = &instance.PanelCertificate{Domain: "panel.example", Issuer: "Let's Encrypt", NotAfter: time.Now().Add(90 * 24 * time.Hour)}
		svc.AgePanelCertificate(time.Hour)
		calls := prober.calls
		certTick(svc, start.Add(2*time.Minute))
		if _, ok := repo.settings[instance.SettingCertificateAsk]; ok || lastAsk(spy) != 0 || prober.calls != calls {
			t.Fatalf("asks kept = %v, routes = %+v, probes = %d: want them forgotten and the middleware gone", ok, spy.got, prober.calls-calls)
		}
		if _, problem, _, _ := svc.PanelProblems(ctx, true); problem != "" {
			t.Fatalf("problem = %q", problem)
		}
	})

	t.Run("a failure in the log is a try", func(t *testing.T) {
		svc, probe, _, spy := certPanel(t, &fakeRepo{}, start.Add(-time.Hour))
		probe.logs = []string{acmeLine("2026-11-11T14:55:00Z", "panel.example", connectionErr)}
		certTick(svc, start)
		if len(spy.got) != 1 {
			t.Fatalf("routes = %+v: Traefik tried 5 minutes ago", spy.got)
		}
		certTick(svc, start.Add(10*time.Minute))
		if lastAsk(spy) != 1 {
			t.Fatalf("routes = %+v: want an ask a quarter hour after Traefik's try", spy.got)
		}
	})

	t.Run("a probe that flips doesn't shorten the wait", func(t *testing.T) {
		svc, probe, prober, spy := certPanel(t, &fakeRepo{}, start.Add(-time.Hour))
		certTick(svc, start)
		if lastAsk(spy) != 1 {
			t.Fatalf("routes = %+v", spy.got)
		}
		// Toft's ask fails 10 seconds later, while the probe misses.
		probe.logs = []string{acmeLine("2026-11-11T15:00:10Z", "panel.example", connectionErr)}
		prober.unreachable = true
		certTick(svc, start.Add(time.Minute))
		prober.unreachable = false
		certTick(svc, start.Add(2*time.Minute))
		certTick(svc, start.Add(15*time.Minute))
		if len(spy.got) != 2 {
			t.Fatalf("routes = %+v: the domain reached the ask that failed, so the wait is a quarter hour", spy.got)
		}
		certTick(svc, start.Add(16*time.Minute))
		if lastAsk(spy) != 2 {
			t.Fatalf("routes = %+v: want the second ask", spy.got)
		}
	})

	t.Run("a log read cut short goes on from where it stopped", func(t *testing.T) {
		svc, probe, _, _ := certPanel(t, &fakeRepo{}, start.Add(-time.Hour))
		probe.logs = []string{"2026-11-11T14:20:00.5Z some line", "2026-11-11T14:30:00.25Z another"}
		probe.logsErr = context.DeadlineExceeded
		certTick(svc, start)
		certTick(svc, start.Add(time.Minute))
		if len(probe.logsSince) != 2 || !probe.logsSince[1].Equal(time.Date(2026, 11, 11, 14, 30, 0, 250000000, time.UTC)) {
			t.Fatalf("log read from %v, want the last line read", probe.logsSince)
		}
	})

	t.Run("without routes of its own, no ask is recorded", func(t *testing.T) {
		repo := &fakeRepo{settings: map[string][]byte{instance.SettingPanel: []byte(`{"domain":"panel.example"}`)}}
		svc := instance.NewService(repo, zap.NewNop()).WithSetup(&probeSpy{certPending: true}, "").
			WithPanelMonitor(&fakeProbe{cert: &instance.PanelCertificate{Domain: "panel.example", Issuer: "TRAEFIK DEFAULT CERT"},
				containers: []instance.PanelContainer{{Name: "paas-traefik", State: "running", StartedAt: ptr(start.Add(-time.Hour))}}}, &fakeStore{}, t.TempDir(), t.TempDir())
		if err := svc.SyncPanelRoutes(ctx); err != nil {
			t.Fatal(err)
		}
		certTick(svc, start)
		if raw := repo.settings[instance.SettingCertificateAsk]; !strings.Contains(string(raw), `"domain":"panel.example"`) {
			t.Fatalf("asks = %s: want the domain's state kept", raw)
		}
		if raw := repo.settings[instance.SettingCertificateAsk]; strings.Contains(string(raw), `"asked_at":"`) {
			t.Fatalf("asks = %s: nothing could ask", raw)
		}
	})

	t.Run("the domain doesn't reach this panel: no ask, and the Problem says where it points", func(t *testing.T) {
		svc, _, prober, spy := certPanel(t, &fakeRepo{}, start.Add(-time.Hour))
		prober.unreachable = true
		certTick(svc, start)
		if len(spy.got) != 1 {
			t.Fatalf("routes = %+v", spy.got)
		}
		_, problem, _, _ := svc.PanelProblems(ctx, true)
		if !strings.Contains(problem, "has no DNS record") {
			t.Fatalf("problem = %q", problem)
		}
	})
}

// A restore brings the old server's asks with its database: they were the
// old Traefik's, so the restored panel starts over. Its Traefik tried while
// the domain still pointed at the old server, so once the domain arrives it
// asks a minute after that try, not a quarter hour.
func TestARestoredPanelStartsItsAsksOver(t *testing.T) {
	start := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	repo := &fakeRepo{settings: map[string][]byte{
		instance.SettingRestore: []byte(`{"at":"2026-11-11T14:02:00Z"}`),
		instance.SettingCertificateAsk: []byte(`{"domain":"panel.example","since":"2026-11-01T00:00:00Z","n":7,"asked_at":"2026-11-11T13:00:00Z","wait":86400000000000,` +
			`"tried_at":"2026-11-11T13:00:00Z","reached":true,"error":{"type":"rate_limited","detail":"","at":"2026-11-11T13:00:00Z","retry_after":"2026-11-12T13:00:00Z"}}`),
	}}
	svc, _, prober, spy := certPanel(t, repo, start.Add(-2*time.Minute))
	if !spy.got[0].AnyHost {
		t.Fatalf("routes = %+v: a restored panel waits for its domain", spy.got)
	}
	prober.unreachable = true
	certTick(svc, start)
	prober.unreachable = false
	certTick(svc, start.Add(time.Minute))
	if len(spy.got) != 2 || lastAsk(spy) != 8 || !spy.got[1].AnyHost {
		t.Fatalf("routes = %+v: want an ask once the domain arrived, the old server's rate limit not this one's", spy.got)
	}
}
