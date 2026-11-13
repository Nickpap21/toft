package issue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepository struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
}

// NewPostgresRepository stores issues in Postgres.
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

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func at(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (r *postgresRepository) Project(ctx context.Context, projectID string) (*Project, error) {
	row, err := r.q.GetIssueProject(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("GetIssueProject: %w", notFound(err))
	}
	pr := &Project{ID: str(row.ID), OrgID: str(row.OrgID), Name: row.Name, Key: deref(row.IssueKey),
		Digits: int(row.Digits), OrgDigits: int(row.OrgDigits), DefaultTypeID: str(row.DefaultIssueTypeID), NextNumber: int(row.NextIssueNumber),
		FailedDeployIssues: row.FailedDeployIssues,
		Cycles:             row.CycleWeeks != nil, Zone: row.CycleTimeZone, Estimates: row.IssueEstimates}
	if row.IssueKeyDigits != nil {
		d := int(*row.IssueKeyDigits)
		pr.OwnDigits = &d
	}
	return pr, nil
}

func (r *postgresRepository) OrgKeys(ctx context.Context, orgID string) ([]string, error) {
	keys, err := r.q.ListOrgIssueKeys(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListOrgIssueKeys: %w", err)
	}
	return keys, nil
}

func (r *postgresRepository) SetKey(ctx context.Context, projectID, key string) (bool, error) {
	n, err := r.q.SetProjectIssueKey(ctx, sqlcdb.SetProjectIssueKeyParams{ID: id(projectID), IssueKey: &key})
	if isUnique(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("SetProjectIssueKey: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) MemberRole(ctx context.Context, orgID, userID string) (string, error) {
	role, err := r.q.IssueAssigneeRole(ctx, sqlcdb.IssueAssigneeRoleParams{OrgID: id(orgID), UserID: id(userID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("IssueAssigneeRole: %w", err)
	}
	return role, nil
}

// inTx runs f in a transaction.
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

func insertEvent(ctx context.Context, q *sqlcdb.Queries, e Event) (bool, error) {
	var gitEvent *int64
	if e.GitEventID != 0 {
		gitEvent = &e.GitEventID
	}
	n, err := q.InsertIssueEvent(ctx, sqlcdb.InsertIssueEventParams{
		IssueID: id(e.IssueID), Kind: e.Kind, ActorID: id(e.ActorID), Automatic: e.Automatic,
		FromValue: opt(e.From), ToValue: opt(e.To), GitEventID: gitEvent,
		PullRequestID: id(e.PullID), DeploymentID: id(e.DeploymentID), Branch: opt(e.Branch),
	})
	if err != nil {
		return false, fmt.Errorf("InsertIssueEvent: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) Create(ctx context.Context, in NewIssue) (int, error) {
	var number int
	err := r.inTx(ctx, func(q *sqlcdb.Queries) error {
		n, err := q.NextIssueNumber(ctx, id(in.ProjectID))
		if err != nil {
			return fmt.Errorf("NextIssueNumber: %w", err)
		}
		number = int(n)
		issueID, err := q.InsertIssue(ctx, sqlcdb.InsertIssueParams{
			ProjectID: id(in.ProjectID), Number: n, Title: in.Title, Description: in.Description,
			Status: in.Status, Priority: in.Priority, AssigneeID: id(in.AssigneeID), CreatedBy: id(in.CreatedBy),
			TypeID: id(in.TypeID), ParentID: id(in.ParentID),
			CycleID: id(in.CycleID), StartOn: pgDate(in.StartOn), DueOn: pgDate(in.DueOn), Estimate: est16(in.Estimate),
			AgentConnectionID: agentOf(ctx),
		})
		if err != nil {
			return fmt.Errorf("InsertIssue: %w", err)
		}
		if len(in.Labels) > 0 {
			if err := q.InsertIssueLabels(ctx, sqlcdb.InsertIssueLabelsParams{IssueID: issueID, Names: in.Labels}); err != nil {
				return fmt.Errorf("InsertIssueLabels: %w", err)
			}
		}
		events := []Event{{Kind: "opened", ActorID: in.CreatedBy}}
		if in.AssigneeID != "" {
			events = append(events, Event{Kind: "assigned", ActorID: in.CreatedBy, To: in.AssigneeID})
		}
		for _, e := range events {
			e.IssueID = str(issueID)
			if _, err := insertEvent(ctx, q, e); err != nil {
				return err
			}
		}
		return nil
	})
	return number, err
}

func (r *postgresRepository) Get(ctx context.Context, projectID string, number int) (*Stored, error) {
	row, err := r.q.GetIssue(ctx, sqlcdb.GetIssueParams{ProjectID: id(projectID), Number: int32(number)})
	if err != nil {
		return nil, notFound(err)
	}
	return &Stored{
		ID: str(row.ID), ProjectID: str(row.ProjectID), Number: int(row.Number), Title: row.Title,
		Description: row.Description, Status: row.Status, Priority: row.Priority,
		AssigneeID: str(row.AssigneeID), AssigneeName: row.AssigneeName,
		CreatedByID: str(row.CreatedBy), CreatedByName: row.CreatedByName, CreatedByAgent: row.CreatedByAgent,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, StatusAt: row.StatusChangedAt.Time,
		Labels: row.Labels, Comments: int(row.Comments), TypeID: str(row.TypeID), ParentID: str(row.ParentID),
		CycleID: str(row.CycleID), CycleName: row.CycleName, CycleNumber: int(row.CycleNumber), CarriedFrom: int(row.CarriedFrom),
		CycleStarts: dateOf(row.CycleStartsOn), CycleEnds: dateOf(row.CycleEndsOn),
		StartOn: fromDate(row.StartOn), DueOn: fromDate(row.DueOn), Estimate: estInt(row.Estimate), ChildrenPoints: int(row.ChildrenPoints),
	}, nil
}

// Update stores s. A labelsVersion is the labels' from the read the request
// came from, sent when it replaces them: the issue's lock conflicts with the
// FOR KEY SHARE that adding a label takes through its foreign key, the rows'
// locks hold the ones the check saw, and a stale one writes nothing at all
// (listversion.ErrChanged).
func (r *postgresRepository) Update(ctx context.Context, s *Stored, labelsChanged bool, labelsVersion string, events []Event) error {
	return r.inTx(ctx, func(q *sqlcdb.Queries) error {
		if labelsVersion != "" {
			if _, err := q.LockIssueForReplace(ctx, id(s.ID)); err != nil {
				return fmt.Errorf("LockIssueForReplace: %w", err)
			}
			stored, err := q.ListIssueLabelsForReplace(ctx, id(s.ID))
			if err != nil {
				return fmt.Errorf("ListIssueLabelsForReplace: %w", err)
			}
			if listversion.Of(stored) != labelsVersion {
				return listversion.ErrChanged
			}
		}
		if err := q.UpdateIssue(ctx, sqlcdb.UpdateIssueParams{
			ID: id(s.ID), Title: s.Title, Description: s.Description, Priority: s.Priority,
			AssigneeID: id(s.AssigneeID), Status: s.Status, TypeID: id(s.TypeID), ParentID: id(s.ParentID),
			CycleID: id(s.CycleID), StartOn: pgDate(s.StartOn), DueOn: pgDate(s.DueOn), Estimate: est16(s.Estimate),
		}); err != nil {
			return fmt.Errorf("UpdateIssue: %w", err)
		}
		if labelsChanged {
			if err := q.DeleteIssueLabels(ctx, id(s.ID)); err != nil {
				return fmt.Errorf("DeleteIssueLabels: %w", err)
			}
			if err := q.InsertIssueLabels(ctx, sqlcdb.InsertIssueLabelsParams{IssueID: id(s.ID), Names: s.Labels}); err != nil {
				return fmt.Errorf("InsertIssueLabels: %w", err)
			}
		}
		for _, e := range events {
			if _, err := insertEvent(ctx, q, e); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *postgresRepository) List(ctx context.Context, lq ListQuery) ([]Row, error) {
	p := sqlcdb.ListIssuesParams{
		Sort: lq.Order, ProjectID: id(lq.ProjectID), Statuses: lq.Statuses, Priorities: lq.Priorities,
		Assignee: lq.Assignee, Labels: lq.Labels, HasQ: lq.HasQ, Tsq: lq.TSQ, QNumber: int32(lq.QNumber),
		TypeIds: ids(lq.TypeIDs), Untyped: lq.Untyped, ParentID: id(lq.ParentID),
		CycleID: id(lq.CycleID), NoCycle: lq.NoCycle,
		RowLimit: int32(lq.Limit),
	}
	if lq.AfterID != "" {
		rank := int32(lq.AfterRank)
		p.AfterID, p.AfterRank, p.AfterAt = id(lq.AfterID), &rank, ts(lq.AfterAt)
	}
	rows, err := r.q.ListIssues(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("ListIssues: %w", err)
	}
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		out = append(out, Row{
			ID: str(row.ID), Number: int(row.Number), Title: row.Title, Status: row.Status, Priority: row.Priority,
			AssigneeID: str(row.AssigneeID), AssigneeName: row.AssigneeName,
			CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, Labels: row.Labels,
			SortRank: int(row.SortRank), SortAt: row.SortAt.Time,
			PullNumber: int(row.PullNumber), PullState: row.PullState, PullRepoID: str(row.PullRepositoryID),
			PullHeadSHA: row.PullHeadSha, PullMergeSHA: row.PullMergeSha, PullDeploys: row.PullDeploys,
			Branch: deref(row.Branch), TypeID: str(row.TypeID), ParentKey: row.ParentKey, ParentTitle: row.ParentTitle,
			Children: int(row.Children), ChildrenShipped: int(row.ChildrenShipped), ChildrenDone: int(row.ChildrenDone),
			BlockedBy: row.BlockedBy,
			CycleID:   str(row.CycleID), CycleName: row.CycleName, CycleNumber: int(row.CycleNumber),
			CycleStarts: dateOf(row.CycleStartsOn), CycleEnds: dateOf(row.CycleEndsOn),
			StartOn: fromDate(row.StartOn), DueOn: fromDate(row.DueOn), Estimate: estInt(row.Estimate), ChildrenPoints: int(row.ChildrenPoints),
		})
	}
	return out, nil
}

func (r *postgresRepository) Count(ctx context.Context, lq ListQuery) (map[string]int, error) {
	rows, err := r.q.CountIssuesByStatus(ctx, sqlcdb.CountIssuesByStatusParams{
		ProjectID: id(lq.ProjectID), Priorities: lq.Priorities, Assignee: lq.Assignee, Labels: lq.Labels,
		HasQ: lq.HasQ, Tsq: lq.TSQ, QNumber: int32(lq.QNumber),
		TypeIds: ids(lq.TypeIDs), Untyped: lq.Untyped, ParentID: id(lq.ParentID),
		CycleID: id(lq.CycleID), NoCycle: lq.NoCycle,
	})
	if err != nil {
		return nil, fmt.Errorf("CountIssuesByStatus: %w", err)
	}
	out := make(map[string]int, len(statuses))
	for _, s := range statuses {
		out[s] = 0
	}
	for _, row := range rows {
		out[row.Status] = int(row.N)
	}
	return out, nil
}

func (r *postgresRepository) CheckStates(ctx context.Context, shas []string) ([]CheckState, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListCheckStates(ctx, shas)
	if err != nil {
		return nil, fmt.Errorf("ListCheckStates: %w", err)
	}
	out := make([]CheckState, 0, len(rows))
	for _, row := range rows {
		out = append(out, CheckState{RepositoryID: str(row.RepositoryID), SHA: row.Sha, State: row.State})
	}
	return out, nil
}

func (r *postgresRepository) Timeline(ctx context.Context, issueID string, afterAt time.Time, afterID string, limit int) ([]StoredItem, error) {
	rows, err := r.q.ListIssueTimeline(ctx, sqlcdb.ListIssueTimelineParams{
		IssueID: id(issueID), AfterAt: ts(afterAt), AfterID: opt(afterID), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("ListIssueTimeline: %w", err)
	}
	out := make([]StoredItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredItem{
			Item: row.Item, ID: row.ID, ActorID: str(row.ActorID), ActorName: row.ActorName, AgentName: row.AgentName, Body: row.Body,
			Kind: row.Kind, Automatic: row.Automatic, From: row.FromValue, To: row.ToValue,
			AssigneeName: row.AssigneeName, Branch: row.Branch, PullNumber: int(row.PullNumber),
			PullTitle: row.PullTitle, Repository: row.RepositoryName, Service: row.ServiceName,
			Environment: row.EnvironmentName, CommitSHA: row.CommitSha,
			CreatedAt: row.CreatedAt.Time, EditedAt: at(row.EditedAt),
		})
	}
	return out, nil
}

func (r *postgresRepository) AddComment(ctx context.Context, issueID, authorID, body string) (*StoredComment, error) {
	var c *StoredComment
	err := r.inTx(ctx, func(q *sqlcdb.Queries) error {
		row, err := q.InsertIssueComment(ctx, sqlcdb.InsertIssueCommentParams{IssueID: id(issueID), AuthorID: id(authorID), Body: body,
			AgentConnectionID: agentOf(ctx)})
		if err != nil {
			return fmt.Errorf("InsertIssueComment: %w", err)
		}
		if err := q.TouchIssue(ctx, id(issueID)); err != nil {
			return fmt.Errorf("TouchIssue: %w", err)
		}
		c = &StoredComment{ID: str(row.ID), AuthorID: authorID, Body: body, CreatedAt: row.CreatedAt.Time}
		return nil
	})
	return c, err
}

func (r *postgresRepository) GetComment(ctx context.Context, issueID, commentID string) (*StoredComment, error) {
	row, err := r.q.GetIssueComment(ctx, sqlcdb.GetIssueCommentParams{ID: id(commentID), IssueID: id(issueID)})
	if err != nil {
		return nil, notFound(err)
	}
	return &StoredComment{ID: str(row.ID), AuthorID: str(row.AuthorID), AuthorName: row.AuthorName, AgentName: row.AgentName, Body: row.Body,
		CreatedAt: row.CreatedAt.Time, EditedAt: at(row.EditedAt)}, nil
}

// agentOf is the agent connection acting on the request, if one is
// (D-102): what it opens and writes says so.
func agentOf(ctx context.Context) pgtype.UUID {
	if a, ok := actor.AgentFrom(ctx); ok {
		return id(a.ConnectionID)
	}
	return pgtype.UUID{}
}

func (r *postgresRepository) EditComment(ctx context.Context, commentID, body string) error {
	if err := r.q.UpdateIssueComment(ctx, sqlcdb.UpdateIssueCommentParams{ID: id(commentID), Body: body}); err != nil {
		return fmt.Errorf("UpdateIssueComment: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeleteComment(ctx context.Context, commentID string) error {
	if err := r.q.DeleteIssueComment(ctx, id(commentID)); err != nil {
		return fmt.Errorf("DeleteIssueComment: %w", err)
	}
	return nil
}

func (r *postgresRepository) Labels(ctx context.Context, projectID string) ([]Label, error) {
	rows, err := r.q.ListProjectIssueLabels(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListProjectIssueLabels: %w", err)
	}
	out := make([]Label, 0, len(rows))
	for _, row := range rows {
		out = append(out, Label{Name: row.Name, Open: int(row.Open), Total: int(row.Total)})
	}
	return out, nil
}

func (r *postgresRepository) ServiceNames(ctx context.Context, projectID string) ([]string, error) {
	names, err := r.q.ListProjectServiceNames(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListProjectServiceNames: %w", err)
	}
	return names, nil
}

func toView(vid pgtype.UUID, name string, filters []byte, owner pgtype.UUID, ownerName string, created, updated pgtype.Timestamptz) StoredView {
	return StoredView{ID: str(vid), Name: name, Filters: filters, OwnerID: str(owner), OwnerName: ownerName,
		CreatedAt: created.Time, UpdatedAt: updated.Time}
}

func (r *postgresRepository) Views(ctx context.Context, projectID string) ([]StoredView, error) {
	rows, err := r.q.ListIssueViews(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListIssueViews: %w", err)
	}
	out := make([]StoredView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toView(row.ID, row.Name, row.Filters, row.OwnerID, row.OwnerName, row.CreatedAt, row.UpdatedAt))
	}
	return out, nil
}

func (r *postgresRepository) GetView(ctx context.Context, projectID, viewID string) (*StoredView, error) {
	row, err := r.q.GetIssueView(ctx, sqlcdb.GetIssueViewParams{ID: id(viewID), ProjectID: id(projectID)})
	if err != nil {
		return nil, notFound(err)
	}
	v := toView(row.ID, row.Name, row.Filters, row.OwnerID, row.OwnerName, row.CreatedAt, row.UpdatedAt)
	return &v, nil
}

func (r *postgresRepository) AddView(ctx context.Context, projectID, ownerID, name string, filters []byte) (string, error) {
	vid, err := r.q.InsertIssueView(ctx, sqlcdb.InsertIssueViewParams{ProjectID: id(projectID), OwnerID: id(ownerID), Name: name, Filters: filters})
	if isUnique(err) {
		return "", ErrNameTaken
	}
	if err != nil {
		return "", fmt.Errorf("InsertIssueView: %w", err)
	}
	return str(vid), nil
}

func (r *postgresRepository) UpdateView(ctx context.Context, viewID, name string, filters []byte) error {
	err := r.q.UpdateIssueView(ctx, sqlcdb.UpdateIssueViewParams{ID: id(viewID), Name: name, Filters: filters})
	if isUnique(err) {
		return ErrNameTaken
	}
	if err != nil {
		return fmt.Errorf("UpdateIssueView: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeleteView(ctx context.Context, viewID string) error {
	if err := r.q.DeleteIssueView(ctx, id(viewID)); err != nil {
		return fmt.Errorf("DeleteIssueView: %w", err)
	}
	return nil
}

func (r *postgresRepository) Links(ctx context.Context, issueID string) ([]StoredLink, error) {
	rows, err := r.q.ListIssueLinks(ctx, id(issueID))
	if err != nil {
		return nil, fmt.Errorf("ListIssueLinks: %w", err)
	}
	out := make([]StoredLink, 0, len(rows))
	for _, row := range rows {
		l := StoredLink{RepositoryID: str(row.RepositoryID), Repository: row.RepositoryName, Branch: deref(row.Branch), PullID: str(row.PullRequestID),
			PullTitle: deref(row.PullTitle), PullState: deref(row.PullState), Head: deref(row.HeadBranch), Base: deref(row.BaseBranch),
			HeadSHA: deref(row.HeadSha), MergeSHA: deref(row.MergeSha), MergedAt: at(row.MergedAt)}
		if row.PullNumber != nil {
			l.PullNumber = int(*row.PullNumber)
		}
		out = append(out, l)
	}
	return out, nil
}

func (r *postgresRepository) BranchTargets(ctx context.Context, repoID, branch string) ([]Target, error) {
	rows, err := r.q.ListBranchDeployTargets(ctx, sqlcdb.ListBranchDeployTargetsParams{RepositoryID: id(repoID), Branch: branch})
	if err != nil {
		return nil, fmt.Errorf("ListBranchDeployTargets: %w", err)
	}
	out := make([]Target, 0, len(rows))
	for _, row := range rows {
		out = append(out, Target{ServiceID: str(row.ID), Service: row.Name, Environment: row.EnvironmentName,
			LiveDeploymentID: str(row.LiveDeploymentID), LiveSHA: deref(row.LiveSha)})
	}
	return out, nil
}

func (r *postgresRepository) LatestSince(ctx context.Context, serviceIDs []string, since time.Time) (map[string]StoredDeploy, error) {
	rows, err := r.q.LatestDeploymentsSince(ctx, sqlcdb.LatestDeploymentsSinceParams{ServiceIds: ids(serviceIDs), Since: ts(since)})
	if err != nil {
		return nil, fmt.Errorf("LatestDeploymentsSince: %w", err)
	}
	out := make(map[string]StoredDeploy, len(rows))
	for _, row := range rows {
		out[str(row.ServiceID)] = StoredDeploy{ID: str(row.ID), Status: row.Status, SHA: deref(row.CommitSha)}
	}
	return out, nil
}

func (r *postgresRepository) Unapplied(ctx context.Context, n int) ([]GitEvent, error) {
	rows, err := r.q.ListUnappliedGitEvents(ctx, int32(n))
	if err != nil {
		return nil, fmt.Errorf("ListUnappliedGitEvents: %w", err)
	}
	out := make([]GitEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, GitEvent{
			ID: row.ID, RepositoryID: str(row.RepositoryID), ProjectID: str(row.ProjectID), Kind: row.Kind, Action: row.Action,
			Branch: row.Branch, AfterSHA: row.AfterSha, ActorID: str(row.ActorID), PullID: str(row.PullRequestID),
			CreatedAt: row.CreatedAt.Time, PullNumber: int(row.PullNumber), PullTitle: row.PullTitle,
			PullBody: row.PullBody, PullHead: row.PullHead,
		})
	}
	return out, nil
}

func (r *postgresRepository) MarkApplied(ctx context.Context, eventID int64) error {
	if err := r.q.MarkGitEventIssues(ctx, eventID); err != nil {
		return fmt.Errorf("MarkGitEventIssues: %w", err)
	}
	return nil
}

func (r *postgresRepository) ByNumbers(ctx context.Context, projectID string, numbers []int) ([]Candidate, error) {
	ns := make([]int32, 0, len(numbers))
	for _, n := range numbers {
		ns = append(ns, int32(n))
	}
	rows, err := r.q.ListIssuesByNumbers(ctx, sqlcdb.ListIssuesByNumbersParams{ProjectID: id(projectID), Numbers: ns})
	if err != nil {
		return nil, fmt.Errorf("ListIssuesByNumbers: %w", err)
	}
	out := make([]Candidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, Candidate{ID: str(row.ID), Number: int(row.Number), Status: row.Status, CreatedAt: row.CreatedAt.Time})
	}
	return out, nil
}

func (r *postgresRepository) ForPull(ctx context.Context, pullID string) ([]Candidate, error) {
	rows, err := r.q.ListIssuesForPull(ctx, id(pullID))
	if err != nil {
		return nil, fmt.Errorf("ListIssuesForPull: %w", err)
	}
	out := make([]Candidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, Candidate{ID: str(row.ID), Number: int(row.Number), Status: row.Status, CreatedAt: row.CreatedAt.Time})
	}
	return out, nil
}

func (r *postgresRepository) LinkBranch(ctx context.Context, issueID, repoID, branch string) (bool, error) {
	n, err := r.q.InsertIssueBranchLink(ctx, sqlcdb.InsertIssueBranchLinkParams{IssueID: id(issueID), RepositoryID: id(repoID), Branch: &branch})
	if err != nil {
		return false, fmt.Errorf("InsertIssueBranchLink: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) LinkPull(ctx context.Context, issueID, repoID, pullID string) (bool, error) {
	n, err := r.q.InsertIssuePullLink(ctx, sqlcdb.InsertIssuePullLinkParams{IssueID: id(issueID), RepositoryID: id(repoID), PullRequestID: id(pullID)})
	if err != nil {
		return false, fmt.Errorf("InsertIssuePullLink: %w", err)
	}
	return n == 1, nil
}

// Move locks the issue's row, so an event and a person (or two events)
// never both move it from the same status.
func (r *postgresRepository) Move(ctx context.Context, m Move) (bool, error) {
	moved := false
	err := r.inTx(ctx, func(q *sqlcdb.Queries) error {
		status, err := q.LockIssueStatus(ctx, id(m.Event.IssueID))
		if err != nil {
			return fmt.Errorf("LockIssueStatus: %w", notFound(err))
		}
		if !slices.Contains(m.From, status) {
			return nil
		}
		e := m.Event
		e.Kind, e.From, e.To = "status", status, m.To
		wrote, err := insertEvent(ctx, q, e)
		if err != nil || !wrote {
			return err
		}
		n, err := q.MoveIssue(ctx, sqlcdb.MoveIssueParams{ID: id(e.IssueID), Status: m.To, FromStatuses: m.From})
		if err != nil {
			return fmt.Errorf("MoveIssue: %w", err)
		}
		moved = n == 1
		return nil
	})
	return moved, err
}

func (r *postgresRepository) Record(ctx context.Context, e Event) (bool, error) {
	return insertEvent(ctx, r.q, e)
}

func (r *postgresRepository) OtherOpenPulls(ctx context.Context, issueID, pullID string) (int, error) {
	n, err := r.q.CountOtherOpenLinkedPulls(ctx, sqlcdb.CountOtherOpenLinkedPullsParams{IssueID: id(issueID), PullRequestID: id(pullID)})
	if err != nil {
		return 0, fmt.Errorf("CountOtherOpenLinkedPulls: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) ShipCandidates(ctx context.Context, repoID string) ([]ShipCandidate, error) {
	rows, err := r.q.ListShipCandidates(ctx, id(repoID))
	if err != nil {
		return nil, fmt.Errorf("ListShipCandidates: %w", err)
	}
	out := make([]ShipCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, ShipCandidate{
			IssueID: str(row.IssueID), Status: row.Status, PullID: str(row.PullID), RepositoryID: str(row.RepositoryID),
			Base: row.BaseBranch, PullNumber: int(row.PullNumber), MergeSHA: deref(row.MergeSha), MergedBy: str(row.MergedBy),
			MergedAt: row.MergedAt.Time, HasOpen: row.HasOpen,
		})
	}
	return out, nil
}

func (r *postgresRepository) ServiceRepository(ctx context.Context, serviceID string) (string, error) {
	repo, err := r.q.ServiceRepository(ctx, id(serviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ServiceRepository: %w", err)
	}
	return str(repo), nil
}

func ref(key string, number int32, digits int32, title, status string) code.IssueRef {
	return code.IssueRef{Key: KeyOf(key, int(number), int(digits)), Number: int(number), Title: title, Status: status}
}

func (r *postgresRepository) PullIssues(ctx context.Context, pullIDs []string) (map[string][]code.IssueRef, error) {
	rows, err := r.q.ListPullIssues(ctx, ids(pullIDs))
	if err != nil {
		return nil, fmt.Errorf("ListPullIssues: %w", err)
	}
	out := map[string][]code.IssueRef{}
	for _, row := range rows {
		k := str(row.PullRequestID)
		out[k] = append(out[k], ref(row.IssueKey, row.Number, row.Digits, row.Title, row.Status))
	}
	return out, nil
}

func (r *postgresRepository) BranchIssues(ctx context.Context, repoID string, branches []string) (map[string][]code.IssueRef, error) {
	rows, err := r.q.ListBranchIssues(ctx, sqlcdb.ListBranchIssuesParams{RepositoryID: id(repoID), Branches: branches})
	if err != nil {
		return nil, fmt.Errorf("ListBranchIssues: %w", err)
	}
	out := map[string][]code.IssueRef{}
	for _, row := range rows {
		b := deref(row.Branch)
		out[b] = append(out[b], ref(row.IssueKey, row.Number, row.Digits, row.Title, row.Status))
	}
	return out, nil
}

func (r *postgresRepository) SearchIssues(ctx context.Context, sq SearchQuery) ([]SearchIssue, error) {
	rows, err := r.q.SearchIssues(ctx, sqlcdb.SearchIssuesParams{
		OrgID: id(sq.OrgID), ProjectID: id(sq.ProjectID), HasQ: sq.HasQ, Tsq: sq.TSQ,
		QNumber: int32(sq.QNumber), QKey: sq.QKey, QPrefix: sq.QPrefix, RowLimit: int32(sq.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("SearchIssues: %w", err)
	}
	out := make([]SearchIssue, 0, len(rows))
	for _, row := range rows {
		out = append(out, SearchIssue{ProjectID: str(row.ProjectID), Project: row.ProjectName, Key: KeyOf(row.IssueKey, int(row.Number), int(row.Digits)),
			Number: int(row.Number), Title: row.Title, Status: row.Status, Priority: row.Priority, typeID: str(row.TypeID)})
	}
	return out, nil
}

func (r *postgresRepository) SearchPulls(ctx context.Context, sq SearchQuery) ([]SearchPull, error) {
	rows, err := r.q.SearchPullRequests(ctx, sqlcdb.SearchPullRequestsParams{
		OrgID: id(sq.OrgID), ProjectID: id(sq.ProjectID), HasQ: sq.HasQ, Tsq: sq.TSQ,
		QNumber: int32(sq.QNumber), RowLimit: int32(sq.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("SearchPullRequests: %w", err)
	}
	out := make([]SearchPull, 0, len(rows))
	for _, row := range rows {
		out = append(out, SearchPull{ProjectID: str(row.ProjectID), Project: row.ProjectName, Repository: row.RepositoryName,
			Number: int(row.Number), Title: row.Title, State: row.State})
	}
	return out, nil
}

// ─── types, keys and settings (D-83) ─────────────────────────────────────────

func (r *postgresRepository) Types(ctx context.Context, orgID string) ([]Type, error) {
	rows, err := r.q.ListIssueTypes(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListIssueTypes: %w", err)
	}
	out := make([]Type, 0, len(rows))
	for _, row := range rows {
		out = append(out, Type{TypeRef: TypeRef{ID: str(row.ID), Name: row.Name, Kind: row.Kind, Icon: row.Icon, Color: row.Color},
			Position: int(row.Position), Archived: row.ArchivedAt.Valid})
	}
	return out, nil
}

func (r *postgresRepository) AddType(ctx context.Context, orgID string, t TypeRef, position int) (string, error) {
	tid, err := r.q.InsertIssueType(ctx, sqlcdb.InsertIssueTypeParams{OrgID: id(orgID), Name: t.Name, Kind: t.Kind, Icon: t.Icon, Color: t.Color, Position: int32(position)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTypeTaken
	}
	if err != nil {
		return "", fmt.Errorf("InsertIssueType: %w", err)
	}
	return str(tid), nil
}

func (r *postgresRepository) UpdateType(ctx context.Context, orgID string, t Type) (bool, error) {
	n, err := r.q.UpdateIssueType(ctx, sqlcdb.UpdateIssueTypeParams{ID: id(t.ID), OrgID: id(orgID), Name: t.Name, Icon: t.Icon, Color: t.Color, Position: int32(t.Position)})
	if isUnique(err) {
		return false, ErrTypeTaken
	}
	if err != nil {
		return false, fmt.Errorf("UpdateIssueType: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) SetTypeArchived(ctx context.Context, orgID, typeID string, archived bool) (bool, error) {
	n, err := r.q.SetIssueTypeArchived(ctx, sqlcdb.SetIssueTypeArchivedParams{ID: id(typeID), OrgID: id(orgID), Archived: archived})
	if err != nil {
		return false, fmt.Errorf("SetIssueTypeArchived: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) OrgDigits(ctx context.Context, orgID string) (int, error) {
	d, err := r.q.GetOrgIssueDigits(ctx, id(orgID))
	if err != nil {
		return 0, fmt.Errorf("GetOrgIssueDigits: %w", notFound(err))
	}
	return int(d), nil
}

func (r *postgresRepository) SetOrgDigits(ctx context.Context, orgID string, digits int) error {
	if err := r.q.SetOrgIssueDigits(ctx, sqlcdb.SetOrgIssueDigitsParams{ID: id(orgID), Digits: int16(digits)}); err != nil {
		return fmt.Errorf("SetOrgIssueDigits: %w", err)
	}
	return nil
}

func (r *postgresRepository) Aliases(ctx context.Context, projectID string) ([]string, error) {
	keys, err := r.q.ListProjectKeyAliases(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListProjectKeyAliases: %w", err)
	}
	return keys, nil
}

func (r *postgresRepository) KeyHolder(ctx context.Context, orgID, key string) (string, error) {
	pid, err := r.q.IssueKeyHolder(ctx, sqlcdb.IssueKeyHolderParams{OrgID: id(orgID), Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("IssueKeyHolder: %w", err)
	}
	return str(pid), nil
}

// ReplaceKey keeps the old key as an alias and drops the new one from the
// project's aliases (a key renamed back), in one transaction.
func (r *postgresRepository) ReplaceKey(ctx context.Context, orgID, projectID, old, key string) error {
	return r.inTx(ctx, func(q *sqlcdb.Queries) error {
		if old != "" {
			if err := q.AddIssueKeyAlias(ctx, sqlcdb.AddIssueKeyAliasParams{OrgID: id(orgID), Key: old, ProjectID: id(projectID)}); err != nil {
				return fmt.Errorf("AddIssueKeyAlias: %w", err)
			}
		}
		if err := q.DeleteIssueKeyAlias(ctx, sqlcdb.DeleteIssueKeyAliasParams{OrgID: id(orgID), Key: key, ProjectID: id(projectID)}); err != nil {
			return fmt.Errorf("DeleteIssueKeyAlias: %w", err)
		}
		if err := q.ReplaceProjectIssueKey(ctx, sqlcdb.ReplaceProjectIssueKeyParams{ID: id(projectID), IssueKey: &key}); err != nil {
			if isUnique(err) {
				return ErrKeyTaken
			}
			return fmt.Errorf("ReplaceProjectIssueKey: %w", err)
		}
		return nil
	})
}

func (r *postgresRepository) HiddenTypes(ctx context.Context, projectID string) ([]string, error) {
	rows, err := r.q.ListHiddenIssueTypes(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListHiddenIssueTypes: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, t := range rows {
		out = append(out, str(t))
	}
	return out, nil
}

func (r *postgresRepository) SaveSettings(ctx context.Context, projectID string, digits *int, defaultType, version string, hidden []string) error {
	return r.inTx(ctx, func(q *sqlcdb.Queries) error {
		if version != "" {
			if _, err := q.LockProjectForReplace(ctx, id(projectID)); err != nil {
				return fmt.Errorf("LockProjectForReplace: %w", err)
			}
			stored, err := q.ListHiddenIssueTypesForReplace(ctx, id(projectID))
			if err != nil {
				return fmt.Errorf("ListHiddenIssueTypesForReplace: %w", err)
			}
			current := make([]string, 0, len(stored))
			for _, t := range stored {
				current = append(current, str(t))
			}
			if listversion.Of(current) != version {
				return listversion.ErrChanged
			}
		}
		var d *int16
		if digits != nil {
			v := int16(*digits)
			d = &v
		}
		if err := q.SetProjectIssueSettings(ctx, sqlcdb.SetProjectIssueSettingsParams{ID: id(projectID), Digits: d, DefaultTypeID: id(defaultType)}); err != nil {
			return fmt.Errorf("SetProjectIssueSettings: %w", err)
		}
		if version == "" {
			return nil
		}
		if err := q.DeleteHiddenIssueTypes(ctx, id(projectID)); err != nil {
			return fmt.Errorf("DeleteHiddenIssueTypes: %w", err)
		}
		if len(hidden) > 0 {
			if err := q.InsertHiddenIssueTypes(ctx, sqlcdb.InsertHiddenIssueTypesParams{ProjectID: id(projectID), TypeIds: ids(hidden)}); err != nil {
				return fmt.Errorf("InsertHiddenIssueTypes: %w", err)
			}
		}
		return nil
	})
}

// ─── parents, children and relations (D-83) ─────────────────────────────────

func (r *postgresRepository) IssueRef(ctx context.Context, issueID string) (*RefRow, error) {
	row, err := r.q.IssueRefByID(ctx, id(issueID))
	if err != nil {
		return nil, fmt.Errorf("IssueRefByID: %w", notFound(err))
	}
	return &RefRow{ID: str(row.ID), ProjectID: str(row.ProjectID), OrgID: str(row.OrgID), Key: row.IssueKey, Number: int(row.Number),
		Digits: int(row.Digits), Title: row.Title, Status: row.Status, TypeID: str(row.TypeID), ParentID: str(row.ParentID),
		CycleID: str(row.CycleID)}, nil
}

func (r *postgresRepository) IssueByKey(ctx context.Context, orgID, key string, number int) (string, error) {
	iid, err := r.q.IssueByKey(ctx, sqlcdb.IssueByKeyParams{OrgID: id(orgID), Key: &key, Number: int32(number)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrIssueNotFound
	}
	if err != nil {
		return "", fmt.Errorf("IssueByKey: %w", err)
	}
	return str(iid), nil
}

func (r *postgresRepository) Children(ctx context.Context, issueID string) ([]RefRow, error) {
	rows, err := r.q.ListIssueChildren(ctx, id(issueID))
	if err != nil {
		return nil, fmt.Errorf("ListIssueChildren: %w", err)
	}
	out := make([]RefRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, RefRow{ID: str(row.ID), ProjectID: str(row.ProjectID), Key: row.IssueKey, Number: int(row.Number), Digits: int(row.Digits),
			Title: row.Title, Status: row.Status, TypeID: str(row.TypeID), AssigneeID: str(row.AssigneeID), AssigneeName: row.AssigneeName,
			Estimate: estInt(row.Estimate), CycleNumber: int(row.CycleNumber)})
	}
	return out, nil
}

func (r *postgresRepository) ChildCounts(ctx context.Context, issueID string) (Progress, error) {
	row, err := r.q.ChildCounts(ctx, id(issueID))
	if err != nil {
		return Progress{}, fmt.Errorf("ChildCounts: %w", err)
	}
	return Progress{Total: int(row.Total), Shipped: int(row.Shipped), Done: int(row.Done)}, nil
}

func (r *postgresRepository) Relations(ctx context.Context, issueID string) ([]RelationRow, error) {
	rows, err := r.q.ListIssueRelations(ctx, id(issueID))
	if err != nil {
		return nil, fmt.Errorf("ListIssueRelations: %w", err)
	}
	out := make([]RelationRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, RelationRow{ID: str(row.ID), Kind: row.Kind, Outward: row.Outward, Other: RefRow{ID: str(row.OtherID), ProjectID: str(row.OtherProjectID),
			Key: row.IssueKey, Number: int(row.Number), Digits: int(row.Digits), Title: row.Title, Status: row.Status, TypeID: str(row.TypeID)}})
	}
	return out, nil
}

func (r *postgresRepository) AddRelation(ctx context.Context, from, to, kind, actorID string) (bool, error) {
	n, err := r.q.InsertIssueRelation(ctx, sqlcdb.InsertIssueRelationParams{FromIssueID: id(from), ToIssueID: id(to), Kind: kind, CreatedBy: id(actorID)})
	if err != nil {
		return false, fmt.Errorf("InsertIssueRelation: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) GetRelation(ctx context.Context, issueID, relationID string) (*RelationRow, error) {
	row, err := r.q.GetIssueRelation(ctx, sqlcdb.GetIssueRelationParams{ID: id(relationID), SelfID: id(issueID)})
	if err != nil {
		return nil, notFound(err)
	}
	other := str(row.ToIssueID)
	if other == issueID {
		other = str(row.FromIssueID)
	}
	return &RelationRow{ID: str(row.ID), Kind: row.Kind, Outward: str(row.FromIssueID) == issueID, Other: RefRow{ID: other}}, nil
}

func (r *postgresRepository) DeleteRelation(ctx context.Context, relationID string) error {
	if err := r.q.DeleteIssueRelation(ctx, id(relationID)); err != nil {
		return fmt.Errorf("DeleteIssueRelation: %w", err)
	}
	return nil
}

func (r *postgresRepository) BlockedIssues(ctx context.Context, blockerID string) ([]string, error) {
	rows, err := r.q.ListBlockedIssues(ctx, id(blockerID))
	if err != nil {
		return nil, fmt.Errorf("ListBlockedIssues: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, b := range rows {
		out = append(out, str(b))
	}
	return out, nil
}

func (r *postgresRepository) CopyLabels(ctx context.Context, src, dst string) error {
	if err := r.q.CopyIssueLabels(ctx, sqlcdb.CopyIssueLabelsParams{SrcID: id(src), DstID: id(dst)}); err != nil {
		return fmt.Errorf("CopyIssueLabels: %w", err)
	}
	return nil
}

func (r *postgresRepository) CopyRelations(ctx context.Context, src, dst, actorID string) error {
	if err := r.q.CopyIssueRelations(ctx, sqlcdb.CopyIssueRelationsParams{SrcID: id(src), DstID: id(dst), CreatedBy: id(actorID)}); err != nil {
		return fmt.Errorf("CopyIssueRelations: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetFailedDeployIssues(ctx context.Context, projectID string, on bool) error {
	if err := r.q.SetProjectFailedDeployIssues(ctx, sqlcdb.SetProjectFailedDeployIssuesParams{ID: id(projectID), FailedDeployIssues: on}); err != nil {
		return fmt.Errorf("SetProjectFailedDeployIssues: %w", err)
	}
	return nil
}

func (r *postgresRepository) FailedDeployment(ctx context.Context, deploymentID string) (*FailedDeployment, error) {
	if !id(deploymentID).Valid {
		return nil, ErrNotFound
	}
	row, err := r.q.GetDeploymentForIssue(ctx, id(deploymentID))
	if err != nil {
		return nil, fmt.Errorf("GetDeploymentForIssue: %w", notFound(err))
	}
	return &FailedDeployment{ID: str(row.ID), Status: row.Status, Error: row.ErrorSummary, CommitSHA: row.CommitSha, CommitMessage: row.CommitMessage,
		At: row.At.Time, ServiceID: str(row.ServiceID), ServiceName: row.ServiceName, Environment: row.EnvironmentName,
		ProjectID: str(row.ProjectID), OrgID: str(row.OrgID), FailedDeployIssues: row.FailedDeployIssues}, nil
}

func (r *postgresRepository) OpenFailureIssues(ctx context.Context, serviceID string) ([]FailureIssue, error) {
	rows, err := r.q.OpenFailureIssues(ctx, id(serviceID))
	if err != nil {
		return nil, fmt.Errorf("OpenFailureIssues: %w", err)
	}
	out := make([]FailureIssue, 0, len(rows))
	for _, row := range rows {
		out = append(out, FailureIssue{ID: str(row.ID), Number: int(row.Number)})
	}
	return out, nil
}

func (r *postgresRepository) SetFromDeployment(ctx context.Context, issueID, deploymentID string) error {
	if err := r.q.SetIssueFromDeployment(ctx, sqlcdb.SetIssueFromDeploymentParams{ID: id(issueID), FromDeploymentID: id(deploymentID)}); err != nil {
		return fmt.Errorf("SetIssueFromDeployment: %w", err)
	}
	return nil
}

func (r *postgresRepository) LiveDeployment(ctx context.Context, serviceID string) (string, error) {
	d, err := r.q.ServiceLiveDeployment(ctx, id(serviceID))
	if err != nil {
		return "", fmt.Errorf("ServiceLiveDeployment: %w", notFound(err))
	}
	return d, nil
}

func (r *postgresRepository) MyIssues(ctx context.Context, orgID, userID string) ([]MyIssue, error) {
	rows, err := r.q.MyIssues(ctx, sqlcdb.MyIssuesParams{OrgID: id(orgID), UserID: id(userID)})
	if err != nil {
		return nil, fmt.Errorf("MyIssues: %w", err)
	}
	out := make([]MyIssue, 0, len(rows))
	for _, row := range rows {
		out = append(out, MyIssue{Key: KeyOf(row.IssueKey, int(row.Number), int(row.Digits)), Number: int(row.Number), Title: row.Title,
			Status: row.Status, Priority: row.Priority, ProjectID: str(row.ProjectID), Project: row.ProjectName, UpdatedAt: row.UpdatedAt.Time,
			typeID: str(row.TypeID), DueOn: optDate(fromDate(row.DueOn))})
		if row.CycleNumber > 0 {
			n := int(row.CycleNumber)
			out[len(out)-1].Cycle = &n
		}
	}
	return out, nil
}
