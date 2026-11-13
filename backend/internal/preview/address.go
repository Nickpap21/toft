package preview

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/Nickpap21/toft/backend/pkg/netcheck"
)

// ErrNoPublicAddress: the service's server has no public IPv4 address for a
// name to point at (D-150).
var ErrNoPublicAddress = errors.New("the server has no public address")

// Address is the name a new web service gets when it is created (D-150):
// <service>-<environment>-<project>.<a-b-c-d>.sslip.io, the project left
// out when it is the service's name. Never under the project's preview
// domain: that wildcard is for previews, and a production service's first
// address must not be a preview's name. A name taken (taken reports it)
// gets -2, -3.
func (s *Service) Address(ctx context.Context, project, environment, service, serverID string, taken func(string) (bool, error)) (string, error) {
	ip, err := s.hosts.Address(ctx, serverID)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "", ErrNoPublicAddress // the panel's domain doesn't resolve: nothing to point a name at
	}
	if err != nil {
		return "", fmt.Errorf("the server's address: %w", err)
	}
	if a, err := netip.ParseAddr(ip); err != nil || !a.Unmap().Is4() || !netcheck.IsPublic(a) {
		return "", ErrNoPublicAddress
	}
	name := func(n int) string {
		parts := []string{service, environment}
		if project != service {
			parts = append(parts, project)
		}
		if n > 1 {
			parts = append(parts, strconv.Itoa(n))
		}
		return label(strings.Join(parts, "-")) + "." + strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
	}
	for n := 1; n <= 20; n++ {
		h := name(n)
		used, err := taken(h)
		if err != nil {
			return "", fmt.Errorf("check %s: %w", h, err)
		}
		if !used {
			return h, nil
		}
	}
	return "", fmt.Errorf("%s and the next 19 names are taken", name(1))
}
