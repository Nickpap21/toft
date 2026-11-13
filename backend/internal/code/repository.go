package code

import (
	"context"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
)

// Repository is the code module's data access.
type Repository interface {
	// Unnamed lists the projects with no repository yet; TakenSlugs every
	// workspace slug; TakenNames the repository names in those workspaces
	// (workspace ID → names).
	Unnamed(ctx context.Context) ([]Unnamed, error)
	TakenSlugs(ctx context.Context) ([]string, error)
	TakenNames(ctx context.Context, orgIDs []string) (map[string][]string, error)
	// Name gives workspaces their slugs (only those with none) and projects
	// their repositories.
	Name(ctx context.Context, slugs map[string]string, repos []NewRepo) error

	ListProjectRepos(ctx context.Context, projectID string) ([]StoredRepo, error)
	// RepoByPath is the repository <orgSlug>/<name>, or ErrNotFound.
	RepoByPath(ctx context.Context, orgSlug, name string) (*Target, error)

	// CreateRepo stores a repository; a name its workspace has is
	// ErrNameTaken. DeleteRepo removes one of the project's, never its last
	// (false then, or when it isn't the project's). SetProtection turns its
	// protection on or off and stores the protected branch's name ("" while
	// off, or on with no branch yet); FillProtectedBranch stores it only
	// while protection is on and no name is stored (0080's backfill).
	CreateRepo(ctx context.Context, orgID, projectID, name string) (*StoredRepo, error)
	DeleteRepo(ctx context.Context, projectID, id string) (bool, error)
	SetProtection(ctx context.Context, id string, on bool, branch string) error
	FillProtectedBranch(ctx context.Context, id, branch string) error

	// Deploy on push and checks (D-77). InsertEvent writes an outbox row;
	// ClaimEvents leases up to n due ones; FinishEvent marks one dispatched
	// (msg is why some deploys didn't start, or ""); RetryEvent makes it due
	// again at; DeleteOldEvents removes dispatched ones made before.
	InsertEvent(ctx context.Context, e GitEvent) (int64, error)
	ClaimEvents(ctx context.Context, n int) ([]ClaimedEvent, error)
	FinishEvent(ctx context.Context, id int64, msg string) error
	RetryEvent(ctx context.Context, id int64, at time.Time, msg string) error
	DeleteOldEvents(ctx context.Context, before time.Time) (int64, error)
	// ListEvents is a repository's events newest first, ids below
	// beforeID (0: from the newest), only branch's when it is set.
	ListEvents(ctx context.Context, repoID, branch string, beforeID int64, n int) ([]Pipeline, error)
	UpsertCheck(ctx context.Context, c NewCheck) error
	CheckExists(ctx context.Context, repoID, sha, name string) (bool, error)
	CountChecks(ctx context.Context, repoID, sha string) (int, error)
	ListChecks(ctx context.Context, repoID string, shas []string) ([]StoredCheck, error)
	FailDeployChecks(ctx context.Context, deploymentIDs []string, description string) error
	// DeployTargets is the services that build from the repository.
	DeployTargets(ctx context.Context, repoID string) ([]DeployTarget, error)
	// Agents' branches (D-102).
	BranchDeployed(ctx context.Context, repoID, branch string) (bool, error)
	AgentBranch(ctx context.Context, repoID, name string) (*AgentBranch, error)
	AgentBranchByToken(ctx context.Context, hash string) (*AgentBranch, error)
	UpsertAgentBranch(ctx context.Context, repoID, name, connectionID, userID, hash string, expires time.Time) error
	// AgentBranches maps a repository's agents' branches to "<agent> for <person>".
	AgentBranches(ctx context.Context, repoID string) (map[string]string, error)

	// Pull requests (D-75). OpenPull numbers and stores one in a
	// transaction; an open one for the same branches is ErrPullExists.
	OpenPull(ctx context.Context, p NewPull) (*StoredPull, error)
	GetPull(ctx context.Context, repoID string, number int) (*StoredPull, error)
	// ListPulls is newest first, numbers below before (0: from the newest).
	ListPulls(ctx context.Context, repoID, state string, before, limit int) ([]StoredPull, error)
	CountPulls(ctx context.Context, repoID string) (map[string]int, error)
	OpenPullsByHead(ctx context.Context, repoID string, branches []string) ([]StoredPull, error)
	UpdatePullText(ctx context.Context, id, title, body string) error
	// SetPullState moves an open pull request to closed or back; false when
	// it wasn't in from.
	SetPullState(ctx context.Context, id, from, to string) (bool, error)
	MarkMerged(ctx context.Context, id string, m Merged) (bool, error)
	SetPullHead(ctx context.Context, id, sha string) error
	AddComment(ctx context.Context, pullID, authorID, body string) (*StoredComment, error)
	GetComment(ctx context.Context, pullID, id string) (*StoredComment, error)
	UpdateComment(ctx context.Context, id, body string) error
	DeleteComment(ctx context.Context, id string) error
	AddEvent(ctx context.Context, pullID, actorID, kind string, data map[string]any) error
	// Timeline is the conversation's comments and events, oldest first,
	// after the item at (afterAt, afterID) when afterID is set.
	Timeline(ctx context.Context, pullID string, afterAt time.Time, afterID string, limit int) ([]StoredItem, error)

	// Import and mirrors (D-79). CreateMirror stores one, due at once; a
	// pull mirror replaces an import that failed and is otherwise
	// ErrImportRunning when the repository has one. ClaimMirrors marks up to
	// n due ones running; FinishMirror records how a sync ended and returns
	// the failures in a row; ResetSyncing makes those a stopped backend left
	// running due again.
	ListMirrors(ctx context.Context, repoID string) ([]StoredMirror, error)
	ProjectPullMirrors(ctx context.Context, projectID string) ([]StoredMirror, error)
	CountPushMirrors(ctx context.Context, repoID string) (int, error)
	CreateMirror(ctx context.Context, m NewMirror) (string, error)
	DeleteMirror(ctx context.Context, repoID, id string) error
	RequestSync(ctx context.Context, repoID, id string) (bool, error)
	ClaimMirrors(ctx context.Context, n int) ([]ClaimedMirror, error)
	SetMirrorRemote(ctx context.Context, id string, refs gitstore.RemoteRefs) error
	FinishMirror(ctx context.Context, id string, r MirrorResult) (int, error)
	ResetSyncing(ctx context.Context) error
	MirrorFailures(ctx context.Context) ([]MirrorHealth, error)

	// Outbound webhooks (D-79). Deliveries are written with their events
	// (InsertEvent); ClaimDeliveries leases up to n due ones of active
	// webhooks; FinishDelivery records an attempt.
	ListWebhooks(ctx context.Context, repoID string) ([]StoredWebhook, error)
	CountWebhooks(ctx context.Context, repoID string) (int, error)
	CreateWebhook(ctx context.Context, w NewWebhook) (string, error)
	UpdateWebhook(ctx context.Context, repoID, id string, in WebhookUpdate) (bool, error)
	RotateWebhookSecret(ctx context.Context, repoID, id, secretEnc, actorID string) (bool, error)
	DeleteWebhook(ctx context.Context, repoID, id string) (bool, error)
	InsertPing(ctx context.Context, webhookID, actorID string) (string, error)
	ListDeliveries(ctx context.Context, webhookID string, afterAt time.Time, afterID string, n int) ([]WebhookDelivery, error)
	LastDeliveries(ctx context.Context, repoID string) (map[string]WebhookDelivery, error)
	GetDelivery(ctx context.Context, repoID, webhookID, id string) (*StoredDelivery, error)
	Redeliver(ctx context.Context, webhookID, id, actorID string) (bool, error)
	ClaimDeliveries(ctx context.Context, n int) ([]ClaimedDelivery, error)
	SetDeliveryPayload(ctx context.Context, id, summary, payload string) error
	FinishDelivery(ctx context.Context, id string, r DeliveryResult) error
	DeleteOldDeliveries(ctx context.Context, before time.Time) (int64, error)
	GetEvent(ctx context.Context, id int64) (*StoredEvent, error)
	PullByID(ctx context.Context, id string) (*StoredPull, error)
	RepoForHook(ctx context.Context, id string) (*HookRepo, error)

	ListKeys(ctx context.Context, userID string) ([]SSHKey, error)
	CountKeys(ctx context.Context, userID string) (int64, error)
	// AddKey stores a key; a fingerprint already stored is ErrKeyInUse.
	AddKey(ctx context.Context, userID, title, publicKey, fingerprint, keyType string) (*SSHKey, error)
	// DeleteKey removes the user's key and returns it, or ErrNotFound.
	DeleteKey(ctx context.Context, userID, id string) (*SSHKey, error)
	// KeyOwner is the key with this fingerprint and its account, or ErrNotFound.
	KeyOwner(ctx context.Context, fingerprint string) (keyID, userID string, err error)
	TouchKey(ctx context.Context, keyID string) error
}

