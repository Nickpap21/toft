package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Where the agent keeps itself (D-106).
var (
	envFile    = "/etc/toft-agent/agent.env"
	stateDir   = "/var/lib/toft-agent"
	libDir     = "/usr/local/lib/toft-agent"
	binLink    = "/usr/local/bin/toft-agent"
	unitFile   = "/etc/systemd/system/toft-agent.service"
	dockerSock = "/var/run/docker.sock"
)

func keyFile() string      { return filepath.Join(stateDir, "agent.key") }
func nextKeyFile() string  { return filepath.Join(stateDir, "agent.key.next") }
func panelKeyFile() string { return filepath.Join(stateDir, "panel.pub") }
func releaseFile() string  { return filepath.Join(stateDir, "release.pub") }
func stateFile() string    { return filepath.Join(stateDir, "state.json") }

// config is agent.env: KEY=VALUE lines.
type config struct {
	Panel       string // https://panel.example
	Server      string // the server's ID
	Token       string // join token, until the first connection succeeds
	PanelKeyFP  string // SHA256 fingerprint of the panel key, until it is pinned
	InsecureTLS bool   // the panel has no domain: SSH keys carry the trust
	PanelCert   string // SHA-256 of the panel certificate's public key (base64), while Token is kept
}

func loadConfig() (config, error) {
	f, err := os.Open(envFile)
	if err != nil {
		return config{}, fmt.Errorf("read %s: %w (join this server first)", envFile, err)
	}
	defer f.Close()
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			vals[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	c := config{
		Panel:       strings.TrimRight(vals["TOFT_PANEL"], "/"),
		Server:      vals["TOFT_SERVER"],
		Token:       vals["TOFT_JOIN_TOKEN"],
		PanelKeyFP:  vals["TOFT_PANEL_KEY"],
		InsecureTLS: vals["TOFT_INSECURE_TLS"] == "1",
		PanelCert:   vals["TOFT_PANEL_CERT"],
	}
	// The panel admitted this token already: the join is done (a new join
	// brings a new token, held to its certificate again).
	if c.Token != "" && readState().JoinedWith == tokenSum(c.Token) {
		c.Token, c.PanelCert = "", ""
	}
	if c.Panel == "" || c.Server == "" {
		return c, fmt.Errorf("%s needs TOFT_PANEL and TOFT_SERVER", envFile)
	}
	return c, sc.Err()
}

func (c config) save() error {
	lines := []string{
		"# Written by toft-agent join. The agent's key is in " + stateDir + ".",
		"TOFT_PANEL=" + c.Panel,
		"TOFT_SERVER=" + c.Server,
	}
	if c.Token != "" {
		lines = append(lines, "TOFT_JOIN_TOKEN="+c.Token)
	}
	if c.PanelKeyFP != "" {
		lines = append(lines, "TOFT_PANEL_KEY="+c.PanelKeyFP)
	}
	if c.InsecureTLS {
		lines = append(lines, "TOFT_INSECURE_TLS=1")
	}
	if c.PanelCert != "" {
		lines = append(lines, "TOFT_PANEL_CERT="+c.PanelCert)
	}
	return writeFileAtomic(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// writeFileAtomic writes path through a temporary file, synced to disk,
// and a rename: a rollout's journal outlives a power cut.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadOrCreateKey returns the agent's ed25519 key, made on first use. It
// never leaves the server.
func loadOrCreateKey() (ssh.Signer, error) {
	raw, err := os.ReadFile(keyFile())
	if errors.Is(err, os.ErrNotExist) {
		return createKey(keyFile())
	}
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(raw)
}

func createKey(path string) (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "toft-agent")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

func authorizedKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// state.json: what the agent tells `status` and the trial boot.
type agentState struct {
	ConnectedAt  string `json:"connected_at,omitempty"`
	Trial        string `json:"trial,omitempty"`    // a version on trial since its upgrade
	Previous     string `json:"previous,omitempty"` // what the trial goes back to
	LastError    string `json:"last_error,omitempty"`
	PanelVersion string `json:"panel_version,omitempty"`
	// JoinedWith: the SHA-256 (base64) of the join token the panel admitted,
	// so the join holds even if agent.env couldn't drop the token.
	JoinedWith string `json:"joined_with,omitempty"`
}

func tokenSum(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func readState() agentState {
	var s agentState
	if raw, err := os.ReadFile(stateFile()); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func writeState(s agentState) {
	raw, _ := json.MarshalIndent(s, "", "  ")
	_ = writeFileAtomic(stateFile(), raw, 0o600)
}

// embeddedReleaseKey is release.pub beside this file: the public half of
// the release signing key (openssl pkey -pubout), whose private half only
// release CI holds (D-106).
//
//go:embed release.pub
var embeddedReleaseKey string

// releasePublicKey is the key the agent pinned at install, else the one it
// was built with. Nil when it has none: then it refuses every upgrade.
func releasePublicKey() ed25519.PublicKey {
	if raw, err := os.ReadFile(releaseFile()); err == nil {
		if k := parseReleaseKey(string(raw)); k != nil {
			return k
		}
	}
	return builtInReleaseKey()
}

func builtInReleaseKey() ed25519.PublicKey { return parseReleaseKey(embeddedReleaseKey) }

// parseReleaseKey reads a PEM public key (openssl pkey -pubout) or its
// base64 DER.
func parseReleaseKey(s string) ed25519.PublicKey {
	s = strings.TrimSpace(s)
	var der []byte
	if b, _ := pem.Decode([]byte(s)); b != nil {
		der = b.Bytes
	} else if d, err := base64.StdEncoding.DecodeString(s); err == nil {
		der = d
	}
	if der == nil {
		return nil
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil
	}
	pk, _ := k.(ed25519.PublicKey)
	return pk
}

// pinReleaseKey writes the built-in key to release.pub once, so later
// upgrades are checked against the key this install started with.
func pinReleaseKey() {
	if _, err := os.Stat(releaseFile()); err == nil {
		return
	}
	k := builtInReleaseKey()
	if k == nil {
		return
	}
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return
	}
	_ = writeFileAtomic(releaseFile(), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644)
}
