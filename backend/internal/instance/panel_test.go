package instance_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"go.uber.org/zap"
)

type routesSpy struct {
	got []instance.PanelRoutes
	err error
}

func (s *routesSpy) WritePanelRoutes(r instance.PanelRoutes) error {
	if s.err != nil {
		return s.err
	}
	s.got = append(s.got, r)
	return nil
}

// The value is what install.sh --restore writes: jsonb_build_object('at', now()).
func TestRestoredAt(t *testing.T) {
	repo := &fakeRepo{}
	svc := instance.NewService(repo, zap.NewNop())
	if at, err := svc.RestoredAt(context.Background()); err != nil || at != nil {
		t.Fatalf("never restored: got %v, %v", at, err)
	}
	repo.settings = map[string][]byte{instance.SettingRestore: []byte(`{"at": "2026-11-10T14:02:11.123456+00:00"}`)}
	at, err := svc.RestoredAt(context.Background())
	if err != nil || at == nil || !at.Equal(time.Date(2026, 11, 10, 14, 2, 11, 123456000, time.UTC)) {
		t.Fatalf("restored: got %v, %v", at, err)
	}
}

func TestSyncPanelRoutes(t *testing.T) {
	cases := []struct {
		name    string
		setting []byte
		env     string
		want    instance.PanelRoutes
	}{
		{"BASE_DOMAIN only: api.<domain> stays", nil, "panel.example", instance.PanelRoutes{Domain: "panel.example", LegacyAPIHost: "api.panel.example"}},
		{"wizard's domain wins over BASE_DOMAIN", []byte(`{"domain":"toft.example"}`), "panel.example", instance.PanelRoutes{Domain: "toft.example"}},
		{"the wizard set BASE_DOMAIN's own domain", []byte(`{"domain":"panel.example"}`), "panel.example", instance.PanelRoutes{Domain: "panel.example", LegacyAPIHost: "api.panel.example"}},
		{"no domain yet: the IP", nil, "", instance.PanelRoutes{AnyHost: true}},
		{"kept on the IP in the wizard", []byte(`{"domain":""}`), "panel.example", instance.PanelRoutes{AnyHost: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := &routesSpy{}
			repo := &fakeRepo{}
			if c.setting != nil {
				repo.settings = map[string][]byte{instance.SettingPanel: c.setting}
			}
			svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, c.env, false)
			if got := svc.PanelDomain(); got != c.env {
				t.Fatalf("before sync PanelDomain = %q, want BASE_DOMAIN %q", got, c.env)
			}
			if err := svc.SyncPanelRoutes(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(spy.got) != 1 || spy.got[0] != c.want {
				t.Fatalf("routes = %+v, want %+v", spy.got, c.want)
			}
			if got := svc.PanelDomain(); got != c.want.Domain {
				t.Fatalf("PanelDomain = %q, want %q", got, c.want.Domain)
			}
		})
	}

	// Traefik's dashboard follows the panel's domain, and only with users.
	for _, c := range []struct {
		setting   []byte
		env       string
		dashboard bool
		want      bool
	}{
		{[]byte(`{"domain":"toft.example"}`), "", true, true},
		{[]byte(`{"domain":""}`), "panel.example", true, false},
		{[]byte(`{"domain":"toft.example"}`), "", false, false},
	} {
		spy := &routesSpy{}
		repo := &fakeRepo{settings: map[string][]byte{instance.SettingPanel: c.setting}}
		if err := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, c.env, c.dashboard).SyncPanelRoutes(context.Background()); err != nil {
			t.Fatal(err)
		}
		if spy.got[0].Dashboard != c.want {
			t.Errorf("%s with users %v: Dashboard = %v", c.setting, c.dashboard, spy.got[0].Dashboard)
		}
	}

	// A database error writes nothing: Traefik keeps the last routes.
	spy := &routesSpy{}
	err := instance.NewService(&fakeRepo{settingErr: errors.New("down")}, zap.NewNop()).WithPanelRoutes(spy, "panel.example", false).SyncPanelRoutes(context.Background())
	if err == nil || len(spy.got) != 0 {
		t.Fatalf("err = %v, writes = %d", err, len(spy.got))
	}
}

