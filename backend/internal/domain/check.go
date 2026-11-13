package domain

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// What the Domains tab shows of each domain: where its DNS points, the
// certificate the service's server presents for it, and whether the running
// container is routed for it (D-45). Looked up when asked, never stored,
// except that a valid certificate's first sighting is (certificate_at).

// ServerFacts are what a domain check needs of the server a service runs on.
type ServerFacts struct {
	Name string
	// Address is the public address the domains' DNS must point at; ""
	// when it is not known (the panel host when its own name doesn't
	// resolve). A workspace using a shared server is told it here, and only
	// here: it is public as soon as any domain points at it (D-45).
	Address string
	// Wildcards can get certificates there (a DNS provider, not the panel
	// host).
	WildcardsOK bool
	// Owner: the caller's workspace owns the server, so the provider's name
	// and the provisioning time are theirs to see.
	Owner              bool
	DNSProvider        string
	DNSProviderSavedAt *time.Time
	ProvisionedAt      *time.Time
}

// ServerLookup gives the facts of a service's server ("" the panel host)
// as orgID may see them.
type ServerLookup interface {
	DomainServer(ctx context.Context, serverID, orgID string) (ServerFacts, error)
}

// Prober looks names up and reads the certificate an address presents for
// a name. The checks' test seam.
type Prober interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	// Certificate is the leaf address:443 presents for serverName, and
	// whether it verifies for that name against the system's roots.
	Certificate(ctx context.Context, address, serverName string) (*x509.Certificate, bool, error)
}

// DNS states.
const (
	DNSHere      = "here"      // every address it resolves to is the server's
	DNSElsewhere = "elsewhere" // it resolves, not (only) to the server
	DNSNone      = "none"      // no record
	DNSUnknown   = "unknown"   // the lookup failed, or the server's address is unknown
)

// Certificate states.
const (
	CertValid   = "valid"   // verifies for the name
	CertInvalid = "invalid" // presented but doesn't verify (expired, another name)
	CertNone    = "none"    // not issued: not routed yet, or Traefik's default
	CertUnknown = "unknown" // the server could not be reached
)

type DomainCheck struct {
	ID        string   `json:"id"`
	Hostname  string   `json:"hostname"`
	DNS       string   `json:"dns"`
	Addresses []string `json:"addresses"`
	// Certificate and, when one was presented, its expiry and issuer.
	Certificate string     `json:"certificate"`
	NotAfter    *time.Time `json:"not_after,omitempty"`
	Issuer      string     `json:"issuer,omitempty"`
	// Routed: the running container is routed for it now.
	Routed bool `json:"routed"`
}

type CheckServer struct {
	Name               string     `json:"name"`
	Address            string     `json:"address"`
	WildcardsOK        bool       `json:"wildcards_ok"`
	Owner              bool       `json:"owner"`
	DNSProvider        string     `json:"dns_provider,omitempty"`
	DNSProviderSavedAt *time.Time `json:"dns_provider_saved_at,omitempty"`
	ProvisionedAt      *time.Time `json:"provisioned_at,omitempty"`
}

// Check is one look at a service's domains.
type Check struct {
	CheckedAt time.Time   `json:"checked_at"`
	Server    CheckServer `json:"server"`
	// RoutingKnown: a running container was read; otherwise nothing is
	// routed and Routed is false throughout.
	RoutingKnown bool          `json:"routing_known"`
	Domains      []DomainCheck `json:"domains"`
	// RoutedRemoved are names the running container is still routed for
	// that are no longer the service's: removed, until the next deploy.
	RoutedRemoved []string `json:"routed_removed"`
}

// checkConcurrency bounds the lookups and handshakes at once: a service
// with many domains is checked in a few seconds, not one after another.
const checkConcurrency = 4

// checkTimeout bounds one whole check.
const checkTimeout = 10 * time.Second

type Checker struct {
	repo     Repository
	services ServiceRouting
	servers  ServerLookup
	probe    Prober
	log      *zap.Logger
	// redeployer: as Service's (WithRedeployer).
	redeployer Redeployer
}

func NewChecker(repo Repository, services ServiceRouting, servers ServerLookup, probe Prober, log *zap.Logger) *Checker {
	return &Checker{repo: repo, services: services, servers: servers, probe: probe, log: log}
}

// WithRedeployer is Service.WithRedeployer, for the certificates a check
// records.
func (c *Checker) WithRedeployer(r Redeployer) { c.redeployer = r }

