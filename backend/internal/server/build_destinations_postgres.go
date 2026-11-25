package server

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/jackc/pgx/v5"
)

// The lists of build destinations (BuildDestinationStore). Every statement
// runs on the context's transaction (dbtx). A server ID of "" is NULL, the
// panel's server; an ID that is no UUID matches nothing.

func (r *postgresRepository) LockBuildDestinations(ctx context.Context, serverID string) error {
	if err := sqlcdb.New(dbtx.On(ctx, r.db)).LockBuildDestinations(ctx, serverID); err != nil {
		return fmt.Errorf("lock the build destinations: %w", err)
	}
	return nil
}

func (r *postgresRepository) ListBuildDestinations(ctx context.Context, serverID string) ([]BuildDestination, error) {
	rows, err := sqlcdb.New(dbtx.On(ctx, r.db)).ListBuildDestinations(ctx, pgUUID(serverID))
	if err != nil {
		return nil, fmt.Errorf("list the build destinations: %w", err)
	}
	out := make([]BuildDestination, 0, len(rows))
	for _, row := range rows {
		out = append(out, BuildDestination{ID: uuidString(row.ID), Prefix: row.Cidr, Port: row.Port, Note: row.Note, CreatedAt: row.CreatedAt.Time})
	}
	return out, nil
}

func (r *postgresRepository) AddBuildDestination(ctx context.Context, serverID string, prefix netip.Prefix, port *int32, note, actorID string) (BuildDestination, error) {
	row, err := sqlcdb.New(dbtx.On(ctx, r.db)).AddBuildDestination(ctx, sqlcdb.AddBuildDestinationParams{
		ServerID: pgUUID(serverID), Cidr: prefix, Port: port, Note: note, CreatedBy: pgUUID(actorID),
	})
	if isUniqueViolation(err) {
		return BuildDestination{}, &BuildDestinationError{Code: codeBuildDestinationExists, Message: "the list has this address and port already"}
	}
	if err != nil {
		return BuildDestination{}, fmt.Errorf("add the build destination: %w", err)
	}
	return BuildDestination{ID: uuidString(row.ID), Prefix: row.Cidr, Port: row.Port, Note: row.Note, CreatedAt: row.CreatedAt.Time}, nil
}

func (r *postgresRepository) RemoveBuildDestination(ctx context.Context, serverID, id string) (BuildDestination, error) {
	row, err := sqlcdb.New(dbtx.On(ctx, r.db)).RemoveBuildDestination(ctx, sqlcdb.RemoveBuildDestinationParams{ID: pgUUID(id), ServerID: pgUUID(serverID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return BuildDestination{}, ErrBuildDestinationNotFound
	}
	if err != nil {
		return BuildDestination{}, fmt.Errorf("remove the build destination: %w", err)
	}
	return BuildDestination{ID: uuidString(row.ID), Prefix: row.Cidr, Port: row.Port, Note: row.Note, CreatedAt: row.CreatedAt.Time}, nil
}