// A restored panel answers on its IP address too until its domain comes
// back to it with a trusted certificate (D-163); one never restored doesn't.
func TestRestoredPanelAnswersOnItsIPUntilItsDomainArrives(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
	withDomain := instance.PanelRoutes{Domain: "panel.example"}
	onIPToo := instance.PanelRoutes{Domain: "panel.example", AnyHost: true}
	panel := func(restore string, prober *probeSpy) (*instance.Service, *fakeRepo, *routesSpy) {
		t.Helper()
		repo := &fakeRepo{settings: map[string][]byte{instance.SettingPanel: []byte(`{"domain":"panel.example"}`)}}
		if restore != "" {
			repo.settings[instance.SettingRestore] = []byte(restore)
		}
		spy := &routesSpy{}
		svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false).WithSetup(prober, "").
			WithPanelMonitor(&fakeProbe{}, &fakeStore{}, t.TempDir(), t.TempDir())
		if err := svc.SyncPanelRoutes(ctx); err != nil {
			t.Fatal(err)
		}
		return svc, repo, spy
	}

	t.Run("never restored: the domain only, and nothing is probed", func(t *testing.T) {
		prober := &probeSpy{}
		svc, _, spy := panel("", prober)
		svc.CertificateTick(ctx, start)
		if len(spy.got) != 1 || spy.got[0] != withDomain || prober.calls != 0 {
			t.Fatalf("routes = %+v, probes = %d", spy.got, prober.calls)
		}
	})

	t.Run("its domain arrived before: the domain only", func(t *testing.T) {
		prober := &probeSpy{}
		svc, _, spy := panel(`{"at":"2026-11-10T14:02:11Z","domain_reached_at":"2026-11-10T14:30:00Z"}`, prober)
		svc.CertificateTick(ctx, start)
		if len(spy.got) != 1 || spy.got[0] != withDomain || prober.calls != 0 {
			t.Fatalf("routes = %+v, probes = %d", spy.got, prober.calls)
		}
	})

	t.Run("waiting: on the IP too, looking once a minute", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, repo, spy := panel(`{"at":"2026-11-10T14:02:11Z"}`, prober)
		if spy.got[0] != onIPToo {
			t.Fatalf("routes = %+v, want %+v", spy.got[0], onIPToo)
		}
		svc.CertificateTick(ctx, start)
		svc.CertificateTick(ctx, start.Add(5*time.Second))
		if prober.calls != 1 || !prober.withCert || len(spy.got) != 1 {
			t.Fatalf("probes = %d (with the certificate: %v), route writes = %d: want one probe within the minute and no write", prober.calls, prober.withCert, len(spy.got))
		}
		// The domain reaches the panel without a trusted certificate:
		// Traefik's tries ran while it pointed elsewhere, and it asks again
		// only on a configuration change. Still on the IP too.
		prober.unreachable, prober.certPending = false, true
		svc.CertificateTick(ctx, start.Add(time.Minute))
		asked := onIPToo
		asked.CertificateAsk = 1
		if prober.calls != 2 || len(spy.got) != 2 || spy.got[1] != asked {
			t.Fatalf("probes = %d, routes = %+v: want the certificate asked for again", prober.calls, spy.got)
		}
		// Not again within the quarter hour: Let's Encrypt counts failures.
		svc.CertificateTick(ctx, start.Add(15*time.Minute))
		if prober.calls != 3 || len(spy.got) != 2 {
			t.Fatalf("probes = %d, route writes = %d: asked again too soon", prober.calls, len(spy.got))
		}
		svc.CertificateTick(ctx, start.Add(16*time.Minute))
		asked.CertificateAsk = 2
		if len(spy.got) != 3 || spy.got[2] != asked {
			t.Fatalf("routes = %+v: want a second ask a quarter hour after the first", spy.got)
		}
		// Then half an hour: a domain that never validates must not run
		// out Let's Encrypt's failures in a row.
		svc.CertificateTick(ctx, start.Add(45*time.Minute))
		if len(spy.got) != 3 {
			t.Fatalf("routes = %+v: the wait doubles after each ask", spy.got)
		}
		// A write that failed reached no one, so it is tried at the next probe.
		spy.err = errors.New("read-only file system")
		svc.CertificateTick(ctx, start.Add(46*time.Minute))
		spy.err = nil
		svc.CertificateTick(ctx, start.Add(47*time.Minute))
		if len(spy.got) != 4 || !spy.got[3].AnyHost || spy.got[3].CertificateAsk <= 2 {
			t.Fatalf("routes = %+v: want the third ask a minute after the failed write", spy.got)
		}

		prober.certPending = false
		arrived := start.Add(48 * time.Minute)
		svc.CertificateTick(ctx, arrived)
		if len(spy.got) != 5 || spy.got[4] != withDomain {
			t.Fatalf("routes = %+v, want the domain only, and no ask, once it arrived", spy.got)
		}
		var mark struct {
			At              time.Time `json:"at"`
			DomainReachedAt time.Time `json:"domain_reached_at"`
		}
		if err := json.Unmarshal(repo.settings[instance.SettingRestore], &mark); err != nil {
			t.Fatal(err)
		}
		if !mark.DomainReachedAt.Equal(arrived) || !mark.At.Equal(time.Date(2026, 11, 10, 14, 2, 11, 0, time.UTC)) {
			t.Fatalf("mark = %+v: the arrival is recorded and the restore's time kept", mark)
		}
		if at, err := svc.RestoredAt(ctx); err != nil || at == nil {
			t.Fatalf("RestoredAt = %v, %v: still a restored panel", at, err)
		}
		svc.CertificateTick(ctx, arrived.Add(time.Hour))
		if prober.calls != 8 {
			t.Fatalf("probes = %d: nothing is probed once the domain arrived", prober.calls)
		}
	})

	t.Run("a route write that failed is tried again a minute later", func(t *testing.T) {
		prober := &probeSpy{}
		svc, _, spy := panel(`{"at":"2026-11-10T14:02:11Z"}`, prober)
		spy.err = errors.New("read-only file system")
		svc.CertificateTick(ctx, start)
		spy.err = nil
		svc.CertificateTick(ctx, start.Add(time.Minute))
		if prober.calls != 2 || len(spy.got) != 2 || spy.got[1] != withDomain {
			t.Fatalf("probes = %d, routes = %+v", prober.calls, spy.got)
		}
	})

	// A server that can't reach its own domain never sees it arrive.
	t.Run("saving the address on the operator's word ends it", func(t *testing.T) {
		prober := &probeSpy{unreachable: true}
		svc, repo, spy := panel(`{"at":"2026-11-10T14:02:11Z"}`, prober)
		repo.operators = map[string]string{"op": instance.SourceFirstRun}
		if _, err := svc.SetPanelDomain(ctx, "op", "panel.example", true); err != nil {
			t.Fatal(err)
		}
		svc.CertificateTick(ctx, start)
		if spy.got[len(spy.got)-1] != withDomain || prober.calls != 0 {
			t.Fatalf("routes = %+v, probes = %d", spy.got, prober.calls)
		}
		if at, err := svc.RestoredAt(ctx); err != nil || at == nil {
			t.Fatalf("RestoredAt = %v, %v: still a restored panel", at, err)
		}
	})

	t.Run("saving the address on a panel never restored leaves no mark", func(t *testing.T) {
		svc, repo, _ := panel("", &probeSpy{})
		repo.operators = map[string]string{"op": instance.SourceFirstRun}
		if _, err := svc.SetPanelDomain(ctx, "op", "toft.example", false); err != nil {
			t.Fatal(err)
		}
		if _, ok := repo.settings[instance.SettingRestore]; ok {
			t.Fatal("a restore mark was written")
		}
	})

	t.Run("kept on the IP: nothing to wait for", func(t *testing.T) {
		prober := &probeSpy{}
		repo := &fakeRepo{settings: map[string][]byte{
			instance.SettingPanel:   []byte(`{"domain":""}`),
			instance.SettingRestore: []byte(`{"at":"2026-11-10T14:02:11Z"}`),
		}}
		spy := &routesSpy{}
		svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false).WithSetup(prober, "").
			WithPanelMonitor(&fakeProbe{}, &fakeStore{}, t.TempDir(), t.TempDir())
		if err := svc.SyncPanelRoutes(ctx); err != nil {
			t.Fatal(err)
		}
		svc.CertificateTick(ctx, start)
		if prober.calls != 0 || len(spy.got) != 1 || !spy.got[0].AnyHost {
			t.Fatalf("probes = %d, routes = %+v", prober.calls, spy.got)
		}
		// With no domain there is no IP answer to stop.
		repo.operators = map[string]string{"op": instance.SourceFirstRun}
		if st, err := svc.Setup(ctx, "op"); err != nil || st.AwaitingDomain {
			t.Fatalf("setup = %+v, %v: a panel on its IP waits for no domain", st, err)
		}
	})

	// D-164: Instance → Overview says so, and "Stop answering on the IP…"
	// saves the address the panel already has.
	t.Run("the setup read says it is waiting, and saving the same address stops it", func(t *testing.T) {
		svc, repo, spy := panel(`{"at":"2026-11-10T14:02:11Z"}`, &probeSpy{unreachable: true})
		repo.operators = map[string]string{"op": instance.SourceFirstRun}
		st, err := svc.Setup(ctx, "op")
		if err != nil || !st.AwaitingDomain {
			t.Fatalf("setup = %+v, %v: want awaiting_domain", st, err)
		}
		st, err = svc.SetPanelDomain(ctx, "op", "panel.example", true)
		if err != nil || st.AwaitingDomain {
			t.Fatalf("setup = %+v, %v: the save ends the wait", st, err)
		}
		if spy.got[len(spy.got)-1] != withDomain {
			t.Fatalf("routes = %+v, want the domain only", spy.got)
		}
		if len(repo.audit) != 1 || repo.audit[0].actor != "op" || repo.audit[0].action != "panel.ip_answer_stopped" || repo.audit[0].detail != `{"domain":"panel.example"}` {
			t.Fatalf("audit = %+v: one row saying the IP answer was stopped, none saying the address changed", repo.audit)
		}
		// Pressed again once the wait is over, as after the domain arrived
		// by itself: nothing changed, so nothing more is recorded.
		if _, err := svc.SetPanelDomain(ctx, "op", "panel.example", true); err != nil || len(repo.audit) != 1 {
			t.Fatalf("the same address saved again: %v, audit = %+v", err, repo.audit)
		}
	})

	t.Run("another address while waiting is a change of address", func(t *testing.T) {
		svc, repo, _ := panel(`{"at":"2026-11-10T14:02:11Z"}`, &probeSpy{})
		repo.operators = map[string]string{"op": instance.SourceFirstRun}
		st, err := svc.SetPanelDomain(ctx, "op", "toft.example", false)
		if err != nil || st.AwaitingDomain {
			t.Fatalf("setup = %+v, %v", st, err)
		}
		if len(repo.audit) != 1 || repo.audit[0].action != "panel.domain_changed" {
			t.Fatalf("audit = %+v", repo.audit)
		}
	})

	t.Run("never restored: not waiting", func(t *testing.T) {
		svc, repo, _ := panel("", &probeSpy{})
		repo.operators = map[string]string{"op": instance.SourceFirstRun}
		if st, err := svc.Setup(ctx, "op"); err != nil || st.AwaitingDomain {
			t.Fatalf("setup = %+v, %v", st, err)
		}
	})
}

