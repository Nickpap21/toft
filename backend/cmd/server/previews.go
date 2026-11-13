package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/docker"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/webhook"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/Nickpap21/toft/backend/pkg/reservation"
)

// Adapters between the previews module (D-97) and the modules it uses or
// that use it.

type previewServices struct{ svc *service.ServiceManager }

func (p previewServices) CopyForPreview(ctx context.Context, in preview.CopyInput) (string, error) {
	return p.svc.CopyForPreview(ctx, service.PreviewCopy{OriginalID: in.OriginalID, EnvironmentID: in.EnvironmentID,
		Branch: in.Branch, ExtraMemoryMB: in.ExtraMemoryMB, Untrusted: in.Untrusted, Hostnames: in.Hostnames})
}
func (p previewServices) Delete(ctx context.Context, id string) error { return p.svc.Delete(ctx, id) }
func (p previewServices) Stop(ctx context.Context, id string) error   { return p.svc.Stop(ctx, id) }

type previewDeployer struct{ h *webhook.Handler }

func (d previewDeployer) DeployPreview(ctx context.Context, in preview.DeployInput) error {
	return d.h.DeployPreview(ctx, webhook.PreviewDeploy{ServiceID: in.ServiceID, SHA: in.SHA, TriggeredBy: in.TriggeredBy,
		Message: in.Message, ActorID: in.ActorID, EventID: in.EventID, EnvVars: in.EnvVars, CertDomains: in.CertDomains,
		CertResolver: in.CertResolver})
}

type previewCode struct{ c *code.Service }

func (p previewCode) AddEvent(ctx context.Context, pullID, actorID, kind string, data map[string]any) {
	p.c.AddPullEvent(ctx, pullID, actorID, kind, data)
}
func (p previewCode) ReportPreviewCheck(ctx context.Context, repoID, sha, svc, state, description string) {
	p.c.ReportPreviewCheck(ctx, repoID, sha, svc, state, description)
}
func (p previewCode) ReadFile(ctx context.Context, projectID, repoID, sha, path string) ([]byte, error) {
	return p.c.ReadFile(ctx, projectID, repoID, sha, path)
}
func (p previewCode) ChangedFiles(ctx context.Context, projectID, repoID, base, head string) ([]preview.ChangedFile, bool, error) {
	cmp, err := p.c.Compare(ctx, projectID, repoID, base, head)
	if err != nil {
		return nil, false, fmt.Errorf("compare %s with %s: %w", head, base, err)
	}
	out := make([]preview.ChangedFile, 0, len(cmp.Files))
	for _, f := range cmp.Files {
		out = append(out, preview.ChangedFile{Path: f.Path, OldPath: f.OldPath})
	}
	return out, cmp.FilesTruncated, nil
}

type previewVars struct{ v *envvar.Service }

func (p previewVars) All(ctx context.Context, id string) ([]string, error) {
	return p.v.ListAllForDeployment(ctx, id)
}
func (p previewVars) Plain(ctx context.Context, id string) ([]string, error) {
	return p.v.ListPlainForDeployment(ctx, id)
}

// previewHosts: a server's address is its own; the panel's is what its
// domain resolves to, or with no domain the IP install.sh recorded.
type previewHosts struct {
	servers *server.Service
	domain  func() string
	panelIP string
}

func (p previewHosts) Address(ctx context.Context, serverID string) (string, error) {
	if serverID != "" {
		srv, err := p.servers.GetServer(ctx, serverID)
		if err != nil {
			return "", err
		}
		return srv.IPAddress, nil
	}
	d := p.domain()
	if d == "" {
		return p.panelIP, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIP(lctx, "ip4", d)
	if err != nil || len(addrs) == 0 {
		return "", err
	}
	return addrs[0].String(), nil
}
func (p previewHosts) HasDNSProvider(ctx context.Context, serverID string) (bool, error) {
	if serverID == "" {
		return false, nil
	}
	return p.servers.HasDNSProvider(ctx, serverID)
}
func (p previewHosts) UntrustedReady(ctx context.Context, serverID string) string {
	return p.servers.UntrustedReady(ctx, serverID)
}

// lookupIPv4 is what a preview domain's wildcard record resolves to.
func lookupIPv4(ctx context.Context, host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out, nil
}

type previewEvictor struct{ p *preview.Service }

func (e previewEvictor) RoomCandidates(ctx context.Context, serverID, orgID string) ([]deployment.RoomCandidate, error) {
	cs, err := e.p.RoomCandidates(ctx, serverID, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]deployment.RoomCandidate, 0, len(cs))
	for _, c := range cs {
		out = append(out, deployment.RoomCandidate{PullID: c.PullID, ServiceIDs: c.ServiceIDs})
	}
	return out, nil
}
func (e previewEvictor) StopForRoom(ctx context.Context, pullID, note, actorID string) error {
	return e.p.StopForRoom(ctx, pullID, note, preview.Actor{UserID: actorID})
}

