package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	// Server
	Port string
	Env  string

	// Database
	DatabaseURL string

	// Auth
	JWTSecret     string
	EncryptionKey string
	// EncryptionKeyPrevious (ENCRYPTION_KEY_PREVIOUS) is set only while a
	// rotation runs: values it sealed still open (D-126).
	EncryptionKeyPrevious string

	// Docker
	DockerHost string

	// Traefik / TLS
	// TraefikNetwork (TRAEFIK_NETWORK) is the control plane's network: the
	// proxy and the panel's containers. Under isolated app networks apps
	// never join it.
	TraefikNetwork string
	// ProxyContainer (TRAEFIK_CONTAINER) is the proxy container on every
	// host, connected to each app network.
	ProxyContainer string
	ACMEEmail      string
	BaseDomain     string

	// Logs
	LogLevel string

	// Instance (D-21). Both optional.
	// OperatorEmails (TOFT_OPERATOR_EMAILS, comma-separated) are granted
	// instance operator at startup, but only while the instance has no active
	// operator (break-glass). Grant-only: removing an email never revokes
	// anyone.
	OperatorEmails []string
	// SetupToken (TOFT_SETUP_TOKEN), when set, must be presented by the first
	// registration on an empty instance.
	SetupToken string
	// SetupTokenExpires (TOFT_SETUP_TOKEN_EXPIRES, RFC 3339) ends the setup
	// code: install.sh gives it 30 minutes (D-61). Zero means it never
	// expires (a manual install); a value that doesn't parse has expired.
	SetupTokenExpires time.Time
	// AppsSlice (TOFT_APPS_SLICE) is the systemd slice apps on the panel's
	// server run under; install.sh sets toft-apps.slice (D-26, D-62).
	AppsSlice string
	// PanelIP (TOFT_PANEL_IP) is this server's public IP, which install.sh
	// records: a panel with no domain makes its apps' temporary addresses
	// from it (D-150).
	PanelIP string
	// TraefikDashboard is true when TRAEFIK_DASHBOARD_USERS has users: the
	// panel's routes then publish traefik.<domain> behind them (D-62).
	TraefikDashboard bool
	// PanelHostShell is false when TOFT_PANEL_HOST_SHELL is "off": no root
	// shell on the panel's server from the UI (D-68).
	PanelHostShell bool
	// Git is false when TOFT_GIT is "off": installed without git hosting,
	// so no git container, SSH listener or repositories (D-71).
	Git bool
	// GitSSHPort (TOFT_GIT_SSH_PORT, 2222 by default) is the host port git
	// over SSH is published on, for clone URLs (D-28).
	GitSSHPort int
	// AgentDir (TOFT_AGENT_DIR) holds the release's toft-agent binaries and
	// their signatures, which the panel serves to servers (D-106).
	AgentDir string
	// SetupApp (TOFT_SETUP_APP) is the catalog template install.sh --app
	// asked for; the wizard ends on it (D-59, D-61).
	SetupApp string

	// DeployPolicy (TOFT_DEPLOY_POLICY) is the deploy policy mode (D-19):
	// "enforce" (default) or "warn". Parsed by deploypolicy.ParseMode, which
	// treats an unknown value as enforce.
	DeployPolicy string
	// AppNetworks (TOFT_APP_NETWORKS) says whether apps have their own
	// networks (see EffectiveAppNetworks).
	AppNetworks string
	// AppNetworkMigration (APP_NETWORK_MIGRATION) is what the startup
	// reconcile does under isolated app networks: isolate (default),
	// attach, report or off (netplan.ParseMigrationMode). Ignored under
	// legacy.
	AppNetworkMigration string
	// DataDir (TOFT_DATA_DIR, default /var/paas) holds the files apps on
	// the control-plane server bind-mount: config files and compose
	// releases. docker-compose.yml mounts it into the backend at the same
	// path as on the host, since Docker reads bind-mount sources on the host.
	DataDir string
	// AppNetworkPool (TOFT_APP_NETWORK_POOL) is the address pool the
	// provisioner writes to daemon.json on fresh servers under isolated app
	// networks (netplan.ParseAddressPool; default 10.203.0.0/16).
	AppNetworkPool string
	// TrustedProxies (TOFT_TRUSTED_PROXIES, comma-separated CIDRs) are the
	// peers whose X-Forwarded-For is believed when working out the client's
	// address, which the per-IP rate limiters and the request log use. The
	// default is the private ranges and loopback: the backend is reached
	// through Traefik on the control-plane network. A header from any other
	// peer is ignored and the peer's own address is used.
	TrustedProxies []string
	// MaxConcurrentBuilds (TOFT_MAX_CONCURRENT_BUILDS, default 2) is how many
	// deploys may clone and build at once on this backend; the rest wait
	// for a slot, and say so in their log. Clones and builds run inside the
	// backend's container and its memory limit.
	MaxConcurrentBuilds int
	// AuditRetentionDays (TOFT_AUDIT_RETENTION_DAYS, default 365) is how long
	// audit log entries are kept; 0 keeps them forever.
	AuditRetentionDays int
}