// The monitor is what looks for a restored panel's domain: a tick without
// certificateTick leaves the panel answering on its IP for good.
func TestPanelMonitorTickLooksForARestoredPanelsDomain(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{settings: map[string][]byte{
		instance.SettingPanel:   []byte(`{"domain":"panel.example"}`),
		instance.SettingRestore: []byte(`{"at":"2026-11-10T14:02:11Z"}`),
	}}
	prober := &probeSpy{unreachable: true}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(&routesSpy{}, "", false).WithSetup(prober, "").
		WithPanelMonitor(&fakeProbe{}, &fakeStore{}, t.TempDir(), t.TempDir())
	if err := svc.SyncPanelRoutes(ctx); err != nil {
		t.Fatal(err)
	}
	svc.MonitorTick(ctx, time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC))
	if prober.calls != 1 || !prober.withCert {
		t.Fatalf("probes = %d (with the certificate: %v), want one", prober.calls, prober.withCert)
	}
}

// A save whose routes can't be written is taken back: left saved, the next
// start would apply it, and on a restored panel end the IP answer then.
func TestSetPanelDomain_RoutesNotWrittenUndoesTheSave(t *testing.T) {
	ctx := context.Background()
	failing := errors.New("read-only file system")
	start := func(settings map[string][]byte, envDomain string) (*instance.Service, *fakeRepo, *routesSpy) {
		t.Helper()
		repo := &fakeRepo{settings: settings, operators: map[string]string{"op": instance.SourceFirstRun}}
		spy := &routesSpy{}
		svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, envDomain, false).WithSetup(&probeSpy{}, "")
		if err := svc.SyncPanelRoutes(ctx); err != nil {
			t.Fatal(err)
		}
		spy.err = failing
		return svc, repo, spy
	}
	failed := func(t *testing.T, repo *fakeRepo, from, to string) {
		t.Helper()
		want := `{"from":"` + from + `","to":"` + to + `","undone":true}`
		if len(repo.audit) != 1 || repo.audit[0].action != "panel.domain_change_failed" || repo.audit[0].detail != want {
			t.Fatalf("audit = %+v, want one panel.domain_change_failed %s", repo.audit, want)
		}
	}

	t.Run("the stop on a restored panel: it still waits for its domain", func(t *testing.T) {
		svc, repo, spy := start(map[string][]byte{
			instance.SettingPanel:   []byte(`{"domain":"panel.example"}`),
			instance.SettingRestore: []byte(`{"at":"2026-11-10T14:02:11Z"}`),
		}, "")
		if _, err := svc.SetPanelDomain(ctx, "op", "panel.example", true); !errors.Is(err, failing) {
			t.Fatalf("err = %v, want the write's", err)
		}
		failed(t, repo, "panel.example", "panel.example")
		// As a restart reads it: on the IP too, not the domain only.
		spy.err = nil
		if err := svc.SyncPanelRoutes(ctx); err != nil {
			t.Fatal(err)
		}
		if got := spy.got[len(spy.got)-1]; !got.AnyHost {
			t.Fatalf("routes after a restart = %+v: the IP answer ended though the stop failed", got)
		}
		if st, err := svc.Setup(ctx, "op"); err != nil || !st.AwaitingDomain {
			t.Fatalf("setup = %+v, %v: want still awaiting", st, err)
		}
	})

	t.Run("another address: the one it had", func(t *testing.T) {
		svc, repo, _ := start(map[string][]byte{instance.SettingPanel: []byte(`{"domain":"panel.example"}`)}, "")
		if _, err := svc.SetPanelDomain(ctx, "op", "toft.example", false); !errors.Is(err, failing) {
			t.Fatalf("err = %v, want the write's", err)
		}
		failed(t, repo, "panel.example", "toft.example")
		if got := string(repo.settings[instance.SettingPanel]); got != `{"domain":"panel.example"}` || svc.PanelDomain() != "panel.example" {
			t.Fatalf("setting = %s, PanelDomain = %q", got, svc.PanelDomain())
		}
	})

	t.Run("no address of its own before: none after", func(t *testing.T) {
		svc, repo, _ := start(map[string][]byte{}, "env.example")
		if _, err := svc.SetPanelDomain(ctx, "op", "toft.example", false); !errors.Is(err, failing) {
			t.Fatalf("err = %v, want the write's", err)
		}
		failed(t, repo, "env.example", "toft.example")
		if _, ok := repo.settings[instance.SettingPanel]; ok || svc.PanelDomain() != "env.example" {
			t.Fatalf("setting = %s, PanelDomain = %q: want BASE_DOMAIN's again", repo.settings[instance.SettingPanel], svc.PanelDomain())
		}
	})
}

