package deployment

import (
	"context"
	"time"
)

// Repository defines data access for the deployment domain.
type Repository interface {
	// Create records a deployment; gitEventID is the push or merge that
	// started it (0 for none), and a second for the same service and event
	// is ErrAlreadyDeployed.
	Create(ctx context.Context, serviceID, triggeredBy, commitMessage string, gitEventID int64) (*Deployment, error)
	// SetSettingsFingerprint records what a deploy that went live shipped of
	// its service's settings (D-159).
	SetSettingsFingerprint(ctx context.Context, id, fingerprint string) error
	// SetReservedMemory records the memory a deploy that went live reserved
	// on its server, in MiB (D-166).
	SetReservedMemory(ctx context.Context, id string, memoryMB float64) error
	// ListPulls is the pull requests whose merges started deployments ids,
	// by deployment (D-77).
	ListPulls(ctx context.Context, ids []string) (map[string]PullRef, error)
	UpdateStatus(ctx context.Context, id string, status Status, imageTag, commitSHA, errMsg string) (*Deployment, error)
	// SetComposeCommit records the commit a compose deploy checked its
	// repository out at (a full SHA).
	SetComposeCommit(ctx context.Context, id, sha string) error
	// SetCommit records the commit a git build checked out (a full SHA) and
	// its subject, which replace what the deploy was started with.
	SetCommit(ctx context.Context, id, sha, subject string) error
	ListByService(ctx context.Context, serviceID string) ([]Deployment, error)
	ListByServicePaginated(ctx context.Context, serviceID string, limit int32, before *time.Time, beforeID string) ([]Deployment, error)
	GetByID(ctx context.Context, id string) (*Deployment, error)
	InsertLog(ctx context.Context, deploymentID, message string) (*Log, error)
	ListLogs(ctx context.Context, deploymentID string) ([]Log, error)
	ListLogsAfter(ctx context.Context, deploymentID string, afterID int64) ([]Log, error)
	ListLogsFiltered(ctx context.Context, deploymentID, search string) ([]Log, error)
	// ListServiceIDs returns all distinct service IDs that have deployments.
	ListServiceIDs(ctx context.Context) ([]string, error)
	// DeleteOldDeploymentsForService removes deployment records (and their logs, via CASCADE)
	// beyond the most recent keepLast for the given service.
	DeleteOldDeploymentsForService(ctx context.Context, serviceID string, keepLast int) error
	// MarkInterrupted marks every deployment left pending, queued or
	// building as failed with summary, except excludeIDs, and returns them.
	// Their started_at is kept.
	MarkInterrupted(ctx context.Context, summary string, excludeIDs []string) ([]Deployment, error)
	// ListUnreconciledInterrupted returns the failed deployments whose
	// error summary is exactly summary and that are still their service's
	// newest deployment: those an earlier start marked interrupted but
	// whose recovery check never finished.
	ListUnreconciledInterrupted(ctx context.Context, summary string) ([]Deployment, error)
	// RecordRecovery sets a deployment's error summary and, when imageTag
	// is not empty, its image tag.
	RecordRecovery(ctx context.Context, id, imageTag, summary string) error
	// LatestIDForService returns the ID of serviceID's newest deployment.
	LatestIDForService(ctx context.Context, serviceID string) (string, error)
	// MarkHandedOff records that the rollout of imageTag goes to the
	// server's agent (D-111), with input, what the panel needs to finish
	// the deploy after a restart; ClearHandOff that the panel finishes it
	// itself again.
	MarkHandedOff(ctx context.Context, id, imageTag string, input []byte) error
	ClearHandOff(ctx context.Context, id string) error
	// IsHandedOff reports whether the deploy's rollout is with the agent.
	IsHandedOff(ctx context.Context, id string) (bool, error)
	// DeployRunningOnServer reports whether a deploy is under way to the
	// server, "" the panel's own (D-134).
	DeployRunningOnServer(ctx context.Context, serverID string) (bool, error)
	// ListHandedOff returns the deploys a server's agent runs that are
	// still in progress, oldest first.
	ListHandedOff(ctx context.Context) ([]HandedOff, error)
	// CountAgentLogLines returns how many of the agent's log lines a
	// handed-off deploy has: those since the hand-off not starting with
	// panelPrefix.
	CountAgentLogLines(ctx context.Context, id, panelPrefix string) (int, error)
	// Adopt records deploymentID as running for serviceID on serverID when
	// there is no record of it (D-113). False when the service isn't on
	// that server or the deployment is another service's.
	Adopt(ctx context.Context, serverID, serviceID, deploymentID, imageTag, triggeredBy string, startedAt time.Time) (bool, error)
}

// HandedOff is a deploy a server's agent runs, with the input the panel
// recorded when it handed the rollout over.
type HandedOff struct {
	Deployment Deployment
	Input      []byte
}
