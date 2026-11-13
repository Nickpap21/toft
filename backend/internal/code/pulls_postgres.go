package code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Repositories and pull requests in Postgres (D-75).

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func optUUID(s string) pgtype.UUID {
	u, err := pgUUID(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (r *postgresRepository) CreateRepo(ctx context.Context, orgID, projectID, name string) (*StoredRepo, error) {
	row, err := r.q.CreateRepository(ctx, sqlcdb.CreateRepositoryParams{OrgID: optUUID(orgID), ProjectID: optUUID(projectID), Name: name})
	if isUnique(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("CreateRepository: %w", err)
	}
	return &StoredRepo{ID: uuidStr(row.ID), Name: name, CreatedAt: row.CreatedAt.Time}, nil
}

func (r *postgresRepository) DeleteRepo(ctx context.Context, projectID, id string) (bool, error) {
	n, err := r.q.DeleteRepository(ctx, sqlcdb.DeleteRepositoryParams{ID: optUUID(id), ProjectID: optUUID(projectID)})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return false, ErrRepoInUse // a service builds from it (fk_services_repository)
	}
	if err != nil {
		return false, fmt.Errorf("DeleteRepository: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) SetProtection(ctx context.Context, id string, on bool, branch string) error {
	if err := r.q.SetRepositoryProtection(ctx, sqlcdb.SetRepositoryProtectionParams{ID: optUUID(id), Protect: on, Branch: optText(branch)}); err != nil {
		return fmt.Errorf("SetRepositoryProtection: %w", err)
	}
	return nil
}

func (r *postgresRepository) FillProtectedBranch(ctx context.Context, id, branch string) error {
	if err := r.q.FillProtectedBranch(ctx, sqlcdb.FillProtectedBranchParams{ID: optUUID(id), Branch: &branch}); err != nil {
		return fmt.Errorf("FillProtectedBranch: %w", err)
	}
	return nil
}

func (r *postgresRepository) OpenPull(ctx context.Context, p NewPull) (*StoredPull, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.q.WithTx(tx)
	repoID := optUUID(p.RepoID)
	n, err := q.NextPullNumber(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("NextPullNumber: %w", err)
	}
	_, err = q.InsertPullRequest(ctx, sqlcdb.InsertPullRequestParams{
		RepositoryID: repoID, Number: n, Title: p.Title, Body: p.Body, AuthorID: optUUID(p.AuthorID),
		BaseBranch: p.Base, HeadBranch: p.Head, HeadSha: p.HeadSHA, OpenedWithKey: p.WithKey, ApiKeyID: optUUID(p.APIKeyID),
		AgentConnectionID: optUUID(p.AgentID),
	})
	if isUnique(err) {
		return nil, ErrPullExists
	}
	if err != nil {
		return nil, fmt.Errorf("InsertPullRequest: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return r.GetPull(ctx, p.RepoID, int(n))
}

func toPull(row sqlcdb.GetPullRequestRow) *StoredPull {
	return &StoredPull{
		ID: uuidStr(row.ID), RepoID: uuidStr(row.RepositoryID), Number: int(row.Number), Title: row.Title, Body: row.Body,
		AuthorID: uuidStr(row.AuthorID), AuthorName: row.AuthorName, AuthorEmail: row.AuthorEmail,
		Base: row.BaseBranch, Head: row.HeadBranch, HeadSHA: row.HeadSha, State: row.State,
		MergeMethod: str(row.MergeMethod), MergeSHA: str(row.MergeSha), MergeBaseSHA: str(row.MergeBaseSha),
		MergedByID: uuidStr(row.MergedBy), MergedByName: row.MergedByName,
		MergedAt: timePtr(row.MergedAt), ClosedAt: timePtr(row.ClosedAt),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, Comments: int(row.Comments), AgentName: row.AgentName,
	}
}

func (r *postgresRepository) GetPull(ctx context.Context, repoID string, number int) (*StoredPull, error) {
	row, err := r.q.GetPullRequest(ctx, sqlcdb.GetPullRequestParams{RepositoryID: optUUID(repoID), Number: int32(number)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetPullRequest: %w", err)
	}
	return toPull(row), nil
}

func (r *postgresRepository) ListPulls(ctx context.Context, repoID, state string, before, limit int) ([]StoredPull, error) {
	var b *int32
	if before > 0 {
		v := int32(before)
		b = &v
	}
	rows, err := r.q.ListPullRequests(ctx, sqlcdb.ListPullRequestsParams{RepositoryID: optUUID(repoID), State: state, Before: b, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("ListPullRequests: %w", err)
	}
	out := make([]StoredPull, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toPull(sqlcdb.GetPullRequestRow(row)))
	}
	return out, nil
}

func (r *postgresRepository) CountPulls(ctx context.Context, repoID string) (map[string]int, error) {
	rows, err := r.q.CountPullRequests(ctx, optUUID(repoID))
	if err != nil {
		return nil, fmt.Errorf("CountPullRequests: %w", err)
	}
	out := map[string]int{"open": 0, "merged": 0, "closed": 0}
	for _, row := range rows {
		out[row.State] = int(row.N)
	}
	return out, nil
}

func (r *postgresRepository) OpenPullsByHead(ctx context.Context, repoID string, branches []string) ([]StoredPull, error) {
	rows, err := r.q.ListOpenPullRequestsByHead(ctx, sqlcdb.ListOpenPullRequestsByHeadParams{RepositoryID: optUUID(repoID), Branches: branches})
	if err != nil {
		return nil, fmt.Errorf("ListOpenPullRequestsByHead: %w", err)
	}
	out := make([]StoredPull, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredPull{ID: uuidStr(row.ID), RepoID: repoID, Number: int(row.Number), Title: row.Title, Base: row.BaseBranch,
			Head: row.HeadBranch, HeadSHA: row.HeadSha, State: "open"})
	}
	return out, nil
}

func (r *postgresRepository) UpdatePullText(ctx context.Context, id, title, body string) error {
	if err := r.q.UpdatePullRequestText(ctx, sqlcdb.UpdatePullRequestTextParams{ID: optUUID(id), Title: title, Body: body}); err != nil {
		return fmt.Errorf("UpdatePullRequestText: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetPullState(ctx context.Context, id, from, to string) (bool, error) {
	n, err := r.q.SetPullRequestState(ctx, sqlcdb.SetPullRequestStateParams{ID: optUUID(id), State: to, FromState: from})
	if isUnique(err) {
		// Reopening while another open one has the same branches.
		return false, ErrPullExists
	}
	if err != nil {
		return false, fmt.Errorf("SetPullRequestState: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) MarkMerged(ctx context.Context, id string, m Merged) (bool, error) {
	n, err := r.q.MarkPullRequestMerged(ctx, sqlcdb.MarkPullRequestMergedParams{
		ID: optUUID(id), MergeMethod: &m.Method, MergeSha: &m.SHA, MergeBaseSha: &m.MergeBase, HeadSha: m.Head, MergedBy: optUUID(m.By),
	})
	if err != nil {
		return false, fmt.Errorf("MarkPullRequestMerged: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) SetPullHead(ctx context.Context, id, sha string) error {
	if err := r.q.SetPullRequestHead(ctx, sqlcdb.SetPullRequestHeadParams{ID: optUUID(id), HeadSha: sha}); err != nil {
		return fmt.Errorf("SetPullRequestHead: %w", err)
	}
	return nil
}

func (r *postgresRepository) AddComment(ctx context.Context, pullID, authorID, body string) (*StoredComment, error) {
	var agentID string
	if a, ok := actor.AgentFrom(ctx); ok {
		agentID = a.ConnectionID
	}
	row, err := r.q.InsertPullRequestComment(ctx, sqlcdb.InsertPullRequestCommentParams{PullRequestID: optUUID(pullID), AuthorID: optUUID(authorID), Body: body,
		AgentConnectionID: optUUID(agentID)})
	if err != nil {
		return nil, fmt.Errorf("InsertPullRequestComment: %w", err)
	}
	return r.GetComment(ctx, pullID, uuidStr(row.ID))
}

func (r *postgresRepository) GetComment(ctx context.Context, pullID, id string) (*StoredComment, error) {
	cid, err := pgUUID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	row, err := r.q.GetPullRequestComment(ctx, sqlcdb.GetPullRequestCommentParams{ID: cid, PullRequestID: optUUID(pullID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetPullRequestComment: %w", err)
	}
	return &StoredComment{ID: uuidStr(row.ID), AuthorID: uuidStr(row.AuthorID), AuthorName: row.AuthorName, AgentName: row.AgentName, Body: row.Body,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (r *postgresRepository) UpdateComment(ctx context.Context, id, body string) error {
	if err := r.q.UpdatePullRequestComment(ctx, sqlcdb.UpdatePullRequestCommentParams{ID: optUUID(id), Body: body}); err != nil {
		return fmt.Errorf("UpdatePullRequestComment: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeleteComment(ctx context.Context, id string) error {
	if err := r.q.DeletePullRequestComment(ctx, optUUID(id)); err != nil {
		return fmt.Errorf("DeletePullRequestComment: %w", err)
	}
	return nil
}

func (r *postgresRepository) AddEvent(ctx context.Context, pullID, actorID, kind string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var actor pgtype.UUID
	if actorID != "" {
		actor = optUUID(actorID)
	}
	if err := r.q.InsertPullRequestEvent(ctx, sqlcdb.InsertPullRequestEventParams{PullRequestID: optUUID(pullID), ActorID: actor, Kind: kind, Data: b}); err != nil {
		return fmt.Errorf("InsertPullRequestEvent: %w", err)
	}
	return nil
}

func (r *postgresRepository) Timeline(ctx context.Context, pullID string, afterAt time.Time, afterID string, limit int) ([]StoredItem, error) {
	p := sqlcdb.ListPullRequestTimelineParams{PullRequestID: optUUID(pullID), RowLimit: int32(limit)}
	if afterID != "" {
		p.AfterAt = pgtype.Timestamptz{Time: afterAt, Valid: true}
		p.AfterID = optUUID(afterID)
	}
	rows, err := r.q.ListPullRequestTimeline(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("ListPullRequestTimeline: %w", err)
	}
	out := make([]StoredItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredItem{Item: row.Item, ID: uuidStr(row.ID), ActorID: uuidStr(row.ActorID), ActorName: row.ActorName,
			AgentName: row.AgentName, Body: row.Body, Kind: row.Kind, Data: row.Data, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time})
	}
	return out, nil
}
