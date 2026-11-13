package issue

import (
	"context"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
)

// Repository is the issues module's storage.
type Repository interface {
	Project(ctx context.Context, projectID string) (*Project, error)
	OrgKeys(ctx context.Context, orgID string) ([]string, error)
	// SetKey sets the project's key unless it has one; false when the
	// workspace has the key already.
	SetKey(ctx context.Context, projectID, key string) (bool, error)
	MemberRole(ctx context.Context, orgID, userID string) (string, error)

	// Create numbers and inserts an issue with its labels and its opened
	// (and assigned) history, returning its number.
	Create(ctx context.Context, in NewIssue) (int, error)
	Get(ctx context.Context, projectID string, number int) (*Stored, error)
	// Update writes s as changed, its labels, and events, in one
	// transaction.
	Update(ctx context.Context, s *Stored, labelsChanged bool, labelsVersion string, events []Event) error
	List(ctx context.Context, q ListQuery) ([]Row, error)
	Count(ctx context.Context, q ListQuery) (map[string]int, error)
	CheckStates(ctx context.Context, shas []string) ([]CheckState, error)

	Timeline(ctx context.Context, issueID string, afterAt time.Time, afterID string, limit int) ([]StoredItem, error)
	AddComment(ctx context.Context, issueID, authorID, body string) (*StoredComment, error)
	GetComment(ctx context.Context, issueID, id string) (*StoredComment, error)
	EditComment(ctx context.Context, id, body string) error
	DeleteComment(ctx context.Context, id string) error

	Labels(ctx context.Context, projectID string) ([]Label, error)
	ServiceNames(ctx context.Context, projectID string) ([]string, error)
	Views(ctx context.Context, projectID string) ([]StoredView, error)
	GetView(ctx context.Context, projectID, id string) (*StoredView, error)
	AddView(ctx context.Context, projectID, ownerID, name string, filters []byte) (string, error)
	UpdateView(ctx context.Context, id, name string, filters []byte) error
	DeleteView(ctx context.Context, id string) error

	Links(ctx context.Context, issueID string) ([]StoredLink, error)
	BranchTargets(ctx context.Context, repoID, branch string) ([]Target, error)
	LatestSince(ctx context.Context, serviceIDs []string, since time.Time) (map[string]StoredDeploy, error)

	// The mover (mover.go).
	Unapplied(ctx context.Context, n int) ([]GitEvent, error)
	MarkApplied(ctx context.Context, eventID int64) error
	ByNumbers(ctx context.Context, projectID string, numbers []int) ([]Candidate, error)
	ForPull(ctx context.Context, pullID string) ([]Candidate, error)
	LinkBranch(ctx context.Context, issueID, repoID, branch string) (bool, error)
	LinkPull(ctx context.Context, issueID, repoID, pullID string) (bool, error)
	// Move moves an issue from one of m.From to m.To and writes m.Event,
	// in one transaction; false when its status isn't one of m.From or
	// the event was written already.
	Move(ctx context.Context, m Move) (bool, error)
	// Record writes an event; false when it was written already.
	Record(ctx context.Context, e Event) (bool, error)
	OtherOpenPulls(ctx context.Context, issueID, pullID string) (int, error)
	ShipCandidates(ctx context.Context, repoID string) ([]ShipCandidate, error)
	ServiceRepository(ctx context.Context, serviceID string) (string, error)

	// Types, keys and settings (D-83).
	Types(ctx context.Context, orgID string) ([]Type, error)
	AddType(ctx context.Context, orgID string, t TypeRef, position int) (string, error)
	UpdateType(ctx context.Context, orgID string, t Type) (bool, error)
	SetTypeArchived(ctx context.Context, orgID, typeID string, archived bool) (bool, error)
	OrgDigits(ctx context.Context, orgID string) (int, error)
	SetOrgDigits(ctx context.Context, orgID string, digits int) error
	Aliases(ctx context.Context, projectID string) ([]string, error)
	KeyHolder(ctx context.Context, orgID, key string) (string, error)
	// ReplaceKey gives the project key, keeping old as an alias.
	ReplaceKey(ctx context.Context, orgID, projectID, old, key string) error
	HiddenTypes(ctx context.Context, projectID string) ([]string, error)
	// SaveSettings sets the project's digits and default type. With a version
	// it also makes hidden the project's hidden types, if version is still
	// what is stored (listversion.ErrChanged and no write otherwise); without
	// one it leaves them.
	SaveSettings(ctx context.Context, projectID string, digits *int, defaultType, version string, hidden []string) error
	// Failed deploys (D-88).
	SetFailedDeployIssues(ctx context.Context, projectID string, on bool) error
	FailedDeployment(ctx context.Context, deploymentID string) (*FailedDeployment, error)
	// OpenFailureIssues are a service's open issues from its failed deploys, newest first.
	OpenFailureIssues(ctx context.Context, serviceID string) ([]FailureIssue, error)
	SetFromDeployment(ctx context.Context, issueID, deploymentID string) error
	LiveDeployment(ctx context.Context, serviceID string) (string, error)
	MyIssues(ctx context.Context, orgID, userID string) ([]MyIssue, error)

	// Parents, children and relations (D-83).
	IssueRef(ctx context.Context, issueID string) (*RefRow, error)
	IssueByKey(ctx context.Context, orgID, key string, number int) (string, error)
	Children(ctx context.Context, issueID string) ([]RefRow, error)
	ChildCounts(ctx context.Context, issueID string) (Progress, error)
	Relations(ctx context.Context, issueID string) ([]RelationRow, error)
	AddRelation(ctx context.Context, from, to, kind, actorID string) (bool, error)
	GetRelation(ctx context.Context, issueID, relationID string) (*RelationRow, error)
	DeleteRelation(ctx context.Context, relationID string) error
	BlockedIssues(ctx context.Context, blockerID string) ([]string, error)
	CopyLabels(ctx context.Context, src, dst string) error
	CopyRelations(ctx context.Context, src, dst, actorID string) error

	// Elsewhere in the panel.
	PullIssues(ctx context.Context, pullIDs []string) (map[string][]code.IssueRef, error)
	BranchIssues(ctx context.Context, repoID string, branches []string) (map[string][]code.IssueRef, error)
	SearchIssues(ctx context.Context, q SearchQuery) ([]SearchIssue, error)
	SearchPulls(ctx context.Context, q SearchQuery) ([]SearchPull, error)

	// Cycles, dates and estimates (D-90; cycles.go).
	OrgSettings(ctx context.Context, orgID string) (OrgSettings, error)
	SetCycleSchedule(ctx context.Context, orgID string, weeks *int, startDay int, zone string) error
	SetEstimates(ctx context.Context, orgID, scale string) error
	CycleSchedules(ctx context.Context) ([]OrgSchedule, error)
	Cycles(ctx context.Context, orgID string) ([]StoredCycle, error)
	// AddCycle inserts a cycle unless the workspace has its number.
	AddCycle(ctx context.Context, orgID string, c StoredCycle) error
	// MoveCycle redraws a cycle that hasn't started by today.
	MoveCycle(ctx context.Context, c StoredCycle, today time.Time) error
	RenameCycle(ctx context.Context, orgID string, number int, name string) (bool, error)
	CycleByNumber(ctx context.Context, orgID string, number int) (*StoredCycle, error)
	// CloseCycle marks an ended cycle closed with its last day's counts and
	// moves its unfinished issues to the next cycle, with their history,
	// in one transaction; nothing when it was closed already.
	CloseCycle(ctx context.Context, orgID string, c StoredCycle) error
	SnapshotCycle(ctx context.Context, cycleID string, day time.Time) error
	// CycleCounts are the workspace's cycles' counts now, by cycle id, of
	// one project's issues when projectID isn't "".
	CycleCounts(ctx context.Context, orgID, projectID string) (map[string]CycleCounts, error)
	CycleDays(ctx context.Context, cycleID string) ([]CycleDay, error)
	CycleShares(ctx context.Context, cycleID string) (projects, assignees []CycleShare, err error)
	CycleMoves(ctx context.Context, c StoredCycle, since time.Time) (added, carriedIn int, err error)
	CycleIssues(ctx context.Context, cycleID, projectID, afterID string, limit int) ([]CycleIssueRow, error)
	TimelineIssues(ctx context.Context, projectID string, since time.Time) ([]TimelineRow, error)
	ProjectBlocks(ctx context.Context, projectID string) ([][2]string, error)
}

