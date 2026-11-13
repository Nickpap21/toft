package webhook

import (
	"context"
	"testing"
	"time"

	domainPkg "github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type certDomains struct {
	domainPkg.Repository
	domains []domainPkg.Domain
}

func (r certDomains) ListByService(context.Context, string) ([]domainPkg.Domain, error) {
	return r.domains, nil
}

// A deploy routes a temporary name over HTTPS, and gives it as TOFT_URL,
// once its certificate is out (D-150): certificate_at decides it.
func TestInputForCertifiedNames(t *testing.T) {
	at := time.Now()
	h := &Handler{log: zap.NewNop(), domainSvc: domainPkg.NewService(certDomains{domains: []domainPkg.Domain{
		{Hostname: "shop.example.com"},
		{Hostname: "web-production-shop.203-0-113-10.sslip.io", CertificateAt: &at},
	}}, nil, zap.NewNop())}
	in := h.inputFor(context.Background(), &service.Service{ID: "0123456789abcdef"}, "org-a", nil, "")
	assert.Equal(t, []string{"shop.example.com", "web-production-shop.203-0-113-10.sslip.io"}, in.Domains)
	assert.Equal(t, []string{"web-production-shop.203-0-113-10.sslip.io"}, in.AccessControl.Certified)
}
