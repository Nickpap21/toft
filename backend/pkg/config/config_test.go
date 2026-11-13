package config_test

import (
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestSplitList(t *testing.T) {
	assert.Equal(t, []string{"a@x", "b@y"}, config.SplitList("a@x, b@y,,"))
	assert.Equal(t, []string{"a@x"}, config.SplitList("  a@x  "))
	assert.Nil(t, config.SplitList(""))
	assert.Nil(t, config.SplitList(" , ,"))
}

func TestLoad_InstanceSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")
	t.Setenv("TOFT_OPERATOR_EMAILS", "ada@example.com, bob@example.com")
	t.Setenv("TOFT_SETUP_TOKEN", "  tok  ")

	cfg := config.Load()

	assert.Equal(t, []string{"ada@example.com", "bob@example.com"}, cfg.OperatorEmails)
	assert.Equal(t, "tok", cfg.SetupToken)
	assert.True(t, cfg.SetupTokenExpires.IsZero(), "no expiry: a manual install's token never expires")

	t.Setenv("TOFT_SETUP_TOKEN_EXPIRES", "2026-10-17T14:35:00Z")
	t.Setenv("TOFT_SETUP_APP", " typesense ")
	cfg = config.Load()
	assert.Equal(t, time.Date(2026, 10, 17, 14, 35, 0, 0, time.UTC), cfg.SetupTokenExpires)
	assert.Equal(t, "typesense", cfg.SetupApp)

	// A value that doesn't parse has expired: it fails closed.
	t.Setenv("TOFT_SETUP_TOKEN_EXPIRES", "in 30 minutes")
	assert.True(t, config.Load().SetupTokenExpires.Before(time.Now()))
}

func TestLoad_DeployPolicyAndAppNetworks(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")

	cfg := config.Load()
	assert.Equal(t, "", cfg.DeployPolicy)
	mode, ok := cfg.EffectiveAppNetworks()
	assert.Equal(t, config.AppNetworksIsolated, mode, "isolated is the default (D-51)")
	assert.True(t, ok)

	t.Setenv("TOFT_APP_NETWORKS", "")
	mode, ok = config.Load().EffectiveAppNetworks()
	assert.Equal(t, config.AppNetworksIsolated, mode, "set but empty is the default too")
	assert.True(t, ok)

	t.Setenv("TOFT_APP_NETWORKS", "legacy")
	mode, ok = config.Load().EffectiveAppNetworks()
	assert.Equal(t, config.AppNetworksLegacy, mode)
	assert.True(t, ok)

	t.Setenv("TOFT_DEPLOY_POLICY", " warn ")
	t.Setenv("TOFT_APP_NETWORKS", "isolated")
	cfg = config.Load()
	assert.Equal(t, "warn", cfg.DeployPolicy)
	mode, ok = cfg.EffectiveAppNetworks()
	assert.Equal(t, config.AppNetworksIsolated, mode)
	assert.True(t, ok)

	t.Setenv("TOFT_APP_NETWORKS", " Isolated ")
	mode, ok = config.Load().EffectiveAppNetworks()
	assert.Equal(t, config.AppNetworksIsolated, mode)
	assert.True(t, ok)

	t.Setenv("TOFT_APP_NETWORKS", "isolate")
	mode, ok = config.Load().EffectiveAppNetworks()
	assert.Equal(t, config.AppNetworksLegacy, mode, "a typo keeps the stopgap on")
	assert.False(t, ok)
}

func TestLoad_AppNetworkSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")
	cfg := config.Load()
	assert.Equal(t, "paas-traefik", cfg.ProxyContainer)
	assert.Equal(t, "paas-network", cfg.TraefikNetwork)
	assert.Equal(t, "", cfg.AppNetworkMigration)
	assert.Equal(t, "", cfg.AppNetworkPool)

	t.Setenv("TRAEFIK_CONTAINER", "proxy")
	t.Setenv("APP_NETWORK_MIGRATION", " report ")
	t.Setenv("TOFT_APP_NETWORK_POOL", "10.50.0.0/16")
	cfg = config.Load()
	assert.Equal(t, "proxy", cfg.ProxyContainer)
	assert.Equal(t, "report", cfg.AppNetworkMigration)
	assert.Equal(t, "10.50.0.0/16", cfg.AppNetworkPool)
}

func TestLoad_TrustedProxies(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")

	assert.Equal(t, config.DefaultTrustedProxies, config.Load().TrustedProxies)

	t.Setenv("TOFT_TRUSTED_PROXIES", "10.9.0.0/16, 127.0.0.1/32")
	assert.Equal(t, []string{"10.9.0.0/16", "127.0.0.1/32"}, config.Load().TrustedProxies)
}

func TestLoad_RefusesShortSecrets(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "short")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")
	assert.PanicsWithValue(t, "JWT_SECRET must be at least 32 characters (generate one with: openssl rand -hex 32)", func() { config.Load() })
}

func TestLoad_RefusesThePlaceholders(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "change-me-to-a-long-random-secret")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")
	assert.PanicsWithValue(t, "JWT_SECRET is still the example's placeholder (generate one with: openssl rand -hex 32)", func() { config.Load() })

	// ENCRYPTION_KEY too (D-126): the placeholder may only be the previous key.
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("ENCRYPTION_KEY", "change-me-to-a-random-32-char-string")
	assert.PanicsWithValue(t, "ENCRYPTION_KEY is still the example's placeholder: on a panel host run toft rotate-encryption-key; "+
		"elsewhere set a new one (openssl rand -hex 32) and the placeholder as ENCRYPTION_KEY_PREVIOUS", func() { config.Load() })
}

// ENCRYPTION_KEY_PREVIOUS is set only while a rotation runs (D-126): unset
// or empty is none, and a set one is held to the length rule.
func TestLoad_PreviousEncryptionKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210")
	assert.Empty(t, config.Load().EncryptionKeyPrevious)

	t.Setenv("ENCRYPTION_KEY_PREVIOUS", "")
	assert.Empty(t, config.Load().EncryptionKeyPrevious)

	t.Setenv("ENCRYPTION_KEY_PREVIOUS", "change-me-to-a-random-32-char-string")
	assert.Equal(t, "change-me-to-a-random-32-char-string", config.Load().EncryptionKeyPrevious, "the placeholder is what a rotation replaces")

	t.Setenv("ENCRYPTION_KEY_PREVIOUS", "short")
	assert.PanicsWithValue(t, "ENCRYPTION_KEY_PREVIOUS must be at least 32 characters (generate one with: openssl rand -hex 32)", func() { config.Load() })
}
