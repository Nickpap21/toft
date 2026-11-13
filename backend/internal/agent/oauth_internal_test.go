package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/netcheck"
)

// A metadata document on the panel's own network is never fetched (I7): the
// address dialled is checked, so the server below is never reached.
func TestClientMetadataRefusesPrivateAddresses(t *testing.T) {
	reached := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()
	_, _, err := fetchClientMetadata(context.Background(), srv.URL+"/client.json")
	if !errors.Is(err, netcheck.ErrPrivateAddress) || reached {
		t.Fatalf("got %v (reached %v), want %v", err, reached, netcheck.ErrPrivateAddress)
	}
}
