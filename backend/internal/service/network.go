package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
)

// NetworkScope is where a service sits, as network planning needs it.
type NetworkScope struct {
	Scope         Scope
	ServiceID     string
	Name          string
	Alias         string
	SourceType    SourceType
	EnvironmentID string
	ProjectID     string
	OrgID         string
	ServerID      string
}

// NetworkPeer is a service whose containers join another service's home
// network.
type NetworkPeer struct {
	ID         string
	Name       string
	Alias      string
	SourceType SourceType
	Scope      Scope
}

// NetworkTarget is an environment whose network a shared service publishes
// itself into.
type NetworkTarget struct {
	EnvironmentID string
	ProjectID     string
}

// ErrNoNetworkScope is returned for a service whose rows do not say which
// network it belongs to (an environment service without an environment, a
// shared service without its project or org).
var ErrNoNetworkScope = errors.New("the service has no environment, project or org to put it on a network")

// BuildNetworkPlan returns the isolated network plan of the service sc
// (netplan): its home network, the peers attached into it, and for a shared
// service the consumer networks it publishes itself into. peers are the
// services on the same server whose containers join its home
// (ListNetworkPeers); targets and projectIDs are a shared service's consumer
// environments and, for an org, its projects. The result is sorted by ID.
//
//   - environment: home paas-env-<environment>; peers are the environment's
//     services and its project's and org's shared services.
//   - project: home paas-proj-<project>; publishes into each environment of
//     the project; peers are the project's other shared services and the
//     org's.
//   - org: home paas-org-<org>; publishes into every environment and
//     project network of the org; peers are its other shared services.
//
// Org-shared services are thus reachable from every environment of their org
// on the same server (D-31), and never from another org.
func BuildNetworkPlan(sc NetworkScope, peers []NetworkPeer, targets []NetworkTarget, projectIDs []string) (netplan.Plan, error) {
	p := netplan.Plan{
		ServiceID:   sc.ServiceID,
		ServiceName: sc.Name,
		Alias:       sc.Alias,
		Compose:     sc.SourceType == SourceTypeDockerCompose,
	}
	switch sc.Scope {
	case ScopeEnvironment, "":
		if sc.EnvironmentID == "" || sc.ProjectID == "" {
			return netplan.Plan{}, fmt.Errorf("%w: environment service %s has no environment (run make db-backfill-envs)", ErrNoNetworkScope, sc.ServiceID)
		}
		p.Home = netplan.Env(sc.EnvironmentID, sc.ProjectID, sc.OrgID)
	case ScopeProject:
		if sc.ProjectID == "" {
			return netplan.Plan{}, fmt.Errorf("%w: project-shared service %s has no project", ErrNoNetworkScope, sc.ServiceID)
		}
		p.Home = netplan.Project(sc.ProjectID, sc.OrgID)
		for _, t := range targets {
			if t.ProjectID == sc.ProjectID {
				p.Publish = append(p.Publish, netplan.Env(t.EnvironmentID, t.ProjectID, sc.OrgID))
			}
		}
	case ScopeOrg:
		if sc.OrgID == "" {
			return netplan.Plan{}, fmt.Errorf("%w: org-shared service %s has no org", ErrNoNetworkScope, sc.ServiceID)
		}
		p.Home = netplan.Org(sc.OrgID)
		for _, t := range targets {
			p.Publish = append(p.Publish, netplan.Env(t.EnvironmentID, t.ProjectID, sc.OrgID))
		}
		for _, id := range projectIDs {
			p.Publish = append(p.Publish, netplan.Project(id, sc.OrgID))
		}
	default:
		return netplan.Plan{}, fmt.Errorf("%w: unknown scope %q", ErrNoNetworkScope, sc.Scope)
	}

	seen := map[string]bool{sc.ServiceID: true}
	for _, peer := range peers {
		if peer.ID == "" || seen[peer.ID] {
			continue
		}
		seen[peer.ID] = true
		p.Attach = append(p.Attach, netplan.Endpoint{
			ServiceID:   peer.ID,
			ServiceName: peer.Name,
			Alias:       peer.Alias,
			Compose:     peer.SourceType == SourceTypeDockerCompose,
		})
	}
	sort.Slice(p.Attach, func(i, j int) bool { return p.Attach[i].ServiceID < p.Attach[j].ServiceID })

	pub := map[string]bool{}
	out := p.Publish[:0:0]
	for _, n := range p.Publish {
		if n.Name != p.Home.Name && !pub[n.Name] {
			pub[n.Name] = true
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	p.Publish = out
	return p, p.Valid()
}

// NetworkPlan returns the isolated network plan of service id, read from
// the database. Every deploy computes it once (deployment.NetworkPlanner).
func (s *ServiceManager) NetworkPlan(ctx context.Context, id string) (netplan.Plan, error) {
	sc, err := s.repo.NetworkScope(ctx, id)
	if err != nil {
		return netplan.Plan{}, err
	}
	peers, err := s.repo.ListNetworkPeers(ctx, *sc)
	if err != nil {
		return netplan.Plan{}, err
	}
	var (
		targets  []NetworkTarget
		projects []string
	)
	switch sc.Scope {
	case ScopeProject:
		if sc.ProjectID != "" {
			targets, _, err = s.repo.ListNetworkTargets(ctx, sc.ProjectID, "")
		}
	case ScopeOrg:
		if sc.OrgID != "" {
			targets, projects, err = s.repo.ListNetworkTargets(ctx, "", sc.OrgID)
		}
	}
	if err != nil {
		return netplan.Plan{}, err
	}
	return s.withPreviews(ctx, *sc, peers, targets, projects)
}

// withPreviews builds sc's plan with previews in it (D-97). A trusted
// preview's services also reach the copied environment's services they
// have no copy of, and those publish themselves into the preview's
// network; an untrusted preview's network holds its own services alone,
// on a toft-u bridge, and nothing shared publishes into it.
func (s *ServiceManager) withPreviews(ctx context.Context, sc NetworkScope, peers []NetworkPeer, targets []NetworkTarget, projects []string) (netplan.Plan, error) {
	untrusted := false
	var publish []string
	if sc.Scope == ScopeEnvironment || sc.Scope == "" {
		source, isUntrusted, isPreview, err := s.repo.PreviewOfEnvironment(ctx, sc.EnvironmentID)
		if err != nil {
			return netplan.Plan{}, err
		}
		switch {
		case isPreview && isUntrusted:
			untrusted = true
			own := peers[:0:0]
			for _, p := range peers {
				if p.Scope == ScopeEnvironment || p.Scope == "" {
					own = append(own, p)
				}
			}
			peers = own
		case isPreview:
			extra, err := s.repo.ListPreviewPeers(ctx, source, sc.ServerID, sc.EnvironmentID)
			if err != nil {
				return netplan.Plan{}, err
			}
			peers = append(peers, extra...)
		default:
			if publish, err = s.repo.ListPreviewTargets(ctx, sc.EnvironmentID, sc.ServiceID); err != nil {
				return netplan.Plan{}, err
			}
		}
	} else if len(targets) > 0 {
		skip, err := s.repo.ListUntrustedPreviewEnvironments(ctx, sc.ProjectID, sc.OrgID)
		if err != nil {
			return netplan.Plan{}, err
		}
		kept := targets[:0:0]
		for _, t := range targets {
			if !slices.Contains(skip, t.EnvironmentID) {
				kept = append(kept, t)
			}
		}
		targets = kept
	}
	p, err := BuildNetworkPlan(sc, peers, targets, projects)
	if err != nil {
		return p, err
	}
	p.Home.Untrusted = untrusted
	for _, env := range publish {
		p.Publish = append(p.Publish, netplan.Env(env, sc.ProjectID, sc.OrgID))
	}
	return p, p.Valid()
}

// EnvVarReader reads a service's env vars as KEY=VALUE pairs. Satisfied by
// envvar.Service. Used for the preflight report (names only are reported).
type EnvVarReader interface {
	ListAllForDeployment(ctx context.Context, serviceID string) ([]string, error)
}

// WithEnvVarReader enables the cross-reference preflight in
// ReconcileNetworks.
func (s *ServiceManager) WithEnvVarReader(r EnvVarReader) { s.envReader = r }

// NetworkReconciler is what an orchestrator does to converge a host on the
// network plans of the services it runs. Both docker orchestrators
// implement it.
type NetworkReconciler interface {
	// serviceIDs are every service assigned to the host; plans are those
	// whose plan could be built. A container of a service without a plan is
	// never detached.
	ReconcileNetworks(ctx context.Context, plans []netplan.Plan, serviceIDs []string, mode netplan.MigrationMode, logFn func(string)) (netplan.Report, error)
}

// ReconcileNetworks converges serverID's host ("" is the control-plane
// server) on the network plans of the services assigned to it, in mode. A
// service whose plan cannot be built is skipped and reported; the preflight
// cross-references are reported before anything is detached.
func (s *ServiceManager) ReconcileNetworks(ctx context.Context, serverID string, mode netplan.MigrationMode, logFn func(string)) (netplan.Report, error) {
	var report netplan.Report
	if s.factory == nil {
		return report, errors.New("orchestration is not configured")
	}
	ids, err := s.repo.ListIDsOnServer(ctx, serverID)
	if err != nil {
		return report, err
	}
	plans := make([]netplan.Plan, 0, len(ids))
	for _, id := range ids {
		p, err := s.NetworkPlan(ctx, id)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("service %s: %v", id, err))
			continue
		}
		plans = append(plans, p)
	}
	if s.envReader != nil {
		env := make(map[string][]string, len(plans))
		for _, p := range plans {
			vars, err := s.envReader.ListAllForDeployment(ctx, p.ServiceID)
			if err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("service %s: env vars: %v", p.ServiceID, err))
				continue
			}
			env[p.ServiceID] = vars
		}
		report.CrossRefs = netplan.FindCrossRefs(plans, env)
		for _, r := range report.CrossRefs {
			logFn(fmt.Sprintf("preflight: %s (%s) env var %s names %q, the alias of %s (%s), which it cannot reach once networks are isolated",
				r.FromName, r.From, r.Variable, r.Alias, r.ToName, r.To))
		}
	}

	var srv *serverPkg.Server
	if serverID != "" {
		if s.serverFetcher == nil {
			return report, errors.New("servers are not configured")
		}
		if srv, err = s.serverFetcher.GetServer(ctx, serverID); err != nil {
			return report, fmt.Errorf("get server: %w", err)
		}
	}
	orch, cleanup, err := s.factory.ForServer(srv)
	if err != nil {
		return report, err
	}
	defer cleanup()
	rec, ok := orch.(NetworkReconciler)
	if !ok {
		return report, errors.New("the orchestrator cannot reconcile networks")
	}
	// Every service that exists, so a container of a service assigned to
	// another server entry for the same machine is not taken for an orphan.
	all, err := s.repo.ListAllIDs(ctx)
	if err != nil {
		return report, err
	}
	hostReport, err := rec.ReconcileNetworks(ctx, plans, all, mode, logFn)
	hostReport.CrossRefs = report.CrossRefs
	hostReport.Errors = append(report.Errors, hostReport.Errors...)
	hostReport.Sort()
	return hostReport, err
}