// Project is what issues need of their project: its key, the digits its
// keys show (its own, else the workspace's), its default type and the
// number its next issue gets.
type Project struct {
	ID, OrgID, Name, Key string
	Digits, OrgDigits    int
	OwnDigits            *int
	DefaultTypeID        string
	NextNumber           int
	FailedDeployIssues   bool
	// Cycles and estimates (D-90): whether the workspace uses cycles, the
	// time zone their days are in, its estimate scale.
	Cycles    bool
	Zone      string
	Estimates string
}

// FailedDeployment is a deploy, with its service and project, for an issue
// opened from it (D-88).
type FailedDeployment struct {
	ID, Status, Error, CommitSHA, CommitMessage string
	At                                          time.Time
	ServiceID, ServiceName, Environment         string
	ProjectID, OrgID                            string
	FailedDeployIssues                          bool
}

// FailureIssue is an open issue opened from a service's failed deploy.
type FailureIssue struct {
	ID     string
	Number int
}

// RefRow is an issue as another names it.
type RefRow struct {
	ID, ProjectID, OrgID, Key string
	Number, Digits            int
	Title, Status, TypeID     string
	ParentID                  string
	AssigneeID, AssigneeName  string
	// CycleID is IssueRef's; Estimate and CycleNumber are Children's (D-90).
	CycleID     string
	Estimate    *int
	CycleNumber int
}

