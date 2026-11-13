package instance_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestValidateWatchdogURL(t *testing.T) {
	assert.NoError(t, instance.ValidateWatchdogURL("https://hc-ping.com/7f3c2a91"))
	assert.ErrorIs(t, instance.ValidateWatchdogURL("http://hc-ping.com/7f3c2a91"), instance.ErrWatchdogURL)
	assert.ErrorIs(t, instance.ValidateWatchdogURL("hc-ping.com/x"), instance.ErrWatchdogURL)
	assert.ErrorIs(t, instance.ValidateWatchdogURL("https://10.0.0.5/ping"), netcheck.ErrPrivateAddress)
	assert.ErrorIs(t, instance.ValidateWatchdogURL("https://127.0.0.1/ping"), netcheck.ErrPrivateAddress)
}

// The panel pings while it is healthy and holds back while it isn't, so the
// watchdog notices; setting the URL pings at once.
func TestWatchdog(t *testing.T) {
	ctx := context.Background()
	var pings atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings.Add(1)
		assert.True(t, strings.HasPrefix(r.UserAgent(), "Toft/"))
	}))
	defer srv.Close()

	repo := newFakeRepo(ada, bob)
	repo.operators[ada.id] = "first_run"
	probe := &fakeProbe{}
	svc := instance.NewService(repo, zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir()).WithWatchdog(srv.Client())

	_, err := svc.SetWatchdog(ctx, bob.id, srv.URL)
	assert.ErrorIs(t, err, instance.ErrNotOperator)

	// httptest's address is private, which the real client refuses at
	// connect time; the URL check sees a literal 127.0.0.1 and refuses too.
	_, err = svc.SetWatchdog(ctx, ada.id, srv.URL)
	require.ErrorIs(t, err, netcheck.ErrPrivateAddress)
	require.NoError(t, repo.MergeSetting(ctx, instance.SettingWatchdog, []byte(`{"url":"`+srv.URL+`"}`), ada.id))

	w, err := svc.TestWatchdog(ctx, ada.id)
	require.NoError(t, err)
	require.NotNil(t, w.LastStatus)
	assert.Equal(t, 200, *w.LastStatus)
	assert.Equal(t, int32(1), pings.Load())

	now := time.Now().Add(10 * time.Minute)
	svc.WatchdogTick(ctx, now)
	assert.Equal(t, int32(2), pings.Load(), "healthy: pinged")
	svc.WatchdogTick(ctx, now.Add(time.Minute))
	assert.Equal(t, int32(2), pings.Load(), "not again within 5 minutes")

	probe.containers = []instance.PanelContainer{{Name: "paas-backend", State: "running", Health: "unhealthy"}}
	svc.WatchdogTick(ctx, now.Add(6*time.Minute))
	assert.Equal(t, int32(2), pings.Load(), "unhealthy: held back")
	w, err = svc.WatchdogStatus(ctx, ada.id)
	require.NoError(t, err)
	assert.True(t, w.Held)

	w, err = svc.SetWatchdog(ctx, ada.id, "")
	require.NoError(t, err)
	assert.Empty(t, w.URL)
	var actions []string
	for _, a := range repo.audit {
		actions = append(actions, a.action)
	}
	assert.Contains(t, actions, "panel.watchdog_cleared")
}

func TestPanelProblems(t *testing.T) {
	ctx := context.Background()
	probe := &fakeProbe{containers: []instance.PanelContainer{
		{Name: "paas-frontend", State: "restarting", Restarts: 3},
		{Name: "paas-traefik", State: "missing"},
	}}
	svc := instance.NewService(newFakeRepo(ada), zap.NewNop()).WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir())
	containers, cert, read, err := svc.PanelProblems(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "paas-frontend keeps restarting (3 restarts); paas-traefik is missing", containers)
	assert.Empty(t, cert, "no domain, nothing to check")
	assert.True(t, read, "nothing to check is an answer")
}

// A certificate that couldn't be read is not a healthy one (D-164): the
// alert monitor leaves its alert as it is.
func TestPanelProblemsCertificateNotRead(t *testing.T) {
	probe := &fakeProbe{certErr: errors.New("dial tcp: i/o timeout")}
	svc := instance.NewService(newFakeRepo(ada), zap.NewNop()).
		WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir()).
		WithPanelRoutes(&routesSpy{}, "panel.example", false)
	_, cert, read, err := svc.PanelProblems(context.Background(), true)
	require.NoError(t, err)
	assert.Empty(t, cert)
	assert.False(t, read)
}

func TestPanelCertificateReadAgainWhenNotHealthy(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	selfSigned := &instance.PanelCertificate{Domain: "panel.example", Issuer: "TRAEFIK DEFAULT CERT", NotAfter: now.Add(365 * 24 * time.Hour)}
	issued := &instance.PanelCertificate{Domain: "panel.example", Issuer: "Let's Encrypt R11", NotAfter: now.Add(80 * 24 * time.Hour)}
	probe := &fakeProbe{cert: selfSigned}
	svc := instance.NewService(newFakeRepo(ada), zap.NewNop()).
		WithPanelMonitor(probe, &fakeStore{}, t.TempDir(), t.TempDir()).
		WithPanelRoutes(&routesSpy{}, "panel.example", false)

	_, cert, _, err := svc.PanelProblems(ctx, true)
	require.NoError(t, err)
	assert.Contains(t, cert, "self-signed")

	// A bad read is read again within a minute, so the alert judges a fresh one.
	probe.cert = issued
	svc.AgePanelCertificate(2 * time.Minute)
	_, cert, _, err = svc.PanelProblems(ctx, true)
	require.NoError(t, err)
	assert.Empty(t, cert)
	assert.Equal(t, 2, probe.certCalls)

	// A healthy read keeps its 10 minutes.
	svc.AgePanelCertificate(2 * time.Minute)
	_, cert, _, err = svc.PanelProblems(ctx, true)
	require.NoError(t, err)
	assert.Empty(t, cert)
	assert.Equal(t, 2, probe.certCalls)
}

func TestCertProblem(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	c := func(issuer string, days int) *instance.PanelCertificate {
		return &instance.PanelCertificate{Domain: "panel.example", Issuer: issuer, NotAfter: now.Add(time.Duration(days) * 24 * time.Hour)}
	}
	assert.Empty(t, instance.CertProblem(nil, now))
	assert.Empty(t, instance.CertProblem(c("Let's Encrypt", 61), now))
	assert.Contains(t, instance.CertProblem(c("Let's Encrypt", 9), now), "expires in 9 days, on 2026-10-27")
	assert.Contains(t, instance.CertProblem(c("Let's Encrypt", -2), now), "expired on 2026-10-16")
	assert.Contains(t, instance.CertProblem(c("TRAEFIK DEFAULT CERT", 365), now), "self-signed")
}