// panelLedger is the panel's own server's: its apps share (D-62) and what
// its services reserve.
type panelLedger struct {
	local    *docker.LocalOrchestrator
	services *service.PostgresRepository
}

func (l panelLedger) AppsMemoryMB(ctx context.Context) (float64, error) {
	cores, mem, _, _, err := l.local.HostSize(ctx)
	if err != nil {
		return 0, err
	}
	_, apps := reservation.Apps(cores, mem)
	return float64(apps) / (1 << 20), nil
}
func (l panelLedger) AllocatedMemoryMB(ctx context.Context) (float64, error) {
	return l.services.SumAllocatedOnPanel(ctx)
}

type previewApprovals struct{ p *preview.Service }

func (a previewApprovals) ApprovalsNeeded(ctx context.Context, orgID string) ([]inbox.PreviewApproval, error) {
	as, err := a.p.ApprovalsNeeded(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]inbox.PreviewApproval, 0, len(as))
	for _, x := range as {
		out = append(out, inbox.PreviewApproval{PullID: x.PullID, SHA: x.HeadSHA, RepositoryID: x.RepoID, Repository: x.Repo,
			Number: x.Number, Title: x.Title, ProjectID: x.ProjectID, Project: x.Project, APIKeyName: x.APIKeyName,
			AuthorName: x.AuthorName, AgentName: x.AgentName, At: x.UpdatedAt})
	}
	return out, nil
}

// previewRemovals: environments and repositories remove their previews
// before they go; no one person is the actor.
type previewRemovals struct{ p *preview.Service }

func (r previewRemovals) RemoveForEnvironment(ctx context.Context, environmentID string) error {
	return r.p.RemoveForEnvironment(ctx, environmentID, preview.Actor{})
}
func (r previewRemovals) RemoveForRepository(ctx context.Context, repoID string) error {
	return r.p.RemoveForRepository(ctx, repoID, preview.Actor{})
}
func (r previewRemovals) Nudge() { r.p.Nudge() }

// serviceAddresses names a new web service's address (D-150, service.
// Addresser): preview.Service's rule, through the environment's project.
type serviceAddresses struct {
	hosts    previewHosts
	envs     *environment.Service
	projects *project.Service
	previews *preview.Service
	domains  *domain.Service
}

func (a serviceAddresses) Address(ctx context.Context, environmentID, serverID, name string) (string, error) {
	env, err := a.envs.Get(ctx, environmentID)
	if err != nil {
		return "", fmt.Errorf("its environment: %w", err)
	}
	p, err := a.projects.GetProject(ctx, env.ProjectID)
	if err != nil {
		return "", fmt.Errorf("its project: %w", err)
	}
	return a.AddressIn(ctx, p.Name, env.Name, serverID, name)
}

func (a serviceAddresses) AddressIn(ctx context.Context, project, environment, serverID, name string) (string, error) {
	h, err := a.previews.Address(ctx, project, environment, name, serverID, func(h string) (bool, error) { return a.domains.Taken(ctx, h) })
	if errors.Is(err, preview.ErrNoPublicAddress) {
		return "", service.ErrNoPublicAddress
	}
	return h, err
}

func (a serviceAddresses) ServerAddress(ctx context.Context, serverID string) (string, error) {
	ip, err := a.hosts.Address(ctx, serverID)
	if err != nil {
		return "", err
	}
	if addr, err := netip.ParseAddr(ip); err != nil || !addr.Unmap().Is4() || !netcheck.IsPublic(addr) {
		return "", nil
	}
	return ip, nil
}
