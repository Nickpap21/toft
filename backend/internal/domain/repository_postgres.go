package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{
		db:      pool,
		queries: sqlcdb.New(pool),
	}
}

func (r *PostgresRepository) Create(ctx context.Context, serviceID, createdBy string, input CreateInput) (*Domain, error) {
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	var by pgtype.UUID
	if u, err := uuid.Parse(createdBy); err == nil {
		by = pgtype.UUID{Bytes: u, Valid: true}
	}
	row, err := r.queries.SaveNewDomain(ctx, sqlcdb.SaveNewDomainParams{
		ServiceID:  pgtype.UUID{Bytes: parsedServiceID, Valid: true},
		Hostname:   input.Hostname,
		TlsEnabled: input.TLSEnabled,
		CreatedBy:  by,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "domains_hostname_key" {
		return nil, ErrHostnameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("SaveNewDomain: %w", err)
	}

	d := mapDBDomainToDomain(row)
	return &d, nil
}

func (r *PostgresRepository) ListByService(ctx context.Context, serviceID string) ([]Domain, error) {
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	rows, err := r.queries.ListDomainsForService(ctx, pgtype.UUID{Bytes: parsedServiceID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListDomainsForService: %w", err)
	}

	domains := make([]Domain, len(rows))
	for i, row := range rows {
		domains[i] = Domain{
			ID:         uuidString(row.ID),
			ServiceID:  uuidString(row.ServiceID),
			Hostname:   row.Hostname,
			TLSEnabled: row.TlsEnabled,
			Status:     Status(row.Status),
			CreatedAt:  row.CreatedAt.Time,
			UpdatedAt:  row.UpdatedAt.Time,
			CreatedBy:  row.CreatedByName,
			RoutedAt:   timePtr(row.RoutedAt),

			CertificateAt: timePtr(row.CertificateAt),
			Temporary:     IsTemporary(row.Hostname),
		}
	}

	return domains, nil
}

func (r *PostgresRepository) ListByOrg(ctx context.Context, orgID string) ([]WorkspaceDomain, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace id %q: %w", orgID, err)
	}
	rows, err := r.queries.ListOrgDomains(ctx, pgtype.UUID{Bytes: oid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListOrgDomains: %w", err)
	}
	out := make([]WorkspaceDomain, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkspaceDomain{
			ID: uuidString(row.ID), Hostname: row.Hostname, CreatedAt: row.CreatedAt.Time, CreatedBy: row.CreatedByName,
			RoutedAt: timePtr(row.RoutedAt), CertificateAt: timePtr(row.CertificateAt), Temporary: IsTemporary(row.Hostname),
			ServiceID: uuidString(row.ServiceID), Service: row.ServiceName, Scope: row.Scope,
			ProjectID: uuidString(row.ProjectID), Project: row.ProjectName, Environment: row.EnvironmentName, ServerID: uuidString(row.ServerID),
		})
	}
	return out, nil
}

func (r *PostgresRepository) Delete(ctx context.Context, serviceID, id string) (string, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return "", ErrNotFound
	}
	parsedService, err := uuid.Parse(serviceID)
	if err != nil {
		return "", fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	hostname, err := r.queries.DeleteDomain(ctx, sqlcdb.DeleteDomainParams{
		ID:        pgtype.UUID{Bytes: parsedID, Valid: true},
		ServiceID: pgtype.UUID{Bytes: parsedService, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("DeleteDomain: %w", err)
	}
	return hostname, nil
}

func (r *PostgresRepository) UpdateStatus(ctx context.Context, id string, status Status) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid domain id %q: %w", id, err)
	}

	if err := r.queries.UpdateDomainStatus(ctx, sqlcdb.UpdateDomainStatusParams{
		ID:     pgtype.UUID{Bytes: parsedID, Valid: true},
		Status: string(status),
	}); err != nil {
		return fmt.Errorf("UpdateDomainStatus: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ActivateForService(ctx context.Context, serviceID string) error {
	parsedID, err := uuid.Parse(serviceID)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	if err := r.queries.ActivateDomainsForService(ctx, pgtype.UUID{Bytes: parsedID, Valid: true}); err != nil {
		return fmt.Errorf("ActivateDomainsForService: %w", err)
	}
	return nil
}

func (r *PostgresRepository) AwaitingCertificate(ctx context.Context) ([]Domain, error) {
	rows, err := r.queries.ListDomainsAwaitingCertificate(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListDomainsAwaitingCertificate: %w", err)
	}
	out := make([]Domain, 0, len(rows))
	for _, row := range rows {
		out = append(out, Domain{ID: uuidString(row.ID), ServiceID: uuidString(row.ServiceID), Hostname: row.Hostname, Temporary: IsTemporary(row.Hostname)})
	}
	return out, nil
}

func (r *PostgresRepository) SetCertificate(ctx context.Context, id string) (bool, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return false, fmt.Errorf("invalid domain id %q: %w", id, err)
	}
	n, err := r.queries.SetDomainCertificate(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return false, fmt.Errorf("SetDomainCertificate: %w", err)
	}
	return n > 0, nil
}

func (r *PostgresRepository) Taken(ctx context.Context, hostname string) (bool, error) {
	taken, err := r.queries.HostnameTaken(ctx, hostname)
	if err != nil {
		return false, fmt.Errorf("HostnameTaken: %w", err)
	}
	return taken, nil
}

// ─── Mapper ───────────────────────────────────────────────────────────────────

func mapDBDomainToDomain(row sqlcdb.Domain) Domain {
	return Domain{
		ID:         uuidString(row.ID),
		ServiceID:  uuidString(row.ServiceID),
		Hostname:   row.Hostname,
		TLSEnabled: row.TlsEnabled,
		Status:     Status(row.Status),
		CreatedAt:  row.CreatedAt.Time,
		UpdatedAt:  row.UpdatedAt.Time,
		RoutedAt:   timePtr(row.RoutedAt),

		CertificateAt: timePtr(row.CertificateAt),
		Temporary:     IsTemporary(row.Hostname),
	}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func uuidString(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}
