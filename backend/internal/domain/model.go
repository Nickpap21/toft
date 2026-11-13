package domain

import (
	"errors"
	"strings"
	"time"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusVerified Status = "verified"
	StatusFailed   Status = "failed"
)

// Domain represents a custom domain attached to a Service.
type Domain struct {
	ID         string    `json:"id"`
	ServiceID  string    `json:"service_id"`
	Hostname   string    `json:"hostname"`
	TLSEnabled bool      `json:"tls_enabled"`
	Status     Status    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// CreatedBy is who added it: a name, "" when not recorded (before 0023).
	CreatedBy string `json:"created_by"`
	// RoutedAt is the first successful deploy that routed it; nil until then.
	RoutedAt *time.Time `json:"routed_at"`
	// CertificateAt is when a valid certificate first answered for it; nil
	// while one is pending (D-150).
	CertificateAt *time.Time `json:"certificate_at"`
	// Temporary: under sslip.io (D-150).
	Temporary bool `json:"temporary"`
}

// WorkspaceDomain is a domain on the workspace's Domains page (D-48),
// with where it routes to.
type WorkspaceDomain struct {
	ID        string     `json:"id"`
	Hostname  string     `json:"hostname"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	RoutedAt  *time.Time `json:"routed_at"`
	// CertificateAt and Temporary as on Domain.
	CertificateAt *time.Time `json:"certificate_at"`
	Temporary     bool       `json:"temporary"`
	ServiceID     string     `json:"service_id"`
	Service       string     `json:"service_name"`
	// Scope is the service's: environment, project or org (a workspace
	// service, which has no project).
	Scope       string `json:"scope"`
	ProjectID   string `json:"project_id"`
	Project     string `json:"project_name"`
	Environment string `json:"environment_name"`
	// ServerID is "" for the panel host.
	ServerID string `json:"server_id"`
}

// IsTemporary reports whether a name is under sslip.io: an address for a
// first look, whose certificates Let's Encrypt limits for everyone at once
// (D-150).
func IsTemporary(hostname string) bool {
	return strings.HasSuffix(hostname, ".sslip.io")
}

// TemporarySuffix is what ends the temporary names pointing at an IPv4
// address: ".203-0-113-10.sslip.io".
func TemporarySuffix(ip string) string {
	return "." + strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

// MoveTemporary is hostname moved from the server at fromIP to the one at
// toIP (D-150): a temporary name pointing at fromIP points at toIP; any
// other name is unchanged.
func MoveTemporary(hostname, fromIP, toIP string) string {
	if fromIP == "" || toIP == "" {
		return hostname
	}
	if label, ok := strings.CutSuffix(hostname, TemporarySuffix(fromIP)); ok {
		return label + TemporarySuffix(toIP)
	}
	return hostname
}

var (
	// ErrNotFound is a domain that is not the service's.
	ErrNotFound = errors.New("domain not found")
	// ErrNoPort: Traefik routes a domain to the port a service listens on.
	ErrNoPort = errors.New("this service has no port, so it can't take a domain: set its port in Settings first")
	// ErrHostnameTaken: a hostname belongs to one service on the instance.
	ErrHostnameTaken = errors.New("this hostname already belongs to a service: each hostname can belong to only one service on the instance")
)

type CreateInput struct {
	// Hostname is checked by ValidHostname (lowercased first): a DNS name,
	// or a wildcard "*.example.com".
	Hostname   string `json:"hostname" binding:"required,max=253"`
	TLSEnabled bool   `json:"tls_enabled"`
}