// toft answer-on-ip: a restored panel whose IP answer was stopped waits for
// its domain again from its next start.
func TestAnswerOnIP(t *testing.T) {
	ctx := context.Background()
	t.Run("never restored: nothing to bring back", func(t *testing.T) {
		repo := &fakeRepo{settings: map[string][]byte{instance.SettingPanel: []byte(`{"domain":"panel.example"}`)}}
		if _, err := instance.AnswerOnIP(ctx, repo); !errors.Is(err, instance.ErrNeverRestored) {
			t.Fatalf("err = %v", err)
		}
		if _, ok := repo.settings[instance.SettingRestore]; ok || len(repo.audit) != 0 {
			t.Fatalf("settings = %v, audit = %+v: nothing is written", repo.settings, repo.audit)
		}
	})
	t.Run("stopped: on the IP again after a restart, and recorded", func(t *testing.T) {
		repo := &fakeRepo{settings: map[string][]byte{
			instance.SettingPanel:   []byte(`{"domain":"panel.example"}`),
			instance.SettingRestore: []byte(`{"at":"2026-11-10T14:02:11Z","domain_reached_at":"2026-11-10T14:30:00Z"}`),
		}}
		if already, err := instance.AnswerOnIP(ctx, repo); err != nil || already {
			t.Fatalf("already = %v, err = %v", already, err)
		}
		if len(repo.audit) != 1 || repo.audit[0].action != "panel.ip_answer_restored" || repo.audit[0].actor != "" {
			t.Fatalf("audit = %+v", repo.audit)
		}
		spy := &routesSpy{}
		svc := instance.NewService(repo, zap.NewNop()).WithPanelRoutes(spy, "", false)
		if err := svc.SyncPanelRoutes(ctx); err != nil {
			t.Fatal(err)
		}
		if want := (instance.PanelRoutes{Domain: "panel.example", AnyHost: true}); spy.got[0] != want {
			t.Fatalf("routes = %+v, want %+v", spy.got[0], want)
		}
		if at, err := svc.RestoredAt(ctx); err != nil || at == nil {
			t.Fatalf("RestoredAt = %v, %v: still a restored panel", at, err)
		}
	})
	t.Run("still waiting: nothing to do", func(t *testing.T) {
		repo := &fakeRepo{settings: map[string][]byte{instance.SettingRestore: []byte(`{"at":"2026-11-10T14:02:11Z"}`)}}
		if already, err := instance.AnswerOnIP(ctx, repo); err != nil || !already || len(repo.audit) != 0 {
			t.Fatalf("already = %v, err = %v, audit = %+v", already, err, repo.audit)
		}
	})
}
