package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"golang.org/x/crypto/ssh"
)

// unit is toft-agent.service (D-106). Root, because phase 1 does what root
// SSH did; restarted always, except after a revocation (78). It doesn't
// depend on Docker, so it survives Docker failing and can repair it.
const unit = `[Unit]
Description=Toft agent: connects this server to its Toft panel
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
EnvironmentFile=/etc/toft-agent/agent.env
Environment=GOMEMLIMIT=96MiB
ExecStart=/usr/local/bin/toft-agent run
Restart=always
RestartSec=2
RestartPreventExitStatus=78
KillMode=process
MemoryMax=256M
TasksMax=512
OOMScoreAdjust=-900
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

// join writes the agent's configuration and key, starts the service and
// waits for it to connect. The install script runs it.
func join(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	panel := fs.String("panel", "", "the panel's address, https://…")
	server := fs.String("server", "", "this server's ID in the panel")
	token := fs.String("token", "", "the one-time join token")
	tokenFile := fs.String("token-file", "", "a file holding the join token, deleted once read (keeps it off the command line)")
	panelKey := fs.String("panel-key", "", "the panel key's SHA256 fingerprint")
	insecure := fs.Bool("insecure-tls", false, "the panel has no domain (its SSH key carries the trust)")
	panelCert := fs.String("panel-cert", "", "the SHA-256 (base64) of the panel certificate's public key: the join token goes to no other")
	noStart := fs.Bool("no-start", false, "write the files only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tokenFile != "" {
		t, err := readTokenFile(*tokenFile)
		if err != nil {
			return err
		}
		*token = t
	}
	if *panel == "" || *server == "" || *token == "" || *panelKey == "" {
		return errors.New("join needs --panel, --server, --token or --token-file, and --panel-key")
	}
	if os.Geteuid() != 0 {
		return errors.New("run join as root")
	}
	// A server removed in the panel and added again is another server there:
	// the agent keeps nothing that was the previous one's.
	if old, err := loadConfig(); err == nil && old.Server != *server {
		_ = exec.Command("systemctl", "stop", "toft-agent.service").Run()
		forgetServer()
	}
	cfg := config{Panel: *panel, Server: *server, Token: *token, PanelKeyFP: *panelKey, InsecureTLS: *insecure, PanelCert: *panelCert}
	if err := cfg.save(); err != nil {
		return fmt.Errorf("write %s: %w", envFile, err)
	}
	// A rejoin may follow a restored panel: pin the key this token names.
	_ = os.Remove(panelKeyFile())
	signer, err := loadOrCreateKey()
	if err != nil {
		return fmt.Errorf("agent key: %w", err)
	}
	pinReleaseKey()
	fmt.Printf("Agent key: %s\n", ssh.FingerprintSHA256(signer.PublicKey()))
	fmt.Println("Compare it with the one the panel shows once this server joins.")
	if *noStart {
		return nil
	}
	if err := writeFileAtomic(unitFile, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", unitFile, err)
	}
	before := readState().ConnectedAt
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "toft-agent.service"}, {"restart", "toft-agent.service"}} {
		if out, err := exec.Command("systemctl", c...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %v: %s", c, err, out)
		}
	}
	fmt.Print("Waiting for the agent to connect")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		fmt.Print(".")
		s := readState()
		if s.ConnectedAt != "" && s.ConnectedAt != before {
			fmt.Printf("\nConnected to %s. toft-agent %s runs as toft-agent.service.\n", *panel, buildinfo.Version)
			return nil
		}
	}
	s := readState()
	fmt.Println()
	return fmt.Errorf("the agent hasn't connected yet (last error: %s). It keeps trying; `toft-agent check` shows what's in the way", orNone(s.LastError))
}

// readTokenFile reads the join token the install script left in path and
// deletes the file: the token is single use, and agent.env keeps it.
func readTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the join token: %w", err)
	}
	_ = os.Remove(path)
	return strings.TrimSpace(string(raw)), nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// check walks the path to the panel and says where it stops.
func check() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	u, err := url.Parse(cfg.Panel)
	if err != nil {
		return err
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	step := func(name string, err error) bool {
		if err != nil {
			fmt.Printf("✗ %s: %v\n", name, err)
			return false
		}
		fmt.Printf("✓ %s\n", name)
		return true
	}
	if os.Getenv("HTTPS_PROXY") != "" || os.Getenv("https_proxy") != "" {
		fmt.Println("· through the proxy in HTTPS_PROXY")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if net.ParseIP(host) == nil {
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if !step("resolve "+host+fmt.Sprintf(" %v", addrs), err) {
			return errors.New("fix DNS on this server")
		}
	}
	c, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if !step("reach "+net.JoinHostPort(host, port), err) {
		return errors.New("a firewall between this server and the panel blocks it")
	}
	if u.Scheme == "https" {
		tc := tls.Client(c, &tls.Config{ServerName: host, InsecureSkipVerify: cfg.InsecureTLS}) //nolint:gosec // D-106
		if !step("TLS with "+host, tc.HandshakeContext(ctx)) {
			_ = c.Close()
			return errors.New("TLS failed: a proxy that inspects TLS, or the panel's certificate")
		}
	}
	_ = c.Close()
	signer, err := loadOrCreateKey()
	if err != nil {
		return err
	}
	ws, resp, err := dialPanel(ctx, cfg, signer, "")
	if err != nil {
		if resp != nil {
			err = fmt.Errorf("%s %s", resp.Status, resp.Header.Get("X-Toft-Error"))
		}
		step("open the agent connection", err)
		return errors.New("the panel refused the connection")
	}
	_ = ws.Close()
	step("open the agent connection", nil)
	fmt.Println("The path to the panel is clear. If the agent still shows offline, see journalctl -u toft-agent.")
	return nil
}

func status() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	signer, err := loadOrCreateKey()
	if err != nil {
		return err
	}
	s := readState()
	fmt.Printf("version     %s\npanel       %s\nserver      %s\nagent key   %s\nconnected   %s\nlast error  %s\n",
		buildinfo.Version, cfg.Panel, cfg.Server, ssh.FingerprintSHA256(signer.PublicKey()), orNone(s.ConnectedAt), orNone(s.LastError))
	if s.Trial != "" {
		fmt.Printf("on trial    %s (back to %s if it doesn't connect)\n", s.Trial, s.Previous)
	}
	return nil
}

// forgetServer drops what the agent keeps for the server it joined as: its
// desired state, which would otherwise outrank the new server's generations,
// the rollouts' journals and the metrics the panel would backfill.
func forgetServer() {
	_ = os.Remove(desiredFile())
	_ = os.Remove(metricsFile())
	_ = os.RemoveAll(rolloutDir())
}

func uninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("run uninstall as root")
	}
	_ = exec.Command("systemctl", "stop", "toft-agent.service").Run()
	removeSelf()
	fmt.Println("Removed toft-agent. The panel shows this server offline until it is removed there or joins again.")
	return nil
}
