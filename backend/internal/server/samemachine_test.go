package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One machine used by two workspaces is two server entries. Provisioning one
// whose DNS provider setting differs from the other's would replace the
// machine's one Traefik's wildcard setup, so it is refused before anything
// changes; with the same setting it goes ahead.
func TestProvisionRefusesWhenTheSameMachineHasAnotherDNSSetting(t *testing.T) {
	const other = "srv-2"
	cf := map[string]string{"CF_DNS_API_TOKEN": "tok"}
	setup := func(t *testing.T) (*Service, *fakeRepo) {
		repo := newFakeRepo(
			&Server{ID: testServerID, OrgID: "org-a", Name: "web-1", IPAddress: "203.0.113.7", Port: 2222},
			&Server{ID: other, OrgID: "org-b", Name: "web-1", IPAddress: "203.0.113.7", Port: 2222},
		)
		return NewService(repo, ""), repo
	}
	ctx := context.Background()

	t.Run("the other entry has wildcards, this one not", func(t *testing.T) {
		s, repo := setup(t)
		_, err := repo.SetDNSProvider(ctx, other, "cloudflare", cf)
		require.NoError(t, err)
		err = s.Provision(ctx, testServerID, "", func(string) {})
		assert.ErrorIs(t, err, ErrSameMachineDNS)
		assert.Empty(t, repo.statuses, "refused before anything changed")
	})
	t.Run("different credentials", func(t *testing.T) {
		s, repo := setup(t)
		_, _ = repo.SetDNSProvider(ctx, other, "cloudflare", cf)
		_, _ = repo.SetDNSProvider(ctx, testServerID, "cloudflare", map[string]string{"CF_DNS_API_TOKEN": "older"})
		assert.ErrorIs(t, s.Provision(ctx, testServerID, "", func(string) {}), ErrSameMachineDNS)
	})
	t.Run("the same setting goes ahead", func(t *testing.T) {
		s, repo := setup(t)
		_, _ = repo.SetDNSProvider(ctx, other, "cloudflare", cf)
		_, _ = repo.SetDNSProvider(ctx, testServerID, "cloudflare", map[string]string{"CF_DNS_API_TOKEN": "tok"})
		err := s.Provision(ctx, testServerID, "", func(string) {})
		assert.NotErrorIs(t, err, ErrSameMachineDNS, "it gets as far as SSH, which this test has none of")
	})
	t.Run("another machine is not checked", func(t *testing.T) {
		s, repo := setup(t)
		repo.servers[other].IPAddress = "198.51.100.9"
		_, _ = repo.SetDNSProvider(ctx, other, "cloudflare", cf)
		assert.NotErrorIs(t, s.Provision(ctx, testServerID, "", func(string) {}), ErrSameMachineDNS)
	})
}

func TestSameMachine(t *testing.T) {
	a := Server{IPAddress: "203.0.113.7", Port: 22, HostKey: "ssh-ed25519 AAAA"}
	assert.True(t, sameMachine(a, Server{IPAddress: " 203.0.113.7 ", Port: 22}))
	assert.False(t, sameMachine(a, Server{IPAddress: "203.0.113.7", Port: 2222}), "another port can be another machine behind NAT")
	assert.True(t, sameMachine(a, Server{IPAddress: "web.example.com", Port: 2222, HostKey: "ssh-ed25519 AAAA"}), "the same host key is the same machine")
	assert.False(t, sameMachine(Server{IPAddress: "a"}, Server{IPAddress: "b"}), "two unpinned keys are not a match")

	assert.True(t, sameDNS(nil, nil))
	assert.False(t, sameDNS(nil, &DNSConfig{Provider: "cloudflare"}))
	assert.True(t, sameDNS(&DNSConfig{Provider: "x", Credentials: map[string]string{"A": "1"}}, &DNSConfig{Provider: "x", Credentials: map[string]string{"A": "1"}}))
}
