package deployment

import (
	"slices"
	"testing"
)

// A compose stack learns its address as TOFT_URL from its first domain,
// unless it sets its own; with no domain it gets none (D-147).
func TestWithServiceURL(t *testing.T) {
	kvs := []string{"A=1"}
	if got := withServiceURL(kvs, []string{"app.example.com", "www.example.com"}, nil); !slices.Equal(got, []string{"A=1", "TOFT_URL=https://app.example.com"}) {
		t.Errorf("first domain: %v", got)
	}
	if !slices.Equal(kvs, []string{"A=1"}) {
		t.Errorf("the caller's slice changed: %v", kvs)
	}
	if got := withServiceURL([]string{"TOFT_URL=https://mine.example"}, []string{"app.example.com"}, nil); !slices.Equal(got, []string{"TOFT_URL=https://mine.example"}) {
		t.Errorf("the service's own wins: %v", got)
	}
	if got := withServiceURL(kvs, nil, nil); !slices.Equal(got, kvs) {
		t.Errorf("no domain: %v", got)
	}
	if got := withServiceURL(kvs, []string{"*.example.com", "app.example.com"}, nil); !slices.Equal(got, []string{"A=1", "TOFT_URL=https://app.example.com"}) {
		t.Errorf("a wildcard is not an address: %v", got)
	}
	if got := withServiceURL(kvs, []string{"*.example.com"}, nil); !slices.Equal(got, kvs) {
		t.Errorf("only a wildcard: %v", got)
	}
	// D-150: the person's own domain before the temporary address, which is
	// http until its certificate is out.
	temp := "app-production.203-0-113-10.sslip.io"
	if got := withServiceURL(kvs, []string{temp, "blog.example.com"}, nil); !slices.Equal(got, []string{"A=1", "TOFT_URL=https://blog.example.com"}) {
		t.Errorf("own domain first: %v", got)
	}
	if got := withServiceURL(kvs, []string{temp}, nil); !slices.Equal(got, []string{"A=1", "TOFT_URL=http://" + temp}) {
		t.Errorf("only the temporary address, certificate pending: %v", got)
	}
	if got := withServiceURL(kvs, []string{temp}, []string{temp}); !slices.Equal(got, []string{"A=1", "TOFT_URL=https://" + temp}) {
		t.Errorf("only the temporary address, certificate out: %v", got)
	}
}
