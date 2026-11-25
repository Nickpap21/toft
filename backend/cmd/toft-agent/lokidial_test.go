package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Loki address naming a host that resolves to this server is refused
// (D-148: 127.0.0.1 is another workspace's Loki on a shared server); the
// 127.0.0.1 address Toft gives the catalog Loki itself is dialled, and no
// redirect is followed to one.
func TestLokiClientRefusesANameForThisServer(t *testing.T) {
	pushed := 0
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/loki/api/v1/push", http.StatusTemporaryRedirect)
			return
		}
		pushed++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer loki.Close()
	u, err := url.Parse(loki.URL)
	require.NoError(t, err)
	port := u.Port()

	names := map[string][]netip.Addr{
		"loki.example":   {netip.MustParseAddr("127.0.0.1")},
		"mapped.example": {netip.MustParseAddr("::ffff:127.0.0.1")},
		"any.example":    {netip.MustParseAddr("0.0.0.0")},
	}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if ips, ok := names[host]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}
	c := lokiClient(lookup)
	post := func(raw string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, raw, strings.NewReader("{}"))
		require.NoError(t, err)
		return c.Do(req)
	}

	for name := range names {
		_, err := post("http://" + name + ":" + port + "/loki/api/v1/push")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "is this server", name)
	}
	assert.Zero(t, pushed, "nothing reached the loopback Loki by a name")

	resp, err := post("http://127.0.0.1:" + port + "/loki/api/v1/push")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, 1, pushed, "the address Toft gives is dialled")

	resp, err = post("http://127.0.0.1:" + port + "/redirect")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode, "a redirect is an answer, not followed")
	assert.Equal(t, 1, pushed)
}
