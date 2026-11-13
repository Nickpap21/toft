package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository reads Home's data (db/queries/dashboard.sql).
type PostgresRepository struct {
	q *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{q: sqlcdb.New(pool)}
}

func uuidString(p pgtype.UUID) string {
	if !p.Valid {
		return ""
	}
	return uuid.UUID(p.Bytes).String()
}

// Load runs Home's queries for the workspace orgID.
func (r *PostgresRepository) Load(ctx context.Context, orgID, userID string) (*DashboardData, error) {
	parsed, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace id %q: %w", orgID, err)
	}
	org := pgtype.UUID{Bytes: parsed, Valid: true}

	stats, err := r.q.DashboardDeploys24h(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("DashboardDeploys24h: %w", err)
	}
	failed, err := r.q.ListDashboardFailedDeploys(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListDashboardFailedDeploys: %w", err)
	}
	alerts, err := r.q.ListDashboardOpenAlerts(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListDashboardOpenAlerts: %w", err)
	}
	live, err := r.q.ListDashboardDeploying(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListDashboardDeploying: %w", err)
	}
	projects, err := r.q.ListDashboardProjectDeploys(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListDashboardProjectDeploys: %w", err)
	}
	recent, err := r.q.ListDashboardRecentDeploys(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("ListDashboardRecentDeploys: %w", err)
	}
	user := pgtype.UUID{}
	if u, err := uuid.Parse(userID); err == nil {
		user = pgtype.UUID{Bytes: u, Valid: true}
	}
	work, err := r.q.DashboardWork(ctx, sqlcdb.DashboardWorkParams{OrgID: org, UserID: user})
	if err != nil {
		return nil, fmt.Errorf("DashboardWork: %w", err)
	}
	acts, err := r.q.DashboardWorkActivity(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("DashboardWorkActivity: %w", err)
	}

	data := &DashboardData{
		Deploys24h: Deploys24h{
			Total: stats.Total, Succeeded: stats.Succeeded, Failed: stats.Failed, MedianSeconds: stats.MedianSeconds,
		},
		FailedDeploys:  make([]FailedDeploy, 0, len(failed)),
		OpenAlerts:     make([]OpenAlert, 0, len(alerts)),
		Deploying:      make([]LiveDeploy, 0, len(live)),
		ProjectDeploys: make([]ProjectDeploy, 0, len(projects)),
		RecentDeploys:  make([]RecentDeployment, 0, len(recent)),
		Work: Work{PullsOpen: work.PullsOpen, PullsYours: work.PullsYours, IssuesInProgress: work.IssuesInProgress,
			IssuesYours: work.IssuesYours, IssuesShippedWeek: work.IssuesShippedWeek,
			CycleNumber: int(work.CycleNumber), IssuesShippedCycle: work.IssuesShippedCycle},
		Activity: make([]Activity, 0, len(acts)),
	}
	for _, row := range acts {
		a := Activity{Kind: row.Kind, At: row.At.Time, Actor: row.Actor, Automatic: row.Automatic, ProjectID: uuidString(row.ProjectID),
			Project: row.ProjectName, Title: row.Title, Number: int(row.Number), Repository: row.Repository, PageID: uuidString(row.PageID)}
		if row.IssueKey != "" {
			a.Key = issue.KeyOf(row.IssueKey, int(row.Number), int(row.Digits))
		}
		data.Activity = append(data.Activity, a)
	}
	for _, row := range failed {
		data.FailedDeploys = append(data.FailedDeploys, FailedDeploy{
			ID: uuidString(row.ID), ServiceID: uuidString(row.ServiceID), ServiceName: row.ServiceName,
			ProjectID: uuidString(row.ProjectID), ProjectName: row.ProjectName, EnvironmentName: row.EnvironmentName,
			ServerName: row.ServerName, ErrorSummary: row.ErrorSummary, CommitSHA: row.CommitSha,
			FailedAt: row.FailedAt.Time,
		})
	}
	for _, row := range alerts {
		data.OpenAlerts = append(data.OpenAlerts, OpenAlert{
			ID: uuidString(row.ID), Kind: row.Kind, Subject: row.Subject, SubjectName: row.SubjectName,
			Detail: row.Detail, ProjectID: uuidString(row.ProjectID), ProjectName: row.ProjectName,
			EnvironmentName: row.EnvironmentName, FiredAt: row.FiredAt.Time,
		})
	}
	for _, row := range live {
		data.Deploying = append(data.Deploying, LiveDeploy{
			ID: uuidString(row.ID), Status: row.Status, ServiceID: uuidString(row.ServiceID),
			ServiceName: row.ServiceName, SourceType: row.SourceType, HasComposeRepo: row.ComposeRepoUrl != "",
			ProjectID: uuidString(row.ProjectID), ProjectName: row.ProjectName, EnvironmentName: row.EnvironmentName,
			ServerName: row.ServerName, CommitSHA: row.CommitSha, CreatedAt: row.CreatedAt.Time,
		})
	}
	for _, row := range projects {
		data.ProjectDeploys = append(data.ProjectDeploys, ProjectDeploy{
			ProjectID: uuidString(row.ProjectID), Status: row.Status, ServiceName: row.ServiceName,
			SourceType: row.SourceType, GitBranch: row.GitBranch, DockerImage: row.DockerImage,
			CreatedAt: row.CreatedAt.Time,
		})
	}
	for _, row := range recent {
		var finished *time.Time
		if row.FinishedAt.Valid {
			finished = &row.FinishedAt.Time
		}
		data.RecentDeploys = append(data.RecentDeploys, RecentDeployment{
			ID: uuidString(row.ID), Status: row.Status, ServiceID: uuidString(row.ServiceID),
			ServiceName: row.ServiceName, ProjectID: uuidString(row.ProjectID), ProjectName: row.ProjectName,
			EnvironmentName: row.EnvironmentName, CreatedAt: row.CreatedAt.Time, FinishedAt: finished,
		})
	}
	return data, nil
}