// RelationRow is a relation with the other issue; Outward when this issue
// is its from.
type RelationRow struct {
	ID, Kind string
	Outward  bool
	Other    RefRow
}

// NewIssue is an issue to insert.
type NewIssue struct {
	ProjectID, Title, Description, Status, Priority, AssigneeID, CreatedBy string
	TypeID, ParentID                                                       string
	Labels                                                                 []string
	CycleID                                                                string
	StartOn, DueOn                                                         *time.Time
	Estimate                                                               *int
}

// Stored is an issue as stored.
type Stored struct {
	ID, ProjectID                  string
	Number                         int
	Title, Description             string
	Status, Priority               string
	AssigneeID, AssigneeName       string
	CreatedByID, CreatedByName     string
	CreatedByAgent                 string
	CreatedAt, UpdatedAt, StatusAt time.Time
	Labels                         []string
	Comments                       int
	TypeID, ParentID               string
	// Its cycle, dates and estimate (D-90); CarriedFrom is the number of the
	// cycle that carried it into its own, ChildrenPoints its children's.
	CycleID, CycleName       string
	CycleNumber, CarriedFrom int
	CycleStarts, CycleEnds   time.Time
	StartOn, DueOn           *time.Time
	Estimate                 *int
	ChildrenPoints           int
}

// Event is a history row: Kind opened, status, assigned, priority, labels,
// title, linked or merged; Automatic when an event did it.
type Event struct {
	IssueID, Kind, ActorID       string
	Automatic                    bool
	From, To                     string
	GitEventID                   int64
	PullID, DeploymentID, Branch string
}

// Move is an event's move of an issue's status.
type Move struct {
	From  []string
	To    string
	Event Event // Kind status; From is filled from the issue
}

