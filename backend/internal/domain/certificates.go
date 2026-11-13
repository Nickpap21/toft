package domain

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"go.uber.org/zap"
)

// CertificateEvery is how often the panel looks for the certificates of
// routed names that have none yet (D-150).
const CertificateEvery = 5 * time.Minute

// CertificateError is why hostname doesn't answer on 443 with a certificate
// valid for it, from a CA the panel trusts (Traefik's own default
// certificate, while Let's Encrypt hasn't issued one, is not), or nil when
// it does. Names are the tenants', so only public addresses are dialled.
func CertificateError(ctx context.Context, hostname string) error {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second, Control: netcheck.PublicOnly}, Config: &tls.Config{ServerName: hostname, MinVersion: tls.VersionTLS12}}
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(hostname, "443"))
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// CheckCertificates records, for each routed name still waiting, whether a
// valid certificate answers now. One name at a time: a handful wait at once,
// and a slow one only delays the next round. Why a name has none is logged
// when it changes (a first failure, a different one), and the certificate
// when it is recorded; not the same failure every round.
func (s *Service) CheckCertificates(ctx context.Context) {
	if s.failing == nil {
		s.failing = map[string]string{}
	}
	pending, err := s.repo.AwaitingCertificate(ctx)
	if err != nil {
		if s.failing[""] != err.Error() {
			s.log.Warn("list domains awaiting a certificate", zap.Error(err))
		}
		s.failing[""] = err.Error()
		return
	}
	failing := map[string]string{}
	for _, d := range pending {
		if ctx.Err() != nil {
			return
		}
		if err := s.certificateErr(ctx, d.Hostname); err != nil {
			if s.failing[d.ID] != err.Error() {
				s.log.Warn("no valid certificate answers for a domain yet", zap.String("hostname", d.Hostname), zap.Error(err))
			}
			failing[d.ID] = err.Error()
			continue
		}
		s.log.Info("a domain's certificate is out", zap.String("hostname", d.Hostname))
		recordCertificate(ctx, s.repo, s.redeployer, s.log, d)
	}
	s.failing = failing
}

// Redeployer redeploys a running service on the image it runs now, so the
// routes and TOFT_URL a temporary name got while its certificate was
// pending switch to HTTPS (D-150). It returns at once: the deploy runs in
// the background, as every deploy.
type Redeployer interface {
	RedeployForCertificate(ctx context.Context, serviceID string)
}

// recordCertificate records that a valid certificate answered for d and,
// the first time it is recorded for a temporary name, has its service
// redeployed: once per name, whichever check saw it first.
func recordCertificate(ctx context.Context, repo Repository, redeployer Redeployer, log *zap.Logger, d Domain) {
	recorded, err := repo.SetCertificate(ctx, d.ID)
	if err != nil {
		log.Warn("record a domain's certificate", zap.String("hostname", d.Hostname), zap.Error(err))
		return
	}
	if recorded && redeployer != nil && IsTemporary(d.Hostname) {
		redeployer.RedeployForCertificate(ctx, d.ServiceID)
	}
}

// RunCertificateChecks checks every CertificateEvery until ctx ends.
func (s *Service) RunCertificateChecks(ctx context.Context) {
	t := time.NewTicker(CertificateEvery)
	defer t.Stop()
	for {
		s.CheckCertificates(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