// DefaultTrustedProxies: loopback and the private ranges (RFC 1918 and
// fc00::/7), where Traefik and the control-plane network live.
var DefaultTrustedProxies = []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}

// App network modes (TOFT_APP_NETWORKS).
const (
	// AppNetworksLegacy: apps share the control plane's network, so
	// interactive deploys to the control-plane server are operator-only.
	AppNetworksLegacy = "legacy"
	// AppNetworksIsolated: every environment has its own network and apps
	// never join the control plane's (D-19, D-26, package C). This lifts
	// the control-plane stopgap.
	AppNetworksIsolated = "isolated"
)

// EffectiveAppNetworks returns the app network mode in force and whether the
// configured value was accepted. Empty means isolated, the default since
// D-51; an unknown value is not accepted and means legacy, so a typo keeps
// the D-19 stopgap on and starts no network migration.
func (c *Config) EffectiveAppNetworks() (mode string, accepted bool) {
	switch v := strings.ToLower(strings.TrimSpace(c.AppNetworks)); v {
	case "", AppNetworksIsolated:
		return AppNetworksIsolated, true
	case AppNetworksLegacy:
		return AppNetworksLegacy, true
	}
	return AppNetworksLegacy, false
}

// Load reads environment variables and returns a populated Config.
// It panics on any missing required variable to fail fast at startup.
func Load() *Config {
	return &Config{
		Port:                  getEnv("PORT", "8080"),
		Env:                   getEnv("ENV", "development"),
		DatabaseURL:           mustGetEnv("DATABASE_URL"),
		JWTSecret:             notPlaceholder("JWT_SECRET", mustGetSecret("JWT_SECRET")),
		EncryptionKey:         notPlaceholder("ENCRYPTION_KEY", mustGetSecret("ENCRYPTION_KEY")),
		EncryptionKeyPrevious: optionalSecret("ENCRYPTION_KEY_PREVIOUS"),
		DockerHost:            getEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),
		TraefikNetwork:        getEnv("TRAEFIK_NETWORK", "paas-network"),
		ProxyContainer:        getEnv("TRAEFIK_CONTAINER", "paas-traefik"),
		ACMEEmail:             getEnv("ACME_EMAIL", "admin@paas.local"),
		BaseDomain:            getEnv("BASE_DOMAIN", "paas.local"),
		LogLevel:              getEnv("LOG_LEVEL", "info"),
		OperatorEmails:        SplitList(os.Getenv("TOFT_OPERATOR_EMAILS")),
		SetupToken:            strings.TrimSpace(os.Getenv("TOFT_SETUP_TOKEN")),
		SetupApp:              strings.TrimSpace(os.Getenv("TOFT_SETUP_APP")),
		TraefikDashboard:      strings.TrimSpace(os.Getenv("TRAEFIK_DASHBOARD_USERS")) != "",
		PanelHostShell:        !strings.EqualFold(strings.TrimSpace(os.Getenv("TOFT_PANEL_HOST_SHELL")), "off"),
		Git:                   !strings.EqualFold(strings.TrimSpace(os.Getenv("TOFT_GIT")), "off"),
		GitSSHPort:            positiveInt(os.Getenv("TOFT_GIT_SSH_PORT"), 2222),
		AppsSlice:             strings.TrimSpace(os.Getenv("TOFT_APPS_SLICE")),
		PanelIP:               strings.TrimSpace(os.Getenv("TOFT_PANEL_IP")),
		SetupTokenExpires:     parseExpiry(os.Getenv("TOFT_SETUP_TOKEN_EXPIRES")),
		DeployPolicy:          strings.TrimSpace(os.Getenv("TOFT_DEPLOY_POLICY")),
		AppNetworks:           getEnv("TOFT_APP_NETWORKS", AppNetworksIsolated),
		DataDir:               getEnv("TOFT_DATA_DIR", "/var/paas"),
		AgentDir:              getEnv("TOFT_AGENT_DIR", "/usr/share/toft/agent"),

		AppNetworkMigration: strings.TrimSpace(os.Getenv("APP_NETWORK_MIGRATION")),
		AppNetworkPool:      strings.TrimSpace(os.Getenv("TOFT_APP_NETWORK_POOL")),
		TrustedProxies:      trustedProxies(os.Getenv("TOFT_TRUSTED_PROXIES")),
		MaxConcurrentBuilds: positiveInt(os.Getenv("TOFT_MAX_CONCURRENT_BUILDS"), 2),
		AuditRetentionDays:  nonNegativeInt(os.Getenv("TOFT_AUDIT_RETENTION_DAYS"), 365),
	}
}