// ListQuery is a page of the list, or (without the page) its counts.
type ListQuery struct {
	ProjectID            string
	Statuses, Priorities []string
	Labels               []string
	Assignee             string // an account's id, none, or "" for any
	HasQ                 bool
	TSQ                  string
	QNumber              int
	TypeIDs              []string
	// Untyped: issues without a type count as the workspace's first standard type.
	Untyped   bool
	ParentID  string
	ParentKey string // resolved to ParentID by the service
	// CycleKey is the filter as sent (current, next, none or a number),
	// which the service resolves: CycleID matches one cycle's issues,
	// NoCycle those in none (D-90).
	CycleKey  string
	CycleID   string
	NoCycle   bool
	Order     string // priority, updated, created, due
	AfterRank int
	AfterAt   time.Time
	AfterID   string
	Limit     int
}

// Row is a list row as stored.
type Row struct {
	ID                                      string
	Number                                  int
	Title, Status, Priority                 string
	AssigneeID, AssigneeName                string
	CreatedAt, UpdatedAt                    time.Time
	Labels                                  []string
	SortRank                                int
	SortAt                                  time.Time
	PullNumber                              int
	PullState, PullRepoID                   string
	PullHeadSHA, PullMergeSHA               string
	PullDeploys                             string // the merge's deployments' statuses, comma-separated
	Branch                                  string
	TypeID                                  string
	ParentKey, ParentTitle                  string
	Children, ChildrenShipped, ChildrenDone int
	BlockedBy                               []string
	CycleID, CycleName                      string
	CycleNumber                             int
	CycleStarts, CycleEnds                  time.Time
	StartOn, DueOn                          *time.Time
	Estimate                                *int
	ChildrenPoints                          int
}

// CheckState is one check's state on a commit.
type CheckState struct {
	RepositoryID, SHA, State string
}

// StoredItem is a timeline row.
type StoredItem struct {
	Item, ID, ActorID, ActorName, Body, Kind string
	AgentName                                string
	Automatic                                bool
	From, To, AssigneeName, Branch           string
	PullNumber                               int
	PullTitle, Repository                    string
	Service, Environment, CommitSHA          string
	CreatedAt                                time.Time
	EditedAt                                 *time.Time
}

// StoredComment is a comment as stored.
type StoredComment struct {
	ID, AuthorID, AuthorName, Body string
	AgentName                      string
	CreatedAt                      time.Time
	EditedAt                       *time.Time
}

// StoredView is a saved view as stored.
type StoredView struct {
	ID, Name, OwnerID, OwnerName string
	Filters                      []byte
	CreatedAt, UpdatedAt         time.Time
}

// StoredLink is a link with its pull request, if it is one.
type StoredLink struct {
	RepositoryID, Repository, Branch, PullID string
	PullNumber                               int
	PullTitle, PullState, Head, Base         string
	HeadSHA, MergeSHA                        string
	MergedAt                                 *time.Time
}

// Target is a service a merge into a branch deploys to.
type Target struct {
	ServiceID, Service, Environment string
	LiveDeploymentID, LiveSHA       string
}

// StoredDeploy is a service's latest deployment.
type StoredDeploy struct {
	ID, Status, SHA string
}

// GitEvent is an outbox event the issues module has not applied.
type GitEvent struct {
	ID                                    int64
	RepositoryID, ProjectID, Kind, Action string
	Branch, AfterSHA, ActorID, PullID     string
	CreatedAt                             time.Time
	PullNumber                            int
	PullTitle, PullBody, PullHead         string
}

// Candidate is an issue an event may move.
type Candidate struct {
	ID, Status string
	Number     int
	CreatedAt  time.Time
}

// ShipCandidate is an issue linked to a merged pull request.
type ShipCandidate struct {
	IssueID, Status            string
	PullID, RepositoryID, Base string
	PullNumber                 int
	MergeSHA, MergedBy         string
	MergedAt                   time.Time
	HasOpen                    bool
}

// SearchQuery is ⌘K's.
type SearchQuery struct {
	OrgID, ProjectID string
	HasQ             bool
	TSQ, QKey        string
	QPrefix          string // a key on its own ("vlb", "VLB-"): its project's issues
	QNumber, Limit   int
}
