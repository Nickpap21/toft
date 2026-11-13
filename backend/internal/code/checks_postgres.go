package code

import (
	"context"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// The events outbox and commit checks in Postgres (D-77).

func optText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *postgresRepository) InsertEvent(ctx context.Context, e GitEvent) (int64, error) {
	id, err := r.q.InsertGitEvent(ctx, sqlcdb.InsertGitEventParams{
		RepositoryID: optUUID(e.RepoID), Kind: e.Kind, Action: optText(e.Action), Branch: e.Branch, BeforeSha: e.Before, AfterSha: e.After,
		ActorID: optUUID(e.ActorID), PullRequestID: optUUID(e.PullID), Subject: truncateRunes(e.Subject, 300),
		Commits: int32(e.Commits), Forced: e.Forced, WithKey: e.WithKey, Deploys: e.Deploys, HookEvents: e.hookEvents(), PushMirrors: e.Kind != "pull_request",
		AgentConnectionID: optUUID(e.AgentID),
	})
	if err != nil {
		return 0, fmt.Errorf("InsertGitEvent: %w", err)
	}
	return id, nil
}

func (r *postgresRepository) ClaimEvents(ctx context.Context, n int) ([]ClaimedEvent, error) {
	rows, err := r.q.ClaimGitEvents(ctx, int32(n))
	if err != nil {
		return nil, fmt.Errorf("ClaimGitEvents: %w", err)
	}
	out := make([]ClaimedEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, ClaimedEvent{ID: row.ID, RepoID: uuidStr(row.RepositoryID), ProjectID: uuidStr(row.ProjectID),
			OrgID: uuidStr(row.OrgID), Kind: row.Kind, Branch: row.Branch, After: row.AfterSha, Subject: row.Subject,
			ActorID: uuidStr(row.ActorID), ActorName: row.ActorName, PullNumber: int(row.PullNumber),
			Attempts: int(row.Attempts), CreatedAt: row.CreatedAt.Time})
	}
	return out, nil
}

func (r *postgresRepository) FinishEvent(ctx context.Context, id int64, msg string) error {
	if err := r.q.FinishGitEvent(ctx, sqlcdb.FinishGitEventParams{ID: id, LastError: optText(truncateRunes(msg, 1000))}); err != nil {
		return fmt.Errorf("FinishGitEvent: %w", err)
	}
	return nil
}

func (r *postgresRepository) RetryEvent(ctx context.Context, id int64, at time.Time, msg string) error {
	m := truncateRunes(msg, 1000)
	if err := r.q.RetryGitEvent(ctx, sqlcdb.RetryGitEventParams{ID: id, At: pgtype.Timestamptz{Time: at, Valid: true}, LastError: &m}); err != nil {
		return fmt.Errorf("RetryGitEvent: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeleteOldEvents(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q.DeleteOldGitEvents(ctx, pgtype.Timestamptz{Time: before, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("DeleteOldGitEvents: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) ListEvents(ctx context.Context, repoID, branch string, beforeID int64, n int) ([]Pipeline, error) {
	rows, err := r.q.ListGitEvents(ctx, sqlcdb.ListGitEventsParams{RepositoryID: optUUID(repoID), Branch: optText(branch), BeforeID: beforeID, N: int32(n)})
	if err != nil {
		return nil, fmt.Errorf("ListGitEvents: %w", err)
	}
	out := make([]Pipeline, 0, len(rows))
	for _, row := range rows {
		p := Pipeline{ID: row.ID, Kind: row.Kind, Branch: row.Branch, Before: row.BeforeSha, After: row.AfterSha,
			Subject: row.Subject, Commits: int(row.Commits), Forced: row.Forced, Actor: person(uuidStr(row.ActorID), row.ActorName),
			CreatedAt: row.CreatedAt.Time, DispatchedAt: timePtr(row.DispatchedAt), Error: row.LastError}
		if row.PullNumber != nil {
			n := int(*row.PullNumber)
			p.PullNumber = &n
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *postgresRepository) UpsertCheck(ctx context.Context, c NewCheck) error {
	if _, err := r.q.UpsertCommitCheck(ctx, sqlcdb.UpsertCommitCheckParams{
		RepositoryID: optUUID(c.RepoID), Sha: c.SHA, Name: c.Name, State: c.State, Description: c.Description,
		DetailsUrl: optText(c.DetailsURL), DeploymentID: optUUID(c.DeploymentID), ReporterID: optUUID(c.ReporterID),
	}); err != nil {
		return fmt.Errorf("UpsertCommitCheck: %w", err)
	}
	return nil
}

func (r *postgresRepository) CheckExists(ctx context.Context, repoID, sha, name string) (bool, error) {
	ok, err := r.q.CommitCheckExists(ctx, sqlcdb.CommitCheckExistsParams{RepositoryID: optUUID(repoID), Sha: sha, Name: name})
	if err != nil {
		return false, fmt.Errorf("CommitCheckExists: %w", err)
	}
	return ok, nil
}

func (r *postgresRepository) CountChecks(ctx context.Context, repoID, sha string) (int, error) {
	n, err := r.q.CountCommitChecks(ctx, sqlcdb.CountCommitChecksParams{RepositoryID: optUUID(repoID), Sha: sha})
	if err != nil {
		return 0, fmt.Errorf("CountCommitChecks: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) ListChecks(ctx context.Context, repoID string, shas []string) ([]StoredCheck, error) {
	rows, err := r.q.ListCommitChecks(ctx, sqlcdb.ListCommitChecksParams{RepositoryID: optUUID(repoID), Shas: shas})
	if err != nil {
		return nil, fmt.Errorf("ListCommitChecks: %w", err)
	}
	out := make([]StoredCheck, 0, len(rows))
	for _, row := range rows {
		c := CommitCheck{Name: row.Name, State: row.State, Description: row.Description, DetailsURL: row.DetailsUrl,
			Reporter: person(uuidStr(row.ReporterID), row.ReporterName), StartedAt: timePtr(row.StartedAt),
			CompletedAt: timePtr(row.CompletedAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
		if row.DeploymentID.Valid {
			c.Deployment = &CheckDeployment{ID: uuidStr(row.DeploymentID), ServiceID: uuidStr(row.DeploymentServiceID)}
		}
		out = append(out, StoredCheck{SHA: row.Sha, CommitCheck: c})
	}
	return out, nil
}

func (r *postgresRepository) FailDeployChecks(ctx context.Context, deploymentIDs []string, description string) error {
	ids := make([]pgtype.UUID, 0, len(deploymentIDs))
	for _, id := range deploymentIDs {
		ids = append(ids, optUUID(id))
	}
	if err := r.q.FailRunningDeployChecks(ctx, sqlcdb.FailRunningDeployChecksParams{DeploymentIds: ids, Description: truncateRunes(description, 300)}); err != nil {
		return fmt.Errorf("FailRunningDeployChecks: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeployTargets(ctx context.Context, repoID string) ([]DeployTarget, error) {
	rows, err := r.q.ListDeployTargets(ctx, optUUID(repoID))
	if err != nil {
		return nil, fmt.Errorf("ListDeployTargets: %w", err)
	}
	out := make([]DeployTarget, 0, len(rows))
	for _, row := range rows {
		out = append(out, DeployTarget{ServiceID: uuidStr(row.ID), ServiceName: row.Name, EnvironmentName: row.EnvironmentName,
			EnvironmentColor: row.EnvironmentColor, ServerName: row.ServerName, Branch: row.GitBranch,
			BaseDirectory: row.BaseDirectory, DeployOnPush: row.DeployOnPush})
	}
	return out, nil
}

// truncateRunes is s cut to n characters.
func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