// Unnamed is a project without a repository, with what naming one needs.
type Unnamed struct {
	ProjectID, OrgID, ProjectName, OrgName, OrgSlug string
}

// NewRepo is a repository to create.
type NewRepo struct {
	OrgID, ProjectID, Name string
}

// StoredRepo is a repository row with its workspace's slug.
type StoredRepo struct {
	ID, Name, OrgSlug, OrgID string
	CreatedAt                time.Time
	Protect                  bool
	// ProtectedBranch is the name the hook protects; "" until stored.
	ProtectedBranch string
	OpenPulls       int
	// Mirrored: the repository has a pull mirror, an import included, and
	// is read-only (D-79).
	Mirrored bool
}

// NewPull is a pull request to store.
type NewPull struct {
	RepoID, AuthorID, Title, Body, Base, Head, HeadSHA string
	// Opened with an API key (D-97); APIKeyID is that key.
	WithKey  bool
	APIKeyID string
	// AgentID is the agent connection that opened it (D-102).
	AgentID string
}

// StoredPull is a pull request row with the names of who opened and merged
// it ("" for a deleted account).
type StoredPull struct {
	ID, RepoID                 string
	Number                     int
	Title, Body                string
	AuthorID, AuthorName       string
	AuthorEmail                string
	Base, Head, HeadSHA, State string
	MergeMethod, MergeSHA      string
	MergeBaseSHA               string
	MergedByID, MergedByName   string
	MergedAt, ClosedAt         *time.Time
	CreatedAt, UpdatedAt       time.Time
	Comments                   int
	AgentName                  string
}

// Merged is how a pull request was merged.
type Merged struct {
	Method, SHA, MergeBase, Head, By string
}

// StoredComment is a comment row with its author's name.
type StoredComment struct {
	ID, AuthorID, AuthorName, Body string
	AgentName                      string
	CreatedAt, UpdatedAt           time.Time
}

// StoredItem is a comment or an event of a pull request's conversation.
type StoredItem struct {
	Item, ID, ActorID, ActorName, Body, Kind string
	AgentName                                string
	Data                                     []byte
	CreatedAt, UpdatedAt                     time.Time
}
