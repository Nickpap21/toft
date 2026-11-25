package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/docker"
	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/reservation"
	"github.com/docker/docker/api/types/container"
	"go.uber.org/zap"
)

// A builder's memory limit is no reservation (D-170): the room the panel's
// server has for its apps is the whole of the apps' share, and what is
// reserved there is what its services' rows hold, with both of Toft's
// builders running and a deploy's builder sized at half of that share.
func TestABuildersLimitIsNoReservation(t *testing.T) {
	ctx := context.Background()
	f := fakedocker.New(t)
	f.NCPU, f.MemTotal = 8, 16<<30
	f.Add("buildx_buildkit_toft-build0", container.StateRunning, nil)
	f.Add("buildx_buildkit_toft-build-untrusted0", container.StateRunning, nil)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(f.Srv.URL, "http://"))
	net := rollout.NetConfig{AppsSlice: "toft-apps.slice"}
	local, err := docker.NewLocalOrchestrator(zap.NewNop(), net, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, share := reservation.Apps(8, 16<<30)
	for _, b := range []deployment.Builds{{}, {Untrusted: true}} {
		_, done, err := docker.NewFactory(local, net, nil, zap.NewNop()).ForDeploy(ctx, nil, b)
		if err != nil {
			t.Fatal(err)
		}
		done()
		if apps, err := (panelLedger{local: local}).AppsMemoryMB(ctx); err != nil || apps != float64(share)/(1<<20) {
			t.Fatalf("the apps' room is %v MiB (%v), want the whole share, %d MiB", apps, err, share>>20)
		}
	}

	t.Run("what is reserved", func(t *testing.T) {
		ledger := panelLedger{local: local, services: service.NewPostgresRepository(testdb.New(t), nil)}
		if reserved, err := ledger.AllocatedMemoryMB(ctx); err != nil || reserved != 0 {
			t.Fatalf("no service runs on the panel's server, and %v MiB is reserved there (%v)", reserved, err)
		}
	})
}

// The panel host with no domain: its address is the one install.sh recorded.
func TestPanelHostAddressWithoutADomain(t *testing.T) {
	name := func(ip string) (string, error) {
		hosts := previewHosts{domain: func() string { return "" }, panelIP: ip}
		svc := preview.NewService(nil, nil, nil, nil, nil, nil, hosts, nil, nil, false, zap.NewNop())
		return svc.Address(context.Background(), "web", "production", "web", "", func(string) (bool, error) { return false, nil })
	}

	h, err := name("5.75.200.10")
	if err != nil || h != "web-production.5-75-200-10.sslip.io" {
		t.Fatalf("a public IP: %q, %v", h, err)
	}
	for _, ip := range []string{"", "10.0.0.5"} {
		if h, err := name(ip); !errors.Is(err, preview.ErrNoPublicAddress) {
			t.Fatalf("IP %q: %q, %v", ip, h, err)
		}
	}
}

func TestPanelHostAddressWithADomainResolvesIt(t *testing.T) {
	hosts := previewHosts{domain: func() string { return "localhost" }, panelIP: "5.75.200.10"}
	ip, err := hosts.Address(context.Background(), "")
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("got %q, %v", ip, err)
	}
}

func TestDomainCheckKnowsThePanelHostWithoutADomain(t *testing.T) {
	d := domainServers{domain: func() string { return "" }, panelIP: "5.75.200.10"}
	facts, err := d.DomainServer(context.Background(), "", "org")
	if err != nil || facts.Address != "5.75.200.10" {
		t.Fatalf("got %q, %v", facts.Address, err)
	}
}
