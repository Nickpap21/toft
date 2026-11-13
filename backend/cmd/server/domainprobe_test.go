package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
)

// servedChain is what a Let's Encrypt domain serves: a leaf for name and
// the intermediate that signed it, with the root that signed that one.
func servedChain(t *testing.T, name string) (root *x509.Certificate, served tls.Certificate) {
	t.Helper()
	type signed struct {
		cert *x509.Certificate
		key  ed25519.PrivateKey
	}
	serial := int64(0)
	sign := func(tmpl x509.Certificate, by *signed) signed {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		serial++
		tmpl.SerialNumber = big.NewInt(serial)
		tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
		parent, parentKey := &tmpl, key // the root signs itself
		if by != nil {
			parent, parentKey = by.cert, by.key
		}
		der, err := x509.CreateCertificate(rand.Reader, &tmpl, parent, pub, parentKey)
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return signed{c, key}
	}
	authority := func(org string) x509.Certificate {
		return x509.Certificate{Subject: pkix.Name{Organization: []string{org}}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	}
	r := sign(authority("Test Root"), nil)
	i := sign(authority("Test Intermediate"), &r)
	leaf := sign(x509.Certificate{Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, KeyUsage: x509.KeyUsageDigitalSignature}, &i)
	return r.cert, tls.Certificate{Certificate: [][]byte{leaf.cert.Raw, i.cert.Raw}, PrivateKey: leaf.key, Leaf: leaf.cert}
}

// The certificate reported is the one of the connection that answered, and
// "issued" only when this server trusts it for the domain (D-164).
func TestServedCertificate(t *testing.T) {
	root, served := servedChain(t, "panel.example")
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{served}}
	srv.StartTLS()
	defer srv.Close()
	// As the probe's client: it verifies nothing.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // the test's own server
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	roots := x509.NewCertPool()
	roots.AddCert(root)

	got := servedCertificate(resp.TLS, "panel.example", roots)
	if got.State != instance.CertIssued || got.ExpiresAt == nil || !got.ExpiresAt.Equal(served.Leaf.NotAfter) || got.Issuer != "Test Intermediate" {
		t.Fatalf("trusted for the domain, through the intermediate it served: %+v", got)
	}
	alone := *resp.TLS
	alone.PeerCertificates = alone.PeerCertificates[:1]
	for name, c := range map[string]instance.CertificateState{
		"a certificate this server doesn't trust": servedCertificate(resp.TLS, "panel.example", nil),
		"a certificate for another name":          servedCertificate(resp.TLS, "toft.example", roots),
		"a leaf served without its intermediate":  servedCertificate(&alone, "panel.example", roots),
		"no TLS connection":                       servedCertificate(nil, "panel.example", roots),
	} {
		if c.State != "pending" || c.ExpiresAt != nil || c.Issuer != "" {
			t.Errorf("%s: %+v, want pending", name, c)
		}
	}
}
