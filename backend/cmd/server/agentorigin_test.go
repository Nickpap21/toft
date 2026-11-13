package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Without a domain, the agent's origin carries the pin of the certificate
// Traefik serves for the name the request came to (its SNI, none for an
// address); with one, TLS is verified and Traefik isn't asked.
func TestAgentOriginPinsTraefiksCertificateWithoutADomain(t *testing.T) {
	var sni []string
	proxy := httptest.NewUnstartedServer(http.NotFoundHandler())
	proxy.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		sni = append(sni, h.ServerName)
		return nil, nil
	}}
	proxy.StartTLS()
	defer proxy.Close()
	spki := sha256.Sum256(proxy.Certificate().RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(spki[:])

	request := func(host string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		r.Header.Set("X-Forwarded-Proto", "https")
		return r
	}
	origin := agentOrigin(func() string { return "" }, proxy.Listener.Addr().String())
	for host, name := range map[string]string{"203.0.113.5": "", "panel.internal:8443": "panel.internal"} {
		sni = nil
		o, err := origin(request(host))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if o.URL != "https://"+host || !o.InsecureTLS || o.CertPin != pin {
			t.Fatalf("%s: %+v, want the pin %s", host, o, pin)
		}
		if len(sni) != 1 || sni[0] != name {
			t.Fatalf("%s: Traefik was asked for %q, want %q", host, sni, name)
		}
	}

	sni = nil
	o, err := agentOrigin(func() string { return "panel.example.com" }, proxy.Listener.Addr().String())(request("203.0.113.5"))
	if err != nil || o.URL != "https://panel.example.com" || o.InsecureTLS || o.CertPin != "" || len(sni) != 0 {
		t.Fatalf("with a domain: %+v, %v, asked %q", o, err, sni)
	}

	if o, err := agentOrigin(func() string { return "" }, "127.0.0.1:1")(request("203.0.113.5")); err == nil {
		t.Fatalf("Traefik unreachable: %+v, want an error, not an unpinned origin", o)
	}
}