// nonNegativeInt parses raw as an integer >= 0, or returns fallback.
func nonNegativeInt(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// positiveInt parses raw as a positive integer, or returns fallback.
func positiveInt(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func trustedProxies(raw string) []string {
	if list := SplitList(raw); list != nil {
		return list
	}
	return DefaultTrustedProxies
}

// SplitList splits a comma-separated value, trimming spaces and dropping
// empty items. It returns nil when nothing remains.
func SplitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// minSecretLen: JWT_SECRET signs every session and ENCRYPTION_KEY is hashed
// into the AES key, so a short value is a short key. The documented
// `openssl rand -hex 32` gives 64 characters.
const minSecretLen = 32

// parseExpiry reads an RFC 3339 time: zero when empty, and the Unix epoch
// (long expired) when it doesn't parse, so a broken value fails closed.
func parseExpiry(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Unix(0, 0)
	}
	return t
}

func mustGetSecret(key string) string {
	v := mustGetEnv(key)
	if len(v) < minSecretLen {
		panic(fmt.Sprintf("%s must be at least %d characters (generate one with: openssl rand -hex 32)", key, minSecretLen))
	}
	return v
}

// optionalSecret is "" when key is unset or empty, and otherwise held to
// mustGetSecret's length.
func optionalSecret(key string) string {
	if os.Getenv(key) == "" {
		return ""
	}
	return mustGetSecret(key)
}

// IsPlaceholder reports whether v is the .env examples' placeholder, which
// is long enough and known to anyone with the repository.
func IsPlaceholder(v string) bool {
	l := strings.ToLower(v)
	return strings.Contains(l, "change-me") || strings.Contains(l, "change_me")
}

// notPlaceholder refuses a placeholder JWT_SECRET, which only signs everyone
// out when replaced, or ENCRYPTION_KEY (D-126): the placeholder may be
// ENCRYPTION_KEY_PREVIOUS, so that the start seals every secret again under
// a new key (toft rotate-encryption-key does both on a panel host).
func notPlaceholder(key, v string) string {
	if !IsPlaceholder(v) {
		return v
	}
	if key == "ENCRYPTION_KEY" {
		panic("ENCRYPTION_KEY is still the example's placeholder: on a panel host run toft rotate-encryption-key; " +
			"elsewhere set a new one (openssl rand -hex 32) and the placeholder as ENCRYPTION_KEY_PREVIOUS")
	}
	panic(fmt.Sprintf("%s is still the example's placeholder (generate one with: openssl rand -hex 32)", key))
}

func mustGetEnv(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return v
}
