package server

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DNSConfig is a server's DNS provider, decrypted: a lego provider name
// (https://go-acme.github.io/lego/dns/) and its variables, such as
// CF_DNS_API_TOKEN for cloudflare. Wildcard domains on the server need it.
type DNSConfig struct {
	Provider    string
	Credentials map[string]string
	UpdatedAt   time.Time
}

// DNSProviderDTO is the API's view: the variable names, never their values.
type DNSProviderDTO struct {
	Provider       string    `json:"provider"`
	CredentialKeys []string  `json:"credential_keys"`
	UpdatedAt      time.Time `json:"updated_at"`
}

var (
	// ErrNoDNSProvider is a server without a DNS provider.
	ErrNoDNSProvider = errors.New("this server has no DNS provider")
	// ErrInvalidDNSProvider is a provider name or a variable the platform
	// won't pass to Traefik.
	ErrInvalidDNSProvider = errors.New(`the provider must be a lego provider name (lowercase letters and digits, such as "cloudflare"), with 1 to 10 variables named in capitals (such as CF_DNS_API_TOKEN), each with a value`)
)

var (
	dnsProviderPattern = regexp.MustCompile(`^[a-z0-9]{1,40}$`)
	// dnsKeyPattern: the name becomes <name>_FILE in Traefik's environment
	// and a file name on the server, so nothing a shell or a path could
	// read differently.
	dnsKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// validateDNSConfig checks a provider and its variables. Names starting
// with TRAEFIK_ are refused: Traefik reads its own configuration from those.
func validateDNSConfig(provider string, creds map[string]string) error {
	if !dnsProviderPattern.MatchString(provider) || len(creds) == 0 || len(creds) > 10 {
		return ErrInvalidDNSProvider
	}
	for k, v := range creds {
		if !dnsKeyPattern.MatchString(k) || strings.HasPrefix(k, "TRAEFIK_") || strings.HasSuffix(k, "_FILE") ||
			strings.TrimSpace(v) == "" || len(v) > 4096 {
			return ErrInvalidDNSProvider
		}
	}
	return nil
}

func (c *DNSConfig) dto() *DNSProviderDTO {
	keys := make([]string, 0, len(c.Credentials))
	for k := range c.Credentials {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &DNSProviderDTO{Provider: c.Provider, CredentialKeys: keys, UpdatedAt: c.UpdatedAt}
}

// GetDNSProvider returns the server's DNS provider without its values
// (ErrNoDNSProvider).
func (s *Service) GetDNSProvider(ctx context.Context, serverID string) (*DNSProviderDTO, error) {
	cfg, err := s.repo.GetDNSProvider(ctx, serverID)
	if err != nil {
		return nil, err
	}
	return cfg.dto(), nil
}

// SetDNSProvider sets the server's DNS provider (ErrInvalidDNSProvider). It
// takes effect at the server's next Provision.
func (s *Service) SetDNSProvider(ctx context.Context, serverID, provider string, creds map[string]string) (*DNSProviderDTO, error) {
	if err := validateDNSConfig(provider, creds); err != nil {
		return nil, err
	}
	cfg, err := s.repo.SetDNSProvider(ctx, serverID, provider, creds)
	if err != nil {
		return nil, err
	}
	return cfg.dto(), nil
}

// DeleteDNSProvider removes the server's DNS provider; its next Provision
// removes the credentials from the server.
func (s *Service) DeleteDNSProvider(ctx context.Context, serverID string) error {
	return s.repo.DeleteDNSProvider(ctx, serverID)
}

// HasDNSProvider reports whether a server can get wildcard certificates.
// Satisfied for deployment.DNSProviderChecker.
func (s *Service) HasDNSProvider(ctx context.Context, serverID string) (bool, error) {
	_, err := s.repo.GetDNSProvider(ctx, serverID)
	if errors.Is(err, ErrNoDNSProvider) {
		return false, nil
	}
	return err == nil, err
}
