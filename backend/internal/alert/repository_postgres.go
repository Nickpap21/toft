package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository stores channels and open alerts, and finds who an alert
// is for. It satisfies both the monitor's store and the service's repo.
type PostgresRepository struct {
	q *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{q: sqlcdb.New(pool)}
}

func pgUUID(id string) (pgtype.UUID, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid id %q: %w", id, err)
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func uuidString(p pgtype.UUID) string {
	if !p.Valid {
		return ""
	}
	return uuid.UUID(p.Bytes).String()
}

// ─── Channels ─────────────────────────────────────────────────────────────────

func toChannel(row sqlcdb.AlertChannel) *Channel {
	ch := &Channel{
		OrgID:         uuidString(row.OrgID),
		WebhookURL:    row.WebhookUrl,
		SigningSecret: row.SigningSecret,
		Enabled:       row.Enabled,
		UpdatedAt:     row.UpdatedAt.Time,
		Delivery:      Delivery{Error: row.LastDeliveryError},
	}
	if row.LastDeliveryAt.Valid {
		t := row.LastDeliveryAt.Time
		ch.Delivery.At = &t
	}
	if row.LastDeliveryStatus != nil {
		st := int(*row.LastDeliveryStatus)
		ch.Delivery.Status = &st
	}
	return ch
}

func (r *PostgresRepository) RecordDelivery(ctx context.Context, orgID string, d Delivery) error {
	id, err := pgUUID(orgID)
	if err != nil {
		return err
	}
	var status *int32
	if d.Status != nil {
		st := int32(*d.Status)
		status = &st
	}
	if err := r.q.RecordAlertDelivery(ctx, sqlcdb.RecordAlertDeliveryParams{OrgID: id, LastDeliveryStatus: status, LastDeliveryError: d.Error}); err != nil {
		return fmt.Errorf("RecordAlertDelivery: %w", err)
	}
	return nil
}

func (r *PostgresRepository) RecordOneOff(ctx context.Context, e Event) error {
	if err := r.q.RecordOneOffAlert(ctx, sqlcdb.RecordOneOffAlertParams{
		Kind: string(e.Kind), Subject: e.Subject, Detail: e.Detail, FiredAt: pgtype.Timestamptz{Time: e.At, Valid: true},
	}); err != nil {
		return fmt.Errorf("RecordOneOffAlert: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListRecent(ctx context.Context, orgID string) ([]RecentAlert, error) {
	id, err := pgUUID(orgID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListRecentAlerts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListRecentAlerts: %w", err)
	}
	out := make([]RecentAlert, 0, len(rows))
	for _, row := range rows {
		a := RecentAlert{
			ID: uuidString(row.ID), Kind: Kind(row.Kind), Subject: row.Subject, Detail: row.Detail,
			FiredAt: row.FiredAt.Time, OneOff: oneOff(Kind(row.Kind)),
			SubjectName: row.SubjectName, ProjectID: uuidString(row.ProjectID), ProjectName: row.ProjectName,
			EnvironmentName: row.EnvironmentName, ServerName: row.ServerName,
		}
		if row.ResolvedAt.Valid {
			t := row.ResolvedAt.Time
			a.ResolvedAt = &t
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *PostgresRepository) GetChannel(ctx context.Context, orgID string) (*Channel, error) {
	id, err := pgUUID(orgID)
	if err != nil {
		return nil, err
	}
	row, err := r.q.GetAlertChannel(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoChannel
	}
	if err != nil {
		return nil, fmt.Errorf("GetAlertChannel: %w", err)
	}
	return toChannel(row), nil
}

func (r *PostgresRepository) CreateChannel(ctx context.Context, orgID, webhookURL, encryptedSecret string, enabled bool) (*Channel, error) {
	id, err := pgUUID(orgID)
	if err != nil {
		return nil, err
	}
	row, err := r.q.CreateAlertChannel(ctx, sqlcdb.CreateAlertChannelParams{
		OrgID: id, WebhookUrl: webhookURL, SigningSecret: encryptedSecret, Enabled: enabled,
	})
	if err != nil {
		return nil, fmt.Errorf("CreateAlertChannel: %w", err)
	}
	return toChannel(row), nil
}

func (r *PostgresRepository) UpdateChannel(ctx context.Context, orgID, webhookURL string, enabled bool) (*Channel, error) {
	id, err := pgUUID(orgID)
	if err != nil {
		return nil, err
	}
	row, err := r.q.UpdateAlertChannel(ctx, sqlcdb.UpdateAlertChannelParams{OrgID: id, WebhookUrl: webhookURL, Enabled: enabled})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoChannel
	}
	if err != nil {
		return nil, fmt.Errorf("UpdateAlertChannel: %w", err)
	}
	return toChannel(row), nil
}

func (r *PostgresRepository) RotateSecret(ctx context.Context, orgID, encryptedSecret string) error {
	id, err := pgUUID(orgID)
	if err != nil {
		return err
	}
	n, err := r.q.RotateAlertChannelSecret(ctx, sqlcdb.RotateAlertChannelSecretParams{OrgID: id, SigningSecret: encryptedSecret})
	if err != nil {
		return fmt.Errorf("RotateAlertChannelSecret: %w", err)
	}
	if n == 0 {
		return ErrNoChannel
	}
	return nil
}

func (r *PostgresRepository) DeleteChannel(ctx context.Context, orgID string) error {
	id, err := pgUUID(orgID)
	if err != nil {
		return err
	}
	if err := r.q.DeleteAlertChannel(ctx, id); err != nil {
		return fmt.Errorf("DeleteAlertChannel: %w", err)
	}
	return nil
}

// ─── Open alerts ──────────────────────────────────────────────────────────────

func (r *PostgresRepository) ListOpen(ctx context.Context) ([]key, error) {
	rows, err := r.q.ListOpenAlerts(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListOpenAlerts: %w", err)
	}
	keys := make([]key, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, key{Kind(row.Kind), row.Subject})
	}
	return keys, nil
}

func (r *PostgresRepository) Open(ctx context.Context, k key, detail string) (bool, error) {
	n, err := r.q.OpenAlert(ctx, sqlcdb.OpenAlertParams{Kind: string(k.kind), Subject: k.subject, Detail: detail})
	if err != nil {
		return false, fmt.Errorf("OpenAlert: %w", err)
	}
	return n > 0, nil
}

func (r *PostgresRepository) Resolve(ctx context.Context, k key) (bool, error) {
	n, err := r.q.ResolveAlert(ctx, sqlcdb.ResolveAlertParams{Kind: string(k.kind), Subject: k.subject})
	if err != nil {
		return false, fmt.Errorf("ResolveAlert: %w", err)
	}
	return n > 0, nil
}

func (r *PostgresRepository) SetDetail(ctx context.Context, k key, detail string) error {
	if err := r.q.SetOpenAlertDetail(ctx, sqlcdb.SetOpenAlertDetailParams{Kind: string(k.kind), Subject: k.subject, Detail: detail}); err != nil {
		return fmt.Errorf("SetOpenAlertDetail: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListGoneServiceAlerts(ctx context.Context) ([]key, error) {
	rows, err := r.q.ListGoneServiceAlerts(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListGoneServiceAlerts: %w", err)
	}
	keys := make([]key, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, key{Kind(row.Kind), row.Subject})
	}
	return keys, nil
}

func (r *PostgresRepository) DeleteResolvedBefore(ctx context.Context, before time.Time) error {
	return r.q.DeleteResolvedAlertsBefore(ctx, pgtype.Timestamptz{Time: before, Valid: true})
}

// ─── Routing ──────────────────────────────────────────────────────────────────

func (r *PostgresRepository) ServiceContext(ctx context.Context, serviceID string) (*ServiceContext, error) {
	id, err := pgUUID(serviceID)
	if err != nil {
		return nil, err
	}
	row, err := r.q.GetServiceAlertContext(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("GetServiceAlertContext: %w", err)
	}
	return &ServiceContext{
		Name: row.ServiceName, Project: row.ProjectName, Environment: row.EnvironmentName,
		Server: row.ServerName, OrgID: uuidString(row.OrgID),
	}, nil
}

func (r *PostgresRepository) ServicesOrgIDs(ctx context.Context, serviceIDs []string) ([]string, error) {
	ids := make([]pgtype.UUID, 0, len(serviceIDs))
	for _, id := range serviceIDs {
		u, err := pgUUID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, u)
	}
	rows, err := r.q.ListServicesOrgIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("ListServicesOrgIDs: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, uuidString(row))
	}
	return out, nil
}

func (r *PostgresRepository) ServerContext(ctx context.Context, serverID string) (name, orgID string, err error) {
	id, err := pgUUID(serverID)
	if err != nil {
		return "", "", err
	}
	row, err := r.q.GetServerAlertContext(ctx, id)
	if err != nil {
		return "", "", fmt.Errorf("GetServerAlertContext: %w", err)
	}
	return row.Name, uuidString(row.OrgID), nil
}

func (r *PostgresRepository) VolumeContext(ctx context.Context, volumeID string) (name, orgID string, err error) {
	id, err := pgUUID(volumeID)
	if err != nil {
		return "", "", err
	}
	row, err := r.q.GetSharedVolumeAlertContext(ctx, id)
	if err != nil {
		return "", "", fmt.Errorf("GetSharedVolumeAlertContext: %w", err)
	}
	return row.Name, uuidString(row.OrgID), nil
}

func (r *PostgresRepository) MirrorContext(ctx context.Context, mirrorID string) (name, orgID string, err error) {
	id, err := pgUUID(mirrorID)
	if err != nil {
		return "", "", err
	}
	row, err := r.q.GetMirrorAlertContext(ctx, id)
	if err != nil {
		return "", "", fmt.Errorf("GetMirrorAlertContext: %w", err)
	}
	what := "the push mirror of " + row.FullName + " to " + row.Url
	if row.Direction == "pull" {
		what = row.FullName + "'s pull mirror of " + row.Url
	}
	return what, uuidString(row.OrgID), nil
}

func (r *PostgresRepository) OperatorOrgIDs(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListOperatorOrgIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListOperatorOrgIDs: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, uuidString(row))
	}
	return ids, nil
}
