package code

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Import, mirrors and outbound webhooks in Postgres (D-79).

func intPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

func toMirror(r sqlcdb.ListRepositoryMirrorsRow) StoredMirror {
	m := Mirror{ID: uuidStr(r.ID), Direction: r.Direction, URL: r.Url, Username: r.Username, HasPassword: r.HasPassword,
		Keep: r.Keep, CreatedAt: r.CreatedAt.Time, CreatedBy: person(uuidStr(r.CreatedBy), r.CreatedByName),
		SyncingSince: timePtr(r.SyncingSince), LastSyncAt: timePtr(r.LastSyncAt), LastOKAt: timePtr(r.LastOkAt),
		NextSyncAt: timePtr(r.NextSyncAt), Failures: int(r.Failures), Error: r.LastError, LastMoved: intPtr(r.LastMoved)}
	if r.RemoteBranches != nil {
		m.Remote = &MirrorRemote{DefaultBranch: str(r.RemoteHead), Branches: int(*r.RemoteBranches), Tags: int(*r.RemoteTags)}
	}
	m.State = m.state()
	return StoredMirror{Mirror: m, RepoID: uuidStr(r.RepositoryID)}
}

func (r *postgresRepository) ListMirrors(ctx context.Context, repoID string) ([]StoredMirror, error) {
	rows, err := r.q.ListRepositoryMirrors(ctx, optUUID(repoID))
	if err != nil {
		return nil, fmt.Errorf("ListRepositoryMirrors: %w", err)
	}
	out := make([]StoredMirror, 0, len(rows))
	for _, row := range rows {
		out = append(out, toMirror(row))
	}
	return out, nil
}

