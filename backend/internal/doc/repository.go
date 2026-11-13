package doc

import (
	"context"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/issue"
)

// Repository stores pages (D-86).
type Repository interface {
	CountPages(ctx context.Context, projectID string) (int, error)
	// Tree is the project's pages, or (deleted) those deleted in the last
	// 30 days, without Stale.
	Tree(ctx context.Context, projectID string, deleted bool) ([]TreePage, error)
	// Page is ErrNotFound unless the page is the project's (deleted in the
	// last 30 days included).
	Page(ctx context.Context, projectID, pageID string) (*stored, error)
	// Path is the pages a page is in, from the top.
	Path(ctx context.Context, pageID string) ([]code.DocRef, error)
	// Create writes version 1 and what it links, in one transaction.
	Create(ctx context.Context, p newPage) (string, error)
	// Save writes the next version and what it links in one transaction:
	// ErrConflict unless base is the current version.
	Save(ctx context.Context, p savePage) (int, error)
	// Move puts the page under parent ("" the top) at position among the
	// pages there.
	Move(ctx context.Context, projectID, pageID, parentID string, position int) error
	LiveChildren(ctx context.Context, pageID string) (int, error)
	Delete(ctx context.Context, pageID string) (bool, error)
	Restore(ctx context.Context, pageID string) (bool, error)
	Purge(ctx context.Context) (int, error)
	// Verify takes what the page saw of each service it names from them
	// now, and marks it verified.
	Verify(ctx context.Context, pageID, actorID string) error
	// Versions pages a page's versions, newest first, before a version (0
	// from the newest).
	Versions(ctx context.Context, pageID string, before, limit int) ([]Version, error)
	Version(ctx context.Context, pageID string, version int) (*VersionDetail, error)

	// ProjectServices is which of ids are the project's services.
	ProjectServices(ctx context.Context, projectID string, ids []string) ([]string, error)
	// ServiceStates is the services pages name (pageIDs, or every page of
	// projectID), as the pages saw them and as they are now.
	ServiceStates(ctx context.Context, pageIDs []string, projectID string) ([]serviceNow, error)
	// ResolveIssues maps KEY-12 refs to issue ids in the workspace.
	ResolveIssues(ctx context.Context, orgID string, refs []string) (map[string]string, error)
	// ResolvePulls maps repository!40 refs to pull request ids in the project.
	ResolvePulls(ctx context.Context, projectID string, refs []string) (map[string]string, error)
	RepositoryNames(ctx context.Context, projectID string) ([]string, error)
	ExistingPages(ctx context.Context, projectID string, ids []string) ([]string, error)
	Issues(ctx context.Context, ids []string) (map[string]Issue, error)
	Pulls(ctx context.Context, ids []string) (map[string]Pull, error)
	// Referencing is the live pages that name an issue, a pull request or
	// a page (one of them set).
	Referencing(ctx context.Context, issueID, pullID, pageID string) ([]code.DocRef, error)
	Search(ctx context.Context, orgID, projectID, tsq string, limit int) ([]issue.DocHit, error)

	ServiceForWork(ctx context.Context, serviceID string) (*workService, error)
	ServiceIssues(ctx context.Context, projectID, label string) ([]Issue, error)
	ServicePulls(ctx context.Context, repoID, branch string) ([]workPull, error)
	ServicePages(ctx context.Context, serviceID string) ([]WorkPage, error)
	// PagesBy are the live pages a person wrote or edited (D-88).
	PagesBy(ctx context.Context, orgID, userID string) ([]inbox.StalePage, error)
}

// stored is a page as the repository reads it.
type stored struct {
	ID, ProjectID, ParentID string
	Title, Body             string
	Version, Position       int
	CreatedByID, CreatedBy  string
	UpdatedByID, UpdatedBy  string
	VerifiedByID, Verifier  string
	CreatedAt, UpdatedAt    time.Time
	VerifiedAt, DeletedAt   *time.Time
}

// links is what a version names: the project's services its blocks name,
// and the issues, pull requests and pages it names.
type links struct {
	Services, Issues, Pulls, Pages []string
}

type newPage struct {
	ProjectID, ParentID, Title, Body, ActorID string
	Links                                     links
}

type savePage struct {
	ID, Title, Body, ActorID string
	Base                     int
	Links                    links
}

type workService struct {
	ID, Name, ProjectID, OrgID string
	RepositoryID, Branch, Dir  string
}

type workPull struct {
	Pull
	HeadSHA string
}
