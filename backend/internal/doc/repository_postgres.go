package doc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepository struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
}

// NewPostgresRepository stores pages in Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepository{pool: pool, q: sqlcdb.New(pool)}
}

func id(s string) pgtype.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func ids(ss []string) []pgtype.UUID {
	out := make([]pgtype.UUID, 0, len(ss))
	for _, s := range ss {
		if u := id(s); u.Valid {
			out = append(out, u)
		}
	}
	return out
}

func str(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func strp(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := str(u)
	return &s
}

func at(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r *postgresRepository) inTx(ctx context.Context, f func(q *sqlcdb.Queries) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := f(r.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *postgresRepository) CountPages(ctx context.Context, projectID string) (int, error) {
	n, err := r.q.CountDocPages(ctx, id(projectID))
	if err != nil {
		return 0, fmt.Errorf("CountDocPages: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) Tree(ctx context.Context, projectID string, deleted bool) ([]TreePage, error) {
	rows, err := r.q.ListDocTree(ctx, sqlcdb.ListDocTreeParams{ProjectID: id(projectID), Deleted: deleted})
	if err != nil {
		return nil, fmt.Errorf("ListDocTree: %w", err)
	}
	out := make([]TreePage, 0, len(rows))
	for _, row := range rows {
		out = append(out, TreePage{ID: str(row.ID), ParentID: strp(row.ParentID), Title: row.Title, Position: int(row.Position),
			Runbook: row.Runbook, UpdatedAt: row.UpdatedAt.Time, DeletedAt: at(row.DeletedAt)})
	}
	return out, nil
}

func (r *postgresRepository) Page(ctx context.Context, projectID, pageID string) (*stored, error) {
	if !id(pageID).Valid {
		return nil, ErrNotFound
	}
	row, err := r.q.GetDocPage(ctx, sqlcdb.GetDocPageParams{ProjectID: id(projectID), ID: id(pageID)})
	if err != nil {
		return nil, fmt.Errorf("GetDocPage: %w", notFound(err))
	}
	return &stored{ID: str(row.ID), ProjectID: str(row.ProjectID), ParentID: str(row.ParentID), Title: row.Title, Body: row.Body,
		Version: int(row.Version), Position: int(row.Position),
		CreatedByID: str(row.CreatedBy), CreatedBy: row.CreatedByName, UpdatedByID: str(row.UpdatedBy), UpdatedBy: row.UpdatedByName,
		VerifiedByID: str(row.VerifiedBy), Verifier: row.VerifiedByName,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, VerifiedAt: at(row.VerifiedAt), DeletedAt: at(row.DeletedAt)}, nil
}

func (r *postgresRepository) Path(ctx context.Context, pageID string) ([]code.DocRef, error) {
	rows, err := r.q.GetDocPath(ctx, id(pageID))
	if err != nil {
		return nil, fmt.Errorf("GetDocPath: %w", err)
	}
	out := make([]code.DocRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, code.DocRef{ID: str(row.ID), Title: row.Title, ProjectID: str(row.ProjectID)})
	}
	return out, nil
}

func (r *postgresRepository) Create(ctx context.Context, p newPage) (string, error) {
	var pageID string
	err := r.inTx(ctx, func(q *sqlcdb.Queries) error {
		pid, err := q.CreateDocPage(ctx, sqlcdb.CreateDocPageParams{ProjectID: id(p.ProjectID), ParentID: id(p.ParentID),
			Title: p.Title, Body: p.Body, ActorID: id(p.ActorID)})
		if err != nil {
			return fmt.Errorf("CreateDocPage: %w", err)
		}
		pageID = str(pid)
		if err := q.InsertDocVersion(ctx, sqlcdb.InsertDocVersionParams{PageID: pid, Version: 1, Title: p.Title, Body: p.Body, AuthorID: id(p.ActorID)}); err != nil {
			return fmt.Errorf("InsertDocVersion: %w", err)
		}
		return link(ctx, q, pid, p.Links)
	})
	return pageID, err
}

func (r *postgresRepository) Save(ctx context.Context, p savePage) (int, error) {
	var version int
	err := r.inTx(ctx, func(q *sqlcdb.Queries) error {
		v, err := q.SaveDocContent(ctx, sqlcdb.SaveDocContentParams{ID: id(p.ID), Title: p.Title, Body: p.Body,
			ActorID: id(p.ActorID), BaseVersion: int32(p.Base)})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("SaveDocContent: %w", err)
		}
		version = int(v)
		if err := q.InsertDocVersion(ctx, sqlcdb.InsertDocVersionParams{PageID: id(p.ID), Version: v, Title: p.Title, Body: p.Body, AuthorID: id(p.ActorID)}); err != nil {
			return fmt.Errorf("InsertDocVersion: %w", err)
		}
		return link(ctx, q, id(p.ID), p.Links)
	})
	return version, err
}

// link replaces what the page names. A service it newly names is seen as
// it is now; the others keep what the page saw of them.
func link(ctx context.Context, q *sqlcdb.Queries, pageID pgtype.UUID, l links) error {
	if err := q.UnlinkDocServices(ctx, sqlcdb.UnlinkDocServicesParams{PageID: pageID, Keep: ids(l.Services)}); err != nil {
		return fmt.Errorf("UnlinkDocServices: %w", err)
	}
	if len(l.Services) > 0 {
		empty := map[string]json.RawMessage{}
		for _, s := range l.Services {
			empty[s] = json.RawMessage(`{}`)
		}
		if err := see(ctx, q, pageID, empty, false); err != nil {
			return err
		}
		rows, err := q.ListDocServiceState(ctx, sqlcdb.ListDocServiceStateParams{PageIds: []pgtype.UUID{pageID}})
		if err != nil {
			return fmt.Errorf("ListDocServiceState: %w", err)
		}
		fresh := map[string]json.RawMessage{}
		for _, row := range rows {
			if bytes.Equal(bytes.TrimSpace(row.Seen), []byte("{}")) {
				b, err := json.Marshal(stateOf(row).seen())
				if err != nil {
					return err
				}
				fresh[str(row.ServiceID)] = b
			}
		}
		if len(fresh) > 0 {
			if err := see(ctx, q, pageID, fresh, true); err != nil {
				return err
			}
		}
	}
	if err := q.ReplaceDocRefsDelete(ctx, pageID); err != nil {
		return fmt.Errorf("ReplaceDocRefsDelete: %w", err)
	}
	if err := q.InsertDocRefs(ctx, sqlcdb.InsertDocRefsParams{PageID: pageID, IssueIds: ids(l.Issues), PullIds: ids(l.Pulls), PageIds: ids(l.Pages)}); err != nil {
		return fmt.Errorf("InsertDocRefs: %w", err)
	}
	return nil
}

func see(ctx context.Context, q *sqlcdb.Queries, pageID pgtype.UUID, seen map[string]json.RawMessage, all bool) error {
	b, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	if err := q.SeeDocServices(ctx, sqlcdb.SeeDocServicesParams{PageID: pageID, Seen: b, AllLinks: all}); err != nil {
		return fmt.Errorf("SeeDocServices: %w", err)
	}
	return nil
}

func (r *postgresRepository) Move(ctx context.Context, projectID, pageID, parentID string, position int) error {
	return r.inTx(ctx, func(q *sqlcdb.Queries) error {
		sibs, err := q.ListDocSiblings(ctx, sqlcdb.ListDocSiblingsParams{ProjectID: id(projectID), ParentID: id(parentID), ExceptID: id(pageID)})
		if err != nil {
			return fmt.Errorf("ListDocSiblings: %w", err)
		}
		position = min(max(position, 0), len(sibs))
		order := make([]pgtype.UUID, 0, len(sibs)+1)
		order = append(order, sibs[:position]...)
		order = append(order, id(pageID))
		order = append(order, sibs[position:]...)
		if err := q.PlaceDocPages(ctx, sqlcdb.PlaceDocPagesParams{ParentID: id(parentID), Ids: order, MovedID: id(pageID)}); err != nil {
			return fmt.Errorf("PlaceDocPages: %w", err)
		}
		return nil
	})
}

func (r *postgresRepository) LiveChildren(ctx context.Context, pageID string) (int, error) {
	n, err := r.q.CountLiveDocChildren(ctx, id(pageID))
	if err != nil {
		return 0, fmt.Errorf("CountLiveDocChildren: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) Delete(ctx context.Context, pageID string) (bool, error) {
	n, err := r.q.DeleteDocPage(ctx, id(pageID))
	if err != nil {
		return false, fmt.Errorf("DeleteDocPage: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) Restore(ctx context.Context, pageID string) (bool, error) {
	n, err := r.q.RestoreDocPage(ctx, id(pageID))
	if err != nil {
		return false, fmt.Errorf("RestoreDocPage: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) Purge(ctx context.Context) (int, error) {
	n, err := r.q.PurgeDocPages(ctx)
	if err != nil {
		return 0, fmt.Errorf("PurgeDocPages: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) Verify(ctx context.Context, pageID, actorID string) error {
	return r.inTx(ctx, func(q *sqlcdb.Queries) error {
		rows, err := q.ListDocServiceState(ctx, sqlcdb.ListDocServiceStateParams{PageIds: []pgtype.UUID{id(pageID)}})
		if err != nil {
			return fmt.Errorf("ListDocServiceState: %w", err)
		}
		now := map[string]json.RawMessage{}
		for _, row := range rows {
			b, err := json.Marshal(stateOf(row).seen())
			if err != nil {
				return err
			}
			now[str(row.ServiceID)] = b
		}
		if len(now) > 0 {
			if err := see(ctx, q, id(pageID), now, true); err != nil {
				return err
			}
		}
		if err := q.VerifyDocPage(ctx, sqlcdb.VerifyDocPageParams{ID: id(pageID), VerifiedBy: id(actorID)}); err != nil {
			return fmt.Errorf("VerifyDocPage: %w", err)
		}
		return nil
	})
}

func (r *postgresRepository) Versions(ctx context.Context, pageID string, before, limit int) ([]Version, error) {
	rows, err := r.q.ListDocVersions(ctx, sqlcdb.ListDocVersionsParams{PageID: id(pageID), Before: int32(before), RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("ListDocVersions: %w", err)
	}
	out := make([]Version, 0, len(rows))
	for _, row := range rows {
		out = append(out, Version{Version: int(row.Version), Title: row.Title, Author: person(str(row.AuthorID), row.AuthorName), CreatedAt: row.CreatedAt.Time})
	}
	return out, nil
}

func (r *postgresRepository) Version(ctx context.Context, pageID string, version int) (*VersionDetail, error) {
	row, err := r.q.GetDocVersion(ctx, sqlcdb.GetDocVersionParams{PageID: id(pageID), Version: int32(version)})
	if err != nil {
		return nil, fmt.Errorf("GetDocVersion: %w", notFound(err))
	}
	return &VersionDetail{Version: Version{Version: int(row.Version), Title: row.Title, Author: person(str(row.AuthorID), row.AuthorName),
		CreatedAt: row.CreatedAt.Time}, Body: row.Body}, nil
}

func (r *postgresRepository) ProjectServices(ctx context.Context, projectID string, svcIDs []string) ([]string, error) {
	rows, err := r.q.ProjectServiceIDs(ctx, sqlcdb.ProjectServiceIDsParams{ProjectID: id(projectID), Ids: ids(svcIDs)})
	if err != nil {
		return nil, fmt.Errorf("ProjectServiceIDs: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, str(row))
	}
	return out, nil
}

func stateOf(row sqlcdb.ListDocServiceStateRow) serviceNow {
	s := serviceNow{PageID: str(row.PageID), ServiceID: str(row.ServiceID), Seen: row.Seen, SeenAt: row.SeenAt.Time,
		Name: row.Name, Environment: row.Environment, Status: row.RuntimeStatus, SourceType: row.SourceType,
		DockerImage: row.DockerImage, BuildPack: row.BuildPack, BaseDirectory: row.BaseDirectory, Install: row.InstallCommand,
		BuildCmd: row.BuildCommand, Start: row.StartCommand, Port: int(row.Port), ComposeHash: row.ComposeHash, ComposeFile: row.ComposeFilePath,
		ServerID: row.ServerID, ServerName: row.ServerName, LiveImage: row.LiveImage, LiveCommit: row.LiveCommit, LiveAt: at(row.LiveAt),
		SinceID: str(row.SinceID), SinceAt: at(row.SinceAt), Vars: map[string]varSeen{}}
	if row.SinceCommit != nil {
		s.SinceCommit = *row.SinceCommit
	}
	_ = json.Unmarshal(row.Vars, &s.Vars)
	return s
}

func (r *postgresRepository) ServiceStates(ctx context.Context, pageIDs []string, projectID string) ([]serviceNow, error) {
	rows, err := r.q.ListDocServiceState(ctx, sqlcdb.ListDocServiceStateParams{PageIds: ids(pageIDs), ProjectID: id(projectID)})
	if err != nil {
		return nil, fmt.Errorf("ListDocServiceState: %w", err)
	}
	out := make([]serviceNow, 0, len(rows))
	for _, row := range rows {
		out = append(out, stateOf(row))
	}
	return out, nil
}

func (r *postgresRepository) ResolveIssues(ctx context.Context, orgID string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	rows, err := r.q.ResolveDocIssues(ctx, sqlcdb.ResolveDocIssuesParams{OrgID: id(orgID), Refs: refs})
	if err != nil {
		return nil, fmt.Errorf("ResolveDocIssues: %w", err)
	}
	for _, row := range rows {
		out[row.Ref] = str(row.ID)
	}
	return out, nil
}

func (r *postgresRepository) ResolvePulls(ctx context.Context, projectID string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	rows, err := r.q.ResolveDocPulls(ctx, sqlcdb.ResolveDocPullsParams{ProjectID: id(projectID), Refs: refs})
	if err != nil {
		return nil, fmt.Errorf("ResolveDocPulls: %w", err)
	}
	for _, row := range rows {
		out[row.Ref] = str(row.ID)
	}
	return out, nil
}

func (r *postgresRepository) RepositoryNames(ctx context.Context, projectID string) ([]string, error) {
	names, err := r.q.ProjectRepositoryNames(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ProjectRepositoryNames: %w", err)
	}
	return names, nil
}

func (r *postgresRepository) ExistingPages(ctx context.Context, projectID string, pageIDs []string) ([]string, error) {
	rows, err := r.q.ExistingDocPages(ctx, sqlcdb.ExistingDocPagesParams{ProjectID: id(projectID), Ids: ids(pageIDs)})
	if err != nil {
		return nil, fmt.Errorf("ExistingDocPages: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, str(row))
	}
	return out, nil
}

func (r *postgresRepository) Issues(ctx context.Context, issueIDs []string) (map[string]Issue, error) {
	out := map[string]Issue{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	rows, err := r.q.DocIssues(ctx, ids(issueIDs))
	if err != nil {
		return nil, fmt.Errorf("DocIssues: %w", err)
	}
	for _, row := range rows {
		out[str(row.ID)] = Issue{Key: issue.KeyOf(row.IssueKey, int(row.Number), int(row.Digits)), Number: int(row.Number), Title: row.Title,
			Status: row.Status, ProjectID: str(row.ProjectID), Assignee: person(str(row.AssigneeID), row.AssigneeName)}
	}
	return out, nil
}

func (r *postgresRepository) Pulls(ctx context.Context, pullIDs []string) (map[string]Pull, error) {
	out := map[string]Pull{}
	if len(pullIDs) == 0 {
		return out, nil
	}
	rows, err := r.q.DocPulls(ctx, ids(pullIDs))
	if err != nil {
		return nil, fmt.Errorf("DocPulls: %w", err)
	}
	for _, row := range rows {
		out[str(row.ID)] = Pull{Repository: row.Repository, RepositoryID: str(row.RepositoryID), Number: int(row.Number),
			Title: row.Title, State: row.State, Checks: checksOf(row.CheckStates)}
	}
	return out, nil
}

func checksOf(states []string) string {
	if s := code.SummarizeStates(states); s != nil {
		return s.State
	}
	return ""
}

func (r *postgresRepository) Referencing(ctx context.Context, issueID, pullID, pageID string) ([]code.DocRef, error) {
	rows, err := r.q.DocsReferencing(ctx, sqlcdb.DocsReferencingParams{IssueID: id(issueID), PullID: id(pullID), PageID: id(pageID)})
	if err != nil {
		return nil, fmt.Errorf("DocsReferencing: %w", err)
	}
	out := make([]code.DocRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, code.DocRef{ID: str(row.ID), Title: row.Title, ProjectID: str(row.ProjectID)})
	}
	return out, nil
}

var plain = regexp.MustCompile("(?m)^```.*$|[#*_`>\\[\\]]+")

func (r *postgresRepository) Search(ctx context.Context, orgID, projectID, tsq string, limit int) ([]issue.DocHit, error) {
	rows, err := r.q.SearchDocs(ctx, sqlcdb.SearchDocsParams{OrgID: id(orgID), ProjectID: id(projectID), Tsq: tsq, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("SearchDocs: %w", err)
	}
	out := make([]issue.DocHit, 0, len(rows))
	for _, row := range rows {
		snip := strings.Join(strings.Fields(plain.ReplaceAllString(row.BodyStart, " ")), " ")
		if len([]rune(snip)) > 140 {
			snip = string([]rune(snip)[:140]) + "…"
		}
		out = append(out, issue.DocHit{ID: str(row.ID), ProjectID: str(row.ProjectID), Project: row.ProjectName, Title: row.Title, Snippet: snip})
	}
	return out, nil
}

func (r *postgresRepository) ServiceForWork(ctx context.Context, serviceID string) (*workService, error) {
	if !id(serviceID).Valid {
		return nil, ErrNotFound
	}
	row, err := r.q.GetServiceForWork(ctx, id(serviceID))
	if err != nil {
		return nil, fmt.Errorf("GetServiceForWork: %w", notFound(err))
	}
	return &workService{ID: str(row.ID), Name: row.Name, ProjectID: str(row.ProjectID), OrgID: str(row.OrgID),
		RepositoryID: str(row.RepositoryID), Branch: row.GitBranch, Dir: row.BaseDirectory}, nil
}

func (r *postgresRepository) ServiceIssues(ctx context.Context, projectID, label string) ([]Issue, error) {
	rows, err := r.q.ServiceIssues(ctx, sqlcdb.ServiceIssuesParams{ProjectID: id(projectID), Label: label})
	if err != nil {
		return nil, fmt.Errorf("ServiceIssues: %w", err)
	}
	out := make([]Issue, 0, len(rows))
	for _, row := range rows {
		out = append(out, Issue{Key: issue.KeyOf(row.IssueKey, int(row.Number), int(row.Digits)), Number: int(row.Number), Title: row.Title,
			Status: row.Status, Priority: row.Priority, ProjectID: str(row.ProjectID)})
	}
	return out, nil
}

func (r *postgresRepository) ServicePulls(ctx context.Context, repoID, branch string) ([]workPull, error) {
	rows, err := r.q.ServiceOpenPulls(ctx, sqlcdb.ServiceOpenPullsParams{RepositoryID: id(repoID), Branch: branch})
	if err != nil {
		return nil, fmt.Errorf("ServiceOpenPulls: %w", err)
	}
	out := make([]workPull, 0, len(rows))
	for _, row := range rows {
		out = append(out, workPull{Pull: Pull{Repository: row.Repository, RepositoryID: str(row.RepositoryID), Number: int(row.Number),
			Title: row.Title, State: row.State, Checks: checksOf(row.CheckStates)}, HeadSHA: row.HeadSha})
	}
	return out, nil
}

func (r *postgresRepository) ServicePages(ctx context.Context, serviceID string) ([]WorkPage, error) {
	rows, err := r.q.ServiceDocPages(ctx, id(serviceID))
	if err != nil {
		return nil, fmt.Errorf("ServiceDocPages: %w", err)
	}
	out := make([]WorkPage, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkPage{ID: str(row.ID), Title: row.Title, VerifiedAt: at(row.VerifiedAt)})
	}
	return out, nil
}

func (r *postgresRepository) PagesBy(ctx context.Context, orgID, userID string) ([]inbox.StalePage, error) {
	rows, err := r.q.DocPagesByAuthor(ctx, sqlcdb.DocPagesByAuthorParams{OrgID: id(orgID), UserID: id(userID)})
	if err != nil {
		return nil, fmt.Errorf("DocPagesByAuthor: %w", err)
	}
	out := make([]inbox.StalePage, 0, len(rows))
	for _, row := range rows {
		out = append(out, inbox.StalePage{ID: str(row.ID), ProjectID: str(row.ProjectID), Project: row.ProjectName, Title: row.Title, Since: row.Since.Time})
	}
	return out, nil
}
