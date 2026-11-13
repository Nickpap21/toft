package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"time"

	"github.com/Nickpap21/toft/backend/internal/docker"
	"github.com/Nickpap21/toft/backend/internal/instance"
)

// panelProbe reads the panel's own host for Instance → Overview and Server
// (instance.PanelProbe, D-68): the local Docker daemon, and the certificate
// Traefik serves for the panel's domain.
type panelProbe struct {
	local *docker.LocalOrchestrator
	// proxy is Traefik's container, reached by name on the control plane's
	// network.
	proxy string
}

func (p panelProbe) Containers(ctx context.Context, names []string, stats bool) ([]instance.PanelContainer, error) {
	cs, err := p.local.PanelContainers(ctx, names, stats)
	if err != nil && cs == nil {
		return nil, err
	}
	out := make([]instance.PanelContainer, 0, len(cs))
	for _, c := range cs {
		pc := instance.PanelContainer{Name: c.Name, Image: c.Image, State: c.State, Health: c.Health, Restarts: c.Restarts,
			CPUPercent: c.CPUPercent, MemoryBytes: c.MemoryBytes, MemoryLimitBytes: c.MemoryLimitBytes}
		if t, err := time.Parse(time.RFC3339Nano, c.StartedAt); err == nil && t.Year() > 2000 {
			pc.StartedAt = &t
		}
		out = append(out, pc)
	}
	return out, err
}

func (p panelProbe) Facts(ctx context.Context) (instance.PanelFacts, error) {
	f, err := p.local.HostFacts(ctx)
	return instance.PanelFacts{CPUs: f.CPUs, MemoryBytes: f.MemoryBytes, OS: f.OS, Kernel: f.Kernel, DockerVersion: f.DockerVersion}, err
}

func (p panelProbe) Disk(ctx context.Context) (instance.PanelDisk, error) {
	d, err := p.local.DiskUse(ctx)
	return instance.PanelDisk{Images: d.Images, ImagesUnused: d.ImagesUnused, ImagesBytes: d.ImagesBytes, VolumesBytes: d.VolumesBytes,
		BuildCacheBytes: d.BuildCacheBytes, ContainersBytes: d.ContainersBytes}, err
}

// Certificate asks Traefik for domain's certificate directly, so no DNS or
// hairpin NAT is involved. It reads the certificate whatever it is (expired,
// or Traefik's own self-signed one when Let's Encrypt has none yet), so it
// does not verify it; nothing is sent over the connection.
func (p panelProbe) Certificate(ctx context.Context, domain string) (*instance.PanelCertificate, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: domain, InsecureSkipVerify: true}} //nolint:gosec // read-only, see above
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.proxy, "443"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no certificate")
	}
	c := certs[0]
	issuer := c.Issuer.CommonName
	if len(c.Issuer.Organization) > 0 {
		issuer = c.Issuer.Organization[0]
	}
	return &instance.PanelCertificate{Domain: domain, Issuer: issuer, NotAfter: c.NotAfter.UTC()}, nil
}

func (p panelProbe) Logs(ctx context.Context, name string, tail int, line func(string)) error {
	return p.local.StreamLogs(ctx, "", tail, name, line)
}

func (p panelProbe) LogsSince(ctx context.Context, name string, since time.Time, line func(string)) error {
	return p.local.LogsSince(ctx, name, since, line)
}

func (p panelProbe) RunOnHost(ctx context.Context, argv []string) (string, error) {
	return p.local.RunOnHost(ctx, argv)
}

func (p panelProbe) RunOnHostStream(ctx context.Context, argv []string, stdin io.Reader, logFn func(string)) (string, error) {
	return p.local.RunOnHostStream(ctx, argv, stdin, logFn)
}

func (p panelProbe) SweepHelpers(ctx context.Context) ([]instance.SweptShell, error) {
	swept, err := p.local.SweepPanelHelpers(ctx)
	out := make([]instance.SweptShell, 0, len(swept))
	for _, h := range swept {
		if h.Shell {
			out = append(out, instance.SweptShell{ActorID: h.ActorID, Opened: h.Opened})
		}
	}
	return out, err
}

func (p panelProbe) Shell(ctx context.Context, cols, rows int, actorID string) (instance.PanelShell, error) {
	sh, err := p.local.OpenPanelShell(ctx, cols, rows, actorID)
	if err != nil {
		return nil, err // not a nil *PanelShell in a non-nil interface
	}
	return sh, nil
}
