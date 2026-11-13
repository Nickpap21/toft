package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// An agent stops for good when the panel revoked its key, or refused it
// without a join token (its server was deleted); it retries an outage, and
// a refusal while it still holds a token.
func TestConnectOnceStopsWhenThePanelRefusesForGood(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status int
		code   string
		token  string
		want   error
	}{
		{http.StatusForbidden, "agent_revoked", "", errRevoked},
		{http.StatusUnauthorized, "agent_refused", "", errRefused},
		{http.StatusUnauthorized, "agent_refused", "tok", nil},
		{http.StatusServiceUnavailable, "agent_unavailable", "", nil},
	} {
		panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Toft-Error", tc.code)
			http.Error(w, tc.code, tc.status)
		}))
		cfg := &config{Panel: panel.URL, Server: "s1", Token: tc.token}
		err := connectOnce(context.Background(), cfg, &signer, nil, nil, func() {})
		panel.Close()
		if err == nil {
			t.Fatalf("%s: connected", tc.code)
		}
		if stops := errors.Is(err, errRevoked) || errors.Is(err, errRefused); tc.want == nil && stops {
			t.Errorf("%s with token %q: stops (%v), want a retry", tc.code, tc.token, err)
		} else if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s with token %q: %v, want %v", tc.code, tc.token, err, tc.want)
		}
	}
}

// On a panel without a domain the join token goes only to the certificate
// the join command named: another one (an on-path attacker's, or the
// panel's own after a restart) gets no request at all, and is told apart
// from a used token. A joined agent (no token) still connects to any.
func TestDialPanelPinsTheCertificateWhileItHoldsAToken(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	panel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer panel.Close()
	spki := sha256.Sum256(panel.Certificate().RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(spki[:])
	other := base64.StdEncoding.EncodeToString(make([]byte, 32))

	for _, tc := range []struct {
		name, token, pin string
		reached          bool
	}{
		{"another certificate", "toft_join_x", other, false},
		{"no pin", "toft_join_x", "", false},
		{"the pinned certificate", "toft_join_x", pin, true},
		{"joined: no token", "", "", true},
	} {
		got = nil
		cfg := config{Panel: panel.URL, Server: "s1", Token: tc.token, InsecureTLS: true, PanelCert: tc.pin}
		_, _, err := dialPanel(context.Background(), cfg, signer, "")
		if !tc.reached {
			if len(got) != 0 {
				t.Fatalf("%s: the panel got a request with %q", tc.name, got)
			}
			if err == nil || !strings.Contains(err.Error(), "make a new join command") {
				t.Fatalf("%s: %v, want a plain refusal", tc.name, err)
			}
			continue
		}
		if len(got) != 1 || got[0] != map[bool]string{true: "Bearer " + tc.token, false: ""}[tc.token != ""] {
			t.Fatalf("%s: the panel got %q (%v)", tc.name, got, err)
		}
	}
}

// A panel that admitted the agent's token is the join done, even when
// agent.env couldn't drop the token: after a restart, the agent no longer
// sends it nor holds the panel to the join's certificate, which Traefik
// may since have replaced. An agent never admitted with its token, or
// joined again with a new one, still refuses any other certificate.
func TestAnAdmittedTokenSurvivesAFailedSave(t *testing.T) {
	dir := t.TempDir()
	stateDir, envFile = filepath.Join(dir, "state"), filepath.Join(dir, "agent.env")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var up websocket.Upgrader
	joined := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws, err := up.Upgrade(w, r, nil); err == nil {
			_ = ws.Close()
		}
	}))
	defer joined.Close()
	spki := sha256.Sum256(joined.Certificate().RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(spki[:])
	var got []string
	renewed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		http.Error(w, "no", http.StatusTeapot)
	}))
	// httptest's own certificate is the same for every server.
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)},
		&x509.Certificate{SerialNumber: big.NewInt(1)}, priv.Public(), priv)
	if err != nil {
		t.Fatal(err)
	}
	renewed.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}}
	renewed.StartTLS()
	defer renewed.Close()

	// The join command's agent.env, which the agent then can't rewrite.
	join := config{Panel: joined.URL, Server: "s1", Token: "toft_join_x", PanelKeyFP: "SHA256:x", InsecureTLS: true, PanelCert: pin}
	if err := join.save(); err != nil {
		t.Fatal(err)
	}
	unadmitted := func(name string) {
		t.Helper()
		got = nil
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Panel = renewed.URL
		if _, _, err := dialPanel(context.Background(), cfg, signer, ""); len(got) != 0 || err == nil || !strings.Contains(err.Error(), "make a new join command") {
			t.Fatalf("%s: the panel got %q (%v), want a refusal before any request", name, got, err)
		}
	}
	unadmitted("never admitted")

	if err := os.Mkdir(envFile+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := connectOnce(context.Background(), &cfg, &signer, nil, nil, func() {}); err == nil || !strings.Contains(err.Error(), "SSH handshake") {
		t.Fatalf("connectOnce: %v, want the panel admitted it", err)
	}

	// The restart, and a new certificate.
	got = nil
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Panel = renewed.URL
	if _, _, err := dialPanel(context.Background(), cfg, signer, ""); len(got) != 1 || got[0] != "" {
		t.Fatalf("admitted: the panel got %q (%v), want one request without the spent token", got, err)
	}

	// Joined again: the new token goes only to the new join's certificate.
	join.Token = "toft_join_y"
	if err := os.Remove(envFile + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := join.save(); err != nil {
		t.Fatal(err)
	}
	unadmitted("joined again")
}

// Refused by a panel being restored, an agent keeps trying; it stops only
// after an hour of refusals without a break.
func TestRefusedTooLong(t *testing.T) {
	var since time.Time
	t0 := time.Now()
	if refusedTooLong(&since, errRefused, t0) || refusedTooLong(&since, errRefused, t0.Add(59*time.Minute)) {
		t.Fatal("stopped within the hour")
	}
	if refusedTooLong(&since, errors.New("the panel answered 503"), t0.Add(59*time.Minute)) ||
		refusedTooLong(&since, errRefused, t0.Add(61*time.Minute)) {
		t.Fatal("an answer in between didn't reset the hour")
	}
	if !refusedTooLong(&since, errRefused, t0.Add(121*time.Minute)) {
		t.Fatal("still running after an hour of refusals")
	}
}