func (r *postgresRepository) ProjectPullMirrors(ctx context.Context, projectID string) ([]StoredMirror, error) {
	rows, err := r.q.ListProjectPullMirrors(ctx, optUUID(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListProjectPullMirrors: %w", err)
	}
	out := make([]StoredMirror, 0, len(rows))
	for _, row := range rows {
		out = append(out, toMirror(sqlcdb.ListRepositoryMirrorsRow(row)))
	}
	return out, nil
}

func (r *postgresRepository) CountPushMirrors(ctx context.Context, repoID string) (int, error) {
	n, err := r.q.CountPushMirrors(ctx, optUUID(repoID))
	if err != nil {
		return 0, fmt.Errorf("CountPushMirrors: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) CreateMirror(ctx context.Context, m NewMirror) (string, error) {
	if m.Direction == "pull" {
		if err := r.q.DeleteFailedImport(ctx, optUUID(m.RepoID)); err != nil {
			return "", fmt.Errorf("DeleteFailedImport: %w", err)
		}
	}
	id, err := r.q.InsertMirror(ctx, sqlcdb.InsertMirrorParams{ID: optUUID(m.ID), RepositoryID: optUUID(m.RepoID), Direction: m.Direction, Url: m.URL,
		Username: m.Username, PasswordEncrypted: optText(m.PasswordEnc), Keep: m.Keep, CreatedBy: optUUID(m.CreatedBy)})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrImportRunning
	}
	if err != nil {
		return "", fmt.Errorf("InsertMirror: %w", err)
	}
	return uuidStr(id), nil
}

func (r *postgresRepository) DeleteMirror(ctx context.Context, repoID, id string) error {
	_, err := r.q.DeleteMirror(ctx, sqlcdb.DeleteMirrorParams{RepositoryID: optUUID(repoID), ID: optUUID(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("DeleteMirror: %w", err)
	}
	return nil
}

func (r *postgresRepository) RequestSync(ctx context.Context, repoID, id string) (bool, error) {
	n, err := r.q.RequestMirrorSync(ctx, sqlcdb.RequestMirrorSyncParams{RepositoryID: optUUID(repoID), ID: optUUID(id)})
	if err != nil {
		return false, fmt.Errorf("RequestMirrorSync: %w", err)
	}
	return n > 0, nil
}

func (r *postgresRepository) ClaimMirrors(ctx context.Context, n int) ([]ClaimedMirror, error) {
	rows, err := r.q.ClaimMirrors(ctx, int32(n))
	if err != nil {
		return nil, fmt.Errorf("ClaimMirrors: %w", err)
	}
	out := make([]ClaimedMirror, 0, len(rows))
	for _, row := range rows {
		out = append(out, ClaimedMirror{ID: uuidStr(row.ID), RepoID: uuidStr(row.RepositoryID), OrgID: uuidStr(row.OrgID),
			Direction: row.Direction, URL: row.Url, Username: row.Username, PasswordEnc: str(row.PasswordEncrypted),
			CreatedBy: uuidStr(row.CreatedBy), Keep: row.Keep, Imported: row.LastOkAt.Valid, Failures: int(row.Failures)})
	}
	return out, nil
}

func (r *postgresRepository) SetMirrorRemote(ctx context.Context, id string, refs gitstore.RemoteRefs) error {
	b, t := int32(refs.Branches), int32(refs.Tags)
	if err := r.q.SetMirrorRemote(ctx, sqlcdb.SetMirrorRemoteParams{RemoteHead: optText(refs.Head), RemoteBranches: &b, RemoteTags: &t, ID: optUUID(id)}); err != nil {
		return fmt.Errorf("SetMirrorRemote: %w", err)
	}
	return nil
}

func (r *postgresRepository) FinishMirror(ctx context.Context, id string, res MirrorResult) (int, error) {
	if res.Err == "" {
		moved := int32(res.Moved)
		if err := r.q.FinishMirrorOK(ctx, sqlcdb.FinishMirrorOKParams{Moved: &moved, PullEvery: pullEvery.Seconds(), ID: optUUID(id)}); err != nil {
			return 0, fmt.Errorf("FinishMirrorOK: %w", err)
		}
		if err := r.q.DeleteImportedOnce(ctx, optUUID(id)); err != nil {
			return 0, fmt.Errorf("DeleteImportedOnce: %w", err)
		}
		return 0, nil
	}
	msg := truncateRunes(res.Err, 1000)
	n, err := r.q.FinishMirrorFailed(ctx, sqlcdb.FinishMirrorFailedParams{LastError: &msg, RetryAt: ts(res.RetryAt), ID: optUUID(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil // removed meanwhile
	}
	if err != nil {
		return 0, fmt.Errorf("FinishMirrorFailed: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) ResetSyncing(ctx context.Context) error {
	if err := r.q.ResetSyncingMirrors(ctx); err != nil {
		return fmt.Errorf("ResetSyncingMirrors: %w", err)
	}
	return nil
}

func (r *postgresRepository) MirrorFailures(ctx context.Context) ([]MirrorHealth, error) {
	rows, err := r.q.ListFailingMirrors(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListFailingMirrors: %w", err)
	}
	out := make([]MirrorHealth, 0, len(rows))
	for _, row := range rows {
		out = append(out, MirrorHealth{MirrorID: uuidStr(row.ID), Failing: row.Failures >= MirrorFailingAfter, Detail: row.LastError})
	}
	return out, nil
}

// ─── webhooks ────────────────────────────────────────────────────────────────

func toDelivery(id pgtype.UUID, event, summary, state string, attempts int32, status, ms *int32, errMsg *string,
	created, delivered, next pgtype.Timestamptz, actorID pgtype.UUID, actorName string) WebhookDelivery {
	d := WebhookDelivery{ID: uuidStr(id), Event: event, Summary: summary, State: state, Attempts: int(attempts),
		ResponseStatus: intPtr(status), DurationMS: intPtr(ms), Error: errMsg, CreatedAt: created.Time,
		DeliveredAt: timePtr(delivered), Actor: person(uuidStr(actorID), actorName)}
	if state == "pending" {
		d.NextAttemptAt = timePtr(next)
	}
	return d
}

func (r *postgresRepository) ListWebhooks(ctx context.Context, repoID string) ([]StoredWebhook, error) {
	rows, err := r.q.ListRepositoryWebhooks(ctx, optUUID(repoID))
	if err != nil {
		return nil, fmt.Errorf("ListRepositoryWebhooks: %w", err)
	}
	out := make([]StoredWebhook, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredWebhook{Webhook: Webhook{ID: uuidStr(row.ID), URL: row.Url, Events: row.Events, Active: row.Active,
			CreatedAt: row.CreatedAt.Time, CreatedBy: person(uuidStr(row.CreatedBy), row.CreatedByName),
			SecretRotatedAt: row.SecretRotatedAt.Time, SecretRotatedBy: person(uuidStr(row.SecretRotatedBy), row.SecretRotatedByName)}})
	}
	return out, nil
}

func (r *postgresRepository) CountWebhooks(ctx context.Context, repoID string) (int, error) {
	n, err := r.q.CountRepositoryWebhooks(ctx, optUUID(repoID))
	if err != nil {
		return 0, fmt.Errorf("CountRepositoryWebhooks: %w", err)
	}
	return int(n), nil
}

func (r *postgresRepository) CreateWebhook(ctx context.Context, w NewWebhook) (string, error) {
	id, err := r.q.InsertRepositoryWebhook(ctx, sqlcdb.InsertRepositoryWebhookParams{ID: optUUID(w.ID), RepositoryID: optUUID(w.RepoID), Url: w.URL,
		Events: w.Events, Active: w.Active, SecretEncrypted: w.SecretEnc, CreatedBy: optUUID(w.CreatedBy)})
	if err != nil {
		return "", fmt.Errorf("InsertRepositoryWebhook: %w", err)
	}
	return uuidStr(id), nil
}

// UpdateWebhook changes what in names; pausing fails the deliveries that
// wait, in one transaction.
func (r *postgresRepository) UpdateWebhook(ctx context.Context, repoID, id string, in WebhookUpdate) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := r.q.WithTx(tx)
	n, err := q.UpdateRepositoryWebhook(ctx, sqlcdb.UpdateRepositoryWebhookParams{Url: in.URL, Events: in.Events, Active: in.Active,
		RepositoryID: optUUID(repoID), ID: optUUID(id)})
	if err != nil {
		return false, fmt.Errorf("UpdateRepositoryWebhook: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if in.Active != nil && !*in.Active {
		if err := q.FailPausedDeliveries(ctx, optUUID(id)); err != nil {
			return false, fmt.Errorf("FailPausedDeliveries: %w", err)
		}
	}
	return true, tx.Commit(ctx)
}

func (r *postgresRepository) RotateWebhookSecret(ctx context.Context, repoID, id, secretEnc, actorID string) (bool, error) {
	n, err := r.q.RotateRepositoryWebhookSecret(ctx, sqlcdb.RotateRepositoryWebhookSecretParams{SecretEncrypted: secretEnc,
		RotatedBy: optUUID(actorID), RepositoryID: optUUID(repoID), ID: optUUID(id)})
	if err != nil {
		return false, fmt.Errorf("RotateRepositoryWebhookSecret: %w", err)
	}
	return n > 0, nil
}

func (r *postgresRepository) DeleteWebhook(ctx context.Context, repoID, id string) (bool, error) {
	n, err := r.q.DeleteRepositoryWebhook(ctx, sqlcdb.DeleteRepositoryWebhookParams{RepositoryID: optUUID(repoID), ID: optUUID(id)})
	if err != nil {
		return false, fmt.Errorf("DeleteRepositoryWebhook: %w", err)
	}
	return n > 0, nil
}

func (r *postgresRepository) InsertPing(ctx context.Context, webhookID, actorID string) (string, error) {
	id, err := r.q.InsertPingDelivery(ctx, sqlcdb.InsertPingDeliveryParams{WebhookID: optUUID(webhookID), ActorID: optUUID(actorID)})
	if err != nil {
		return "", fmt.Errorf("InsertPingDelivery: %w", err)
	}
	return uuidStr(id), nil
}

func (r *postgresRepository) ListDeliveries(ctx context.Context, webhookID string, afterAt time.Time, afterID string, n int) ([]WebhookDelivery, error) {
	rows, err := r.q.ListWebhookDeliveries(ctx, sqlcdb.ListWebhookDeliveriesParams{WebhookID: optUUID(webhookID), BeforeAt: ts(afterAt),
		BeforeID: optUUID(afterID), N: int32(n)})
	if err != nil {
		return nil, fmt.Errorf("ListWebhookDeliveries: %w", err)
	}
	out := make([]WebhookDelivery, 0, len(rows))
	for _, x := range rows {
		out = append(out, toDelivery(x.ID, x.Event, x.Summary, x.State, x.Attempts, x.ResponseStatus, x.DurationMs, x.Error,
			x.CreatedAt, x.DeliveredAt, x.NextAttemptAt, x.ActorID, x.ActorName))
	}
	return out, nil
}

func (r *postgresRepository) LastDeliveries(ctx context.Context, repoID string) (map[string]WebhookDelivery, error) {
	rows, err := r.q.LastWebhookDeliveries(ctx, optUUID(repoID))
	if err != nil {
		return nil, fmt.Errorf("LastWebhookDeliveries: %w", err)
	}
	out := make(map[string]WebhookDelivery, len(rows))
	for _, x := range rows {
		out[uuidStr(x.WebhookID)] = toDelivery(x.ID, x.Event, x.Summary, x.State, x.Attempts, x.ResponseStatus, x.DurationMs, x.Error,
			x.CreatedAt, x.DeliveredAt, x.NextAttemptAt, x.ActorID, x.ActorName)
	}
	return out, nil
}

func (r *postgresRepository) GetDelivery(ctx context.Context, repoID, webhookID, id string) (*StoredDelivery, error) {
	if _, err := pgUUID(id); err != nil {
		return nil, ErrNotFound
	}
	x, err := r.q.GetWebhookDelivery(ctx, sqlcdb.GetWebhookDeliveryParams{RepositoryID: optUUID(repoID), WebhookID: optUUID(webhookID), ID: optUUID(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetWebhookDelivery: %w", err)
	}
	return &StoredDelivery{WebhookDelivery: toDelivery(x.ID, x.Event, x.Summary, x.State, x.Attempts, x.ResponseStatus, x.DurationMs,
		x.Error, x.CreatedAt, x.DeliveredAt, x.NextAttemptAt, x.ActorID, x.ActorName), Payload: x.Payload, ResponseBody: x.ResponseBody}, nil
}

func (r *postgresRepository) Redeliver(ctx context.Context, webhookID, id, actorID string) (bool, error) {
	n, err := r.q.RedeliverWebhookDelivery(ctx, sqlcdb.RedeliverWebhookDeliveryParams{ActorID: optUUID(actorID), WebhookID: optUUID(webhookID), ID: optUUID(id)})
	if err != nil {
		return false, fmt.Errorf("RedeliverWebhookDelivery: %w", err)
	}
	return n > 0, nil
}

func (r *postgresRepository) ClaimDeliveries(ctx context.Context, n int) ([]ClaimedDelivery, error) {
	rows, err := r.q.ClaimWebhookDeliveries(ctx, int32(n))
	if err != nil {
		return nil, fmt.Errorf("ClaimWebhookDeliveries: %w", err)
	}
	out := make([]ClaimedDelivery, 0, len(rows))
	for _, x := range rows {
		d := ClaimedDelivery{ID: uuidStr(x.ID), WebhookID: uuidStr(x.WebhookID), RepoID: uuidStr(x.RepositoryID), URL: x.Url,
			SecretEnc: x.SecretEncrypted, Event: x.Event, ActorID: uuidStr(x.ActorID), Payload: str(x.Payload),
			Attempts: int(x.Attempts), CreatedAt: x.CreatedAt.Time}
		if x.GitEventID != nil {
			d.GitEventID = *x.GitEventID
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *postgresRepository) SetDeliveryPayload(ctx context.Context, id, summary, payload string) error {
	if err := r.q.SetDeliveryPayload(ctx, sqlcdb.SetDeliveryPayloadParams{Summary: truncateRunes(summary, 300), Payload: &payload, ID: optUUID(id)}); err != nil {
		return fmt.Errorf("SetDeliveryPayload: %w", err)
	}
	return nil
}

func (r *postgresRepository) FinishDelivery(ctx context.Context, id string, res DeliveryResult) error {
	var msg *string
	if res.Error != nil {
		m := truncateRunes(*res.Error, 500)
		msg = &m
	}
	next := res.NextAttemptAt
	if next.IsZero() {
		next = time.Now()
	}
	if err := r.q.FinishWebhookDelivery(ctx, sqlcdb.FinishWebhookDeliveryParams{State: res.State, NextAttemptAt: ts(next),
		ResponseStatus: int32Ptr(res.ResponseStatus), ResponseBody: res.ResponseBody, Error: msg, DurationMs: int32Ptr(res.DurationMS),
		ID: optUUID(id)}); err != nil {
		return fmt.Errorf("FinishWebhookDelivery: %w", err)
	}
	return nil
}

func (r *postgresRepository) DeleteOldDeliveries(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q.DeleteOldWebhookDeliveries(ctx, ts(before))
	if err != nil {
		return 0, fmt.Errorf("DeleteOldWebhookDeliveries: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) GetEvent(ctx context.Context, id int64) (*StoredEvent, error) {
	x, err := r.q.GetGitEvent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetGitEvent: %w", err)
	}
	return &StoredEvent{ID: x.ID, RepoID: uuidStr(x.RepositoryID), Kind: x.Kind, Action: str(x.Action), Branch: x.Branch,
		Before: x.BeforeSha, After: x.AfterSha, ActorID: uuidStr(x.ActorID), ActorName: x.ActorName,
		PullRequestID: uuidStr(x.PullRequestID), Commits: int(x.Commits), Forced: x.Forced, CreatedAt: x.CreatedAt.Time}, nil
}

func (r *postgresRepository) PullByID(ctx context.Context, id string) (*StoredPull, error) {
	x, err := r.q.GetPullRequestByID(ctx, optUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetPullRequestByID: %w", err)
	}
	return &StoredPull{ID: id, Number: int(x.Number), Title: x.Title, State: x.State, Base: x.BaseBranch, Head: x.HeadBranch,
		HeadSHA: x.HeadSha, MergeSHA: str(x.MergeSha), AuthorID: uuidStr(x.AuthorID), AuthorName: x.AuthorName}, nil
}

func (r *postgresRepository) RepoForHook(ctx context.Context, id string) (*HookRepo, error) {
	x, err := r.q.GetRepositoryForWebhook(ctx, optUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetRepositoryForWebhook: %w", err)
	}
	return &HookRepo{ID: uuidStr(x.ID), Name: x.Name, ProjectID: uuidStr(x.ProjectID), OrgSlug: x.OrgSlug}, nil
}
