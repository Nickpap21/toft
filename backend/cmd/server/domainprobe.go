package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
)

// domainProber checks a domain for the panel (instance.DomainProber): what
// it resolves to, whether https://<domain>/api/v1/instance/probe comes back
// to this backend, and the certificate it serves.
type domainProber struct{}

func (domainProber) ProbeDomain(ctx context.Context, domain, probeID string, withCert bool) (instance.PanelDomainCheck, error) {
	out := instance.PanelDomainCheck{Domain: domain, Addresses: []string{}, Certificate: instance.CertificateState{State: "pending"}}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	addrs, err := net.DefaultResolver.LookupHost(lctx, domain)
	cancel()
	if err != nil || len(addrs) == 0 {
		return out, nil
	}
	slices.Sort(addrs)
	out.Addresses = addrs

	// The certificate may not exist yet: the probe value, random per
	// process, is what proves the answer came from this panel.
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // compared with a random value, never trusted
			// One request per transport: nothing is left open behind it.
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+domain+"/api/v1/instance/probe", nil)
	if err != nil {
		return out, err
	}
	if resp, err := client.Do(req); err == nil {
		var body struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil {
			out.ReachesPanel = body.Data.ID == probeID
		}
		if withCert {
			out.Certificate = servedCertificate(resp.TLS, domain, nil)
		}
		resp.Body.Close()
	}
	return out, nil
}

// servedCertificate is the certificate of the connection that answered the
// probe: issued when this server trusts it for domain (roots nil: the
// system's), pending otherwise. The probe's client verifies nothing, so it
// is verified here; a second dial, with a DNS lookup of its own, could reach
// another server than the one that answered (D-164).
func servedCertificate(cs *tls.ConnectionState, domain string, roots *x509.CertPool) instance.CertificateState {
	out := instance.CertificateState{State: "pending"}
	if cs == nil || len(cs.PeerCertificates) == 0 {
		return out
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: domain, Roots: roots, Intermediates: inter}); err != nil {
		return out
	}
	out.State = instance.CertIssued
	out.ExpiresAt = &leaf.NotAfter
	out.Issuer = leaf.Issuer.CommonName
	if len(leaf.Issuer.Organization) > 0 {
		out.Issuer = leaf.Issuer.Organization[0]
	}
	return out
}
