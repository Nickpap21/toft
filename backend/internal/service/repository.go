package service

import (
	"context"
	"time"
)

// Repository defines data access for Services.
type Repository interface {
	// Create inserts a service under the given environment (scope=environment).
	Create(ctx context.Context, environmentID string, input CreateInput) (*Service, error)
	// CreateProjectScoped inserts a service shared across all environments of a project.
	CreateProjectScoped(ctx context.Context, projectID string, input CreateInput) (*Service, error)
	// CreateOrgScoped inserts a service shared across the entire organisation.
	CreateOrgScoped(ctx context.Context, orgID string, input CreateInput) (*Service, error)
	GetByID(ctx context.Context, id string) (*Service, error)
	// RuntimeStatuses returns the name and stored runtime status of each of
	// ids that exists in serviceID's workspace, by ID, in one query.
	RuntimeStatuses(ctx context.Context, serviceID string, ids []string) (map[string]ServiceStatus, error)
	// ListByProject joins through environments to return all env-scoped services in a project.
	// Used by the compose export and project-level service deleter.
	ListByProject(ctx context.Context, projectID string) ([]Service, error)
	// ListByEnvironment returns env-scoped services for the environment plus project-scoped
	// and org-scoped services that are visible to that environment's context.
	ListByEnvironment(ctx context.Context, environmentID string) ([]Service, error)
	// ListProjectScoped returns services with scope='project' for the given projectID.
	ListProjectScoped(ctx context.Context, projectID string) ([]Service, error)
	// ListOrgScoped returns services with scope='org' for the given orgID.
	ListOrgScoped(ctx context.Context, orgID string) ([]Service, error)
	// ListWorkspaceServices returns every service of the workspace, of every
	// scope, as the shell lists them.
	ListWorkspaceServices(ctx context.Context, orgID string) ([]WorkspaceService, error)
	UpdateConfig(ctx context.Context, id string, input UpdateConfigInput) (*Service, error)
	UpdateRuntimeStatus(ctx context.Context, id, status, activeDeploymentID string) error
	// SetComposeNetworkService records the compose service a successful
	// compose deploy routed ("" none; services.compose_network_service).
	SetComposeNetworkService(ctx context.Context, id, name string) error
	// PidsLimit and SetPidsLimit are the hardened runtime's process limit
	// (D-93); 0 is the default.
	PidsLimit(ctx context.Context, id string) (int, error)
	SetPidsLimit(ctx context.Context, id string, n int) error
	// DeployTimeout and SetDeployTimeout are the deploy time limit in
	// minutes (D-125); 0 is the default.
	DeployTimeout(ctx context.Context, id string) (int, error)
	SetDeployTimeout(ctx context.Context, id string, minutes int) error
	// RollbackImagesKept and SetRollbackImagesKept are how many of its last
	// live deployments keep their image (D-135); 0 is the default.
	RollbackImagesKept(ctx context.Context, id string) (int, error)
	SetRollbackImagesKept(ctx context.Context, id string, n int) error
	// ContainerHealth and SetContainerHealth are the Docker health check
	// its containers are created with (ContainerHealthImage, Off or
	// Command) and the command's shell line.
	ContainerHealth(ctx context.Context, id string) (mode, command string, err error)
	SetContainerHealth(ctx context.Context, id, mode, command string) error
	// Isolation is the service's own isolation level ("" follows the
	// workspace's) and its workspace's default; SetIsolation writes it, ""
	// back to the default (D-95).
	Isolation(ctx context.Context, id string) (own, workspaceDefault string, err error)
	SetIsolation(ctx context.Context, id, level string) error
	UpdateDockerImage(ctx context.Context, id, image string, clearRegistryPassword bool) error
	// SwapDockerImage sets docker_image to image only while it is still
	// from; a registryPassword ("" none) is sealed and put back with it,
	// only while no password is saved.
	SwapDockerImage(ctx context.Context, id, from, image, registryPassword string) error
	UpdateGitBranch(ctx context.Context, id, branch string) error
	AssignServer(ctx context.Context, serviceID, serverID string) (*Service, error)
	// RenameTemporaryAddresses moves the service's sslip.io names to address
	// (a server's IPv4) and reports how many it renamed (D-150).
	RenameTemporaryAddresses(ctx context.Context, serviceID, fromIP, toIP string) (int64, error)
	Delete(ctx context.Context, id string) error
	// CopyForPreview writes a preview's copy of a service (D-97) with
	// memoryLimit, and what goes with it, in one transaction.
	CopyForPreview(ctx context.Context, in PreviewCopy, memoryLimit string) (string, error)
	// AddPreviewAddress adds a hostname to a preview's copy.
	AddPreviewAddress(ctx context.Context, id, hostname string) error
	ListCopies(ctx context.Context, id string) ([]string, error)
	IsPreviewCopy(ctx context.Context, id string) (bool, error)
	IsPreviewEnvironment(ctx context.Context, environmentID string) (bool, error)
	// PreviewOfEnvironment: the environment a preview's environment copies
	// and whether the preview is untrusted (ok false: not a preview's).
	PreviewOfEnvironment(ctx context.Context, environmentID string) (source string, untrusted, ok bool, err error)
	// ListPreviewPeers: the copied environment's services a trusted
	// preview has no copy of, on serverID.
	ListPreviewPeers(ctx context.Context, sourceEnvID, serverID, previewEnvID string) ([]NetworkPeer, error)
	// ListPreviewTargets: the trusted previews' environments a service of
	// the environment they copy publishes itself into.
	ListPreviewTargets(ctx context.Context, environmentID, serviceID string) ([]string, error)
	// ListUntrustedPreviewEnvironments: shared services never publish into
	// these (a project's, or an org's projects').
	ListUntrustedPreviewEnvironments(ctx context.Context, projectID, orgID string) ([]string, error)
	FindByProjectRepoAndBranch(ctx context.Context, projectID, repoURL, repositoryID, branch string) ([]Service, error)
	// SetSource changes a service's repository source and deploy on push
	// (D-77); RepositoryInScope says whether a repository is the project's
	// (the environment's project when environmentID is set).
	SetSource(ctx context.Context, id string, repositoryID *string, deployOnPush *bool) error
	RepositoryInScope(ctx context.Context, repositoryID, environmentID, projectID string) (bool, error)
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	// MoveToWorkspace makes the service orgID's own (scope org): false when
	// it already was; ErrDuplicateName when the workspace has one by its
	// name (D-162). In the caller's transaction (dbtx).
	MoveToWorkspace(ctx context.Context, id, orgID string) (bool, error)
	// AliasUserInWorkspace names another of orgID's services, in any
	// scope, with this network alias ("" for none).
	AliasUserInWorkspace(ctx context.Context, orgID, id, alias string) (string, error)
	// ListRunningMetricsTargets returns every service currently in "running"
	// status, with its name and server, for the metrics collector.
	ListRunningMetricsTargets(ctx context.Context) ([]MetricsTarget, error)
	// Metrics history
	// InsertSample stores a sample collected at at: the collector cycle's
	// time, which all of the cycle's rows share.
	InsertSample(ctx context.Context, serviceID, composeService string, at time.Time, s MetricSample) error
	// ListMetrics returns one point per collector cycle from since on,
	// oldest first, the service's compose services summed.
	// ListMetrics: composeService "" sums a stack's compose services.
	ListMetrics(ctx context.Context, serviceID, composeService string, since time.Time) ([]MetricPoint, error)
	// ListDailyMetrics returns one point per UTC day, oldest first: the 6
	// days before today (midnight UTC) summarised, then today so far.
	ListDailyMetrics(ctx context.Context, serviceID, composeService string, today time.Time) ([]MetricPoint, error)
	DeleteOldMetrics(ctx context.Context, before time.Time) error
	// NewestMetricOn returns when the newest sample of a server's services
	// was collected; zero for none.
	NewestMetricOn(ctx context.Context, serverID string) (time.Time, error)
	// SumAllocatedForServer returns the total cpu (cores) and memory (MB) committed
	// by all non-stopped services assigned to the given server.
	SumAllocatedForServer(ctx context.Context, serverID string) (cpuCores float64, memoryMB float64, err error)

	// App network planning (package C).
	// NetworkScope returns where a service sits: scope, environment,
	// project, org and server.
	NetworkScope(ctx context.Context, serviceID string) (*NetworkScope, error)
	// ListNetworkPeers returns the services on the same server whose
	// containers join the service's home network.
	ListNetworkPeers(ctx context.Context, sc NetworkScope) ([]NetworkPeer, error)
	// ListNetworkTargets returns a shared service's consumer environments
	// and, for an org, its projects.
	ListNetworkTargets(ctx context.Context, projectID, orgID string) ([]NetworkTarget, []string, error)
	// ListIDsOnServer returns the services assigned to a server ("": the
	// control-plane server).
	ListIDsOnServer(ctx context.Context, serverID string) ([]string, error)
	// ListIDsIn returns the IDs of the services a delete of the scope
	// (an environment, a project with its shared services, or a workspace
	// with its own) takes with it.
	ListIDsIn(ctx context.Context, scope Scope, id string) ([]string, error)
	// ListData returns the services given and their previews' copies, with
	// what names their data on their servers (Data), the given ones first.
	ListData(ctx context.Context, ids []string) ([]DataRow, error)
	// ListAllIDs returns every service's ID.
	ListAllIDs(ctx context.Context) ([]string, error)
	// ListResourceFieldsOnServer returns the services on a server, by name,
	// with only the fields resourceRef reads set, in one query.
	ListResourceFieldsOnServer(ctx context.Context, orgID, serverID string) ([]Service, error)
}
