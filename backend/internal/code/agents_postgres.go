package code

import (
	"context"
	"errors"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *postgresRepository) BranchDeployed(ctx context.Context, repoID, branch string) (bool, error) {
	return r.q.BranchDeployed(ctx, sqlcdb.BranchDeployedParams{RepositoryID: optUUID(repoID), GitBranch: branch})
}

func (r *postgresRepository) AgentBranch(ctx context.Context, repoID, name string) (*AgentBranch, error) {
	row, err := r.q.GetAgentBranch(ctx, sqlcdb.GetAgentBranchParams{RepositoryID: optUUID(repoID), Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &AgentBranch{RepoID: uuidStr(row.RepositoryID), Name: row.Name, ConnectionID: uuidStr(row.ConnectionID), UserID: uuidStr(row.UserID),
		AgentName: row.AgentName, UserName: row.UserName, ExpiresAt: row.PasswordEndsAt.Time}, nil
}

func (r *postgresRepository) AgentBranchByToken(ctx context.Context, hash string) (*AgentBranch, error) {
	row, err := r.q.AgentBranchByToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &AgentBranch{RepoID: uuidStr(row.RepositoryID), Name: row.Name, ConnectionID: uuidStr(row.ConnectionID),
		UserID: uuidStr(row.UserID), OrgID: uuidStr(row.OrgID), AgentName: row.AgentName, Paused: row.Paused}, nil
}

func (r *postgresRepository) UpsertAgentBranch(ctx context.Context, repoID, name, connectionID, userID, hash string, expires time.Time) error {
	return r.q.UpsertAgentBranch(ctx, sqlcdb.UpsertAgentBranchParams{RepositoryID: optUUID(repoID), Name: name,
		ConnectionID: optUUID(connectionID), UserID: optUUID(userID), TokenHash: hash,
		TokenExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}})
}

func (r *postgresRepository) AgentBranches(ctx context.Context, repoID string) (map[string]string, error) {
	rows, err := r.q.ListAgentBranches(ctx, optUUID(repoID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Name] = row.AgentName + " for " + row.UserName
	}
	return out, nil
}