// Check looks up each of the service's domains, reads the certificate the
// service's own server presents for it (never another address), and reads
// what the running container is routed for.
func (c *Checker) Check(ctx context.Context, serviceID, orgID string) (*Check, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	domains, err := c.repo.ListByService(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	_, serverID, err := c.services.DomainTarget(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("get the service: %w", err)
	}
	facts, err := c.servers.DomainServer(ctx, serverID, orgID)
	if err != nil {
		return nil, fmt.Errorf("get the server: %w", err)
	}
	routed, running, err := c.services.RoutedHosts(ctx, serviceID)
	if err != nil {
		// Shown as not known rather than failing the DNS and certificates.
		c.log.Warn("read routed hosts", zap.Error(err), zap.String("service_id", serviceID))
		running = false
	}

	out := &Check{
		CheckedAt: time.Now().UTC(),
		Server: CheckServer{
			Name: facts.Name, Address: facts.Address, WildcardsOK: facts.WildcardsOK, Owner: facts.Owner,
		},
		RoutingKnown:  running,
		Domains:       make([]DomainCheck, len(domains)),
		RoutedRemoved: make([]string, 0),
	}
	if facts.Owner {
		out.Server.DNSProvider = facts.DNSProvider
		out.Server.DNSProviderSavedAt = facts.DNSProviderSavedAt
		out.Server.ProvisionedAt = facts.ProvisionedAt
	}

	// One goroutine per domain, at most checkConcurrency at once; each
	// writes only its own slot, and all are done before Check returns.
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup
	for i, d := range domains {
		isRouted := running && slices.Contains(routed, d.Hostname)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out.Domains[i] = DomainCheck{ID: d.ID, Hostname: d.Hostname, DNS: DNSUnknown, Addresses: []string{}, Certificate: CertUnknown, Routed: isRouted}
				return
			}
			defer func() { <-sem }()
			out.Domains[i] = c.checkOne(ctx, d, facts.Address, isRouted)
		}()
	}
	wg.Wait()

	if running {
		for _, h := range routed {
			if !slices.ContainsFunc(domains, func(d Domain) bool { return d.Hostname == h }) {
				out.RoutedRemoved = append(out.RoutedRemoved, h)
			}
		}
	}
	return out, nil
}

func (c *Checker) checkOne(ctx context.Context, d Domain, address string, routed bool) DomainCheck {
	res := DomainCheck{ID: d.ID, Hostname: d.Hostname, Addresses: []string{}, Routed: routed, Certificate: CertNone}
	// A wildcard is looked up, and asked for its certificate, as a name
	// one label below it that has no record of its own.
	name := d.Hostname
	if base, ok := strings.CutPrefix(d.Hostname, "*."); ok {
		name = randomLabel() + "." + base
	}

	addrs, err := c.probe.LookupHost(ctx, name)
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		slices.Sort(addrs)
		res.Addresses = addrs
		res.DNS = compareA(addrs, address)
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		res.DNS = DNSNone
	default:
		res.DNS = DNSUnknown
	}

	// Traefik asks Let's Encrypt for a certificate when a deploy routes the
	// name, so there is none to read before; without the server's address
	// there is nowhere to read it from.
	if !routed {
		return res
	}
	if address == "" {
		res.Certificate = CertUnknown
		return res
	}
	cert, verified, err := c.probe.Certificate(ctx, address, name)
	switch {
	case err != nil:
		res.Certificate = CertUnknown
	case cert == nil || strings.Contains(cert.Subject.CommonName, "TRAEFIK DEFAULT CERT"):
		res.Certificate = CertNone
	default:
		na := cert.NotAfter.UTC()
		res.NotAfter = &na
		res.Issuer = issuerName(cert)
		if verified {
			res.Certificate = CertValid
			// Recorded as the 5-minute check would (D-150), so the header,
			// the link and the routes agree with what this shows.
			if d.CertificateAt == nil {
				recordCertificate(ctx, c.repo, c.redeployer, c.log, d)
			}
		} else {
			res.Certificate = CertInvalid
		}
	}
	return res
}

// compareA says where a name's IPv4 records point against the server's
// address: here when every one is it. IPv6 records are shown but not
// compared, since the panel doesn't know a server's IPv6 address.
func compareA(addrs []string, address string) string {
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	switch {
	case address == "" || len(v4) == 0:
		return DNSUnknown
	case slices.ContainsFunc(v4, func(a string) bool { return a != address }):
		return DNSElsewhere
	default:
		return DNSHere
	}
}

// issuerName is the issuing organisation: "Let's Encrypt".
func issuerName(cert *x509.Certificate) string {
	if len(cert.Issuer.Organization) > 0 {
		return cert.Issuer.Organization[0]
	}
	return cert.Issuer.CommonName
}

func randomLabel() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "toft-check-" + hex.EncodeToString(b)
}

// NetProber is the Prober on the network: the system resolver, and a TLS
// handshake that reads the certificate without trusting it, then verifies
// it on its own.
type NetProber struct{}

func (NetProber) LookupHost(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (NetProber) Certificate(ctx context.Context, address, serverName string) (*x509.Certificate, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{
		ServerName: serverName,
		// Read whatever is presented, then verify it below: an expired or
		// default certificate is a finding, not an error.
		InsecureSkipVerify: true, //nolint:gosec // verified explicitly below
	}}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(address, "443"))
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, false, nil
	}
	leaf := state.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: serverName, Intermediates: inter})
	return leaf, verr == nil, nil
}
