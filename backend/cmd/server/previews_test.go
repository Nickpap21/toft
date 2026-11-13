package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/preview"
	"go.uber.org/zap"
)

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
