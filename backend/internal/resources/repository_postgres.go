package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// graceSettingKey is the instance_settings row that records the grace dates
// were set.
const graceSettingKey = "resource_limits_grace_set"

// PostgresRepository is the Postgres implementation of Repository.
type PostgresRepository struct {
	pool    *pgxpool.Pool
	queries *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool, queries: sqlcdb.New(pool)}
}

func pgID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid service id %q: %w", id, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

// float is a nullable numeric as *float64.
func float(n pgtype.Numeric) *float64 {
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	return &f.Float64
}

func floatOr0(n pgtype.Numeric) float64 {
	if f := float(n); f != nil {
		return *f
	}
	return 0
}

// pgIDs parses ids, and gives back each one as the caller wrote it, so the
// results are keyed by the caller's strings.
func pgIDs(ids []string) ([]pgtype.UUID, map[[16]byte]string, error) {
	out := make([]pgtype.UUID, 0, len(ids))
	back := make(map[[16]byte]string, len(ids))
	for _, id := range ids {
		sid, err := pgID(id)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, sid)
		back[sid.Bytes] = id
	}
	return out, back, nil
}

func (r *PostgresRepository) ComposeLimits(ctx context.Context, serviceID string) (map[string]ComposeLimit, error) {
	all, err := r.ComposeLimitsFor(ctx, []string{serviceID})
	if err != nil {
		return nil, err
	}
	if limits := all[serviceID]; limits != nil {
		return limits, nil
	}
	return map[string]ComposeLimit{}, nil
}

func (r *PostgresRepository) ComposeLimitsFor(ctx context.Context, serviceIDs []string) (map[string]map[string]ComposeLimit, error) {
	ids, back, err := pgIDs(serviceIDs)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListComposeLimitsForServices(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("ListComposeLimitsForServices: %w", err)
	}
	out := make(map[string]map[string]ComposeLimit, len(serviceIDs))
	for _, row := range rows {
		id := back[row.ServiceID.Bytes]
		if out[id] == nil {
			out[id] = map[string]ComposeLimit{}
		}
		out[id][row.ComposeService] = ComposeLimit{MemoryMB: row.MemoryMb, CPUCores: float(row.CpuCores)}
	}
	return out, nil
}

// ReplaceComposeLimits holds to version the way envvar's BulkUpsert does: the
// service's lock conflicts with the FOR KEY SHARE that adding a limit takes
// through its foreign key (a default added at a deploy, say), and the rows'
// locks hold the ones the check saw.
func (r *PostgresRepository) ReplaceComposeLimits(ctx context.Context, serviceID, version string, limits map[string]ComposeLimit) error {
	sid, err := pgID(serviceID)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after Commit
	q := r.queries.WithTx(tx)
	if _, err := q.LockServiceForReplace(ctx, sid); err != nil {
		return fmt.Errorf("LockServiceForReplace: %w", err)
	}
	stored, err := q.ListComposeLimitsForReplace(ctx, sid)
	if err != nil {
		return fmt.Errorf("ListComposeLimitsForReplace: %w", err)
	}
	current := make(map[string]ComposeLimit, len(stored))
	for _, row := range stored {
		current[row.ComposeService] = ComposeLimit{MemoryMB: row.MemoryMb, CPUCores: float(row.CpuCores)}
	}
	if versionOf(current) != version {
		return listversion.ErrChanged
	}
	if err := q.DeleteComposeLimits(ctx, sid); err != nil {
		return fmt.Errorf("DeleteComposeLimits: %w", err)
	}
	for name, l := range limits {
		var cpu pgtype.Numeric
		if l.CPUCores != nil {
			if err := cpu.Scan(strconv.FormatFloat(*l.CPUCores, 'f', -1, 64)); err != nil {
				return fmt.Errorf("CPU cores %v: %w", *l.CPUCores, err)
			}
		}
		if err := q.InsertComposeLimit(ctx, sqlcdb.InsertComposeLimitParams{
			ServiceID: sid, ComposeService: name, MemoryMb: l.MemoryMB, CpuCores: cpu,
		}); err != nil {
			return fmt.Errorf("InsertComposeLimit %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) AddDefaultComposeLimits(ctx context.Context, serviceID string, defaults map[string]int64) error {
	sid, err := pgID(serviceID)
	if err != nil {
		return err
	}
	for name, mb := range defaults {
		if err := r.queries.InsertComposeLimitIfAbsent(ctx, sqlcdb.InsertComposeLimitIfAbsentParams{
			ServiceID: sid, ComposeService: name, MemoryMb: mb,
		}); err != nil {
			return fmt.Errorf("InsertComposeLimitIfAbsent %s: %w", name, err)
		}
	}
	return nil
}

func (r *PostgresRepository) GraceUntil(ctx context.Context, serviceID string) (*time.Time, error) {
	sid, err := pgID(serviceID)
	if err != nil {
		return nil, err
	}
	ts, err := r.queries.GetMemoryLimitGrace(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("GetMemoryLimitGrace: %w", err)
	}
	if !ts.Valid {
		return nil, nil
	}
	return &ts.Time, nil
}

func (r *PostgresRepository) ClaimGrace(ctx context.Context, until time.Time) (bool, int64, error) {
	value, err := json.Marshal(until.UTC())
	if err != nil {
		return false, 0, fmt.Errorf("encode the grace date: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after Commit
	q := r.queries.WithTx(tx)
	inserted, err := q.ClaimInstanceSetting(ctx, sqlcdb.ClaimInstanceSettingParams{Key: graceSettingKey, Value: value})
	if err != nil {
		return false, 0, fmt.Errorf("ClaimInstanceSetting: %w", err)
	}
	if inserted == 0 {
		return false, 0, nil // an earlier start set them
	}
	services, err := q.SetMemoryLimitGrace(ctx, pgtype.Timestamptz{Time: until, Valid: true})
	if err != nil {
		return false, 0, fmt.Errorf("SetMemoryLimitGrace: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, fmt.Errorf("commit: %w", err)
	}
	return true, services, nil
}

func (r *PostgresRepository) UsageDaily(ctx context.Context, serviceID string, since time.Time) (map[string][]DailyUsage, error) {
	all, err := r.UsageDailyFor(ctx, []string{serviceID}, since)
	if err != nil {
		return nil, err
	}
	if usage := all[serviceID]; usage != nil {
		return usage, nil
	}
	return map[string][]DailyUsage{}, nil
}

func (r *PostgresRepository) UsageDailyFor(ctx context.Context, serviceIDs []string, since time.Time) (map[string]map[string][]DailyUsage, error) {
	ids, back, err := pgIDs(serviceIDs)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListUsageDailyForServices(ctx, sqlcdb.ListUsageDailyForServicesParams{
		ServiceIds: ids, Since: pgtype.Date{Time: since, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("ListUsageDailyForServices: %w", err)
	}
	out := make(map[string]map[string][]DailyUsage, len(serviceIDs))
	for _, row := range rows {
		id := back[row.ServiceID.Bytes]
		if out[id] == nil {
			out[id] = map[string][]DailyUsage{}
		}
		out[id][row.ComposeService] = append(out[id][row.ComposeService], DailyUsage{
			Day:          row.Day.Time,
			Samples:      int(row.Samples),
			MemoryPeakMB: floatOr0(row.MemoryPeakMb),
			CPUP95:       floatOr0(row.CpuP95Percent),
			ThrottledMax: float(row.ThrottledMaxPct),
			CPUWaitAvg:   float(row.CpuWaitAvgPct),
			OOMKills:     int(row.OomKills),
		})
	}
	return out, nil
}

func (r *PostgresRepository) OOMKillsSince(ctx context.Context, serviceID string, since time.Time) (map[string]int, error) {
	sid, err := pgID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.CountOOMKillsByComposeService(ctx, sqlcdb.CountOOMKillsByComposeServiceParams{
		ServiceID: sid, KilledAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("CountOOMKillsByComposeService: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.ComposeService] = int(row.Kills)
	}
	return out, nil
}

func (r *PostgresRepository) RestartsSince(ctx context.Context, serviceID string, since time.Time) (map[string]Restarts, error) {
	sid, err := pgID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.CountRestartsByComposeService(ctx, sqlcdb.CountRestartsByComposeServiceParams{
		ServiceID: sid, Since: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("CountRestartsByComposeService: %w", err)
	}
	out := make(map[string]Restarts, len(rows))
	for _, row := range rows {
		out[row.ComposeService] = Restarts{Samples: int(row.Samples), Restarts: int(row.Restarts), FirstAt: row.FirstAt.Time, Earlier: row.Earlier}
	}
	return out, nil
}

func (r *PostgresRepository) ServerCPUWait(ctx context.Context, serverID string, day time.Time) (*float64, error) {
	parsed, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	rows, err := r.queries.ListServerUsageDaily(ctx, sqlcdb.ListServerUsageDailyParams{
		ServerID: pgtype.UUID{Bytes: parsed, Valid: true},
		Day:      pgtype.Date{Time: day, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("ListServerUsageDaily: %w", err)
	}
	for _, row := range rows {
		if row.Day.Time.Format(time.DateOnly) == day.Format(time.DateOnly) && row.Samples > 0 {
			avg := floatOr0(row.CpuWaitSumPct) / float64(row.Samples)
			return &avg, nil
		}
	}
	return nil, nil
}

func (r *PostgresRepository) SummariseUsage(ctx context.Context, from, to time.Time) error {
	if err := r.queries.SummariseUsageDays(ctx, sqlcdb.SummariseUsageDaysParams{
		FromDay: pgtype.Date{Time: from, Valid: true},
		ToDay:   pgtype.Date{Time: to, Valid: true},
	}); err != nil {
		return fmt.Errorf("SummariseUsageDays: %w", err)
	}
	if err := r.queries.SummariseUsageSums(ctx, sqlcdb.SummariseUsageSumsParams{
		FromDay: pgtype.Date{Time: from, Valid: true},
		ToDay:   pgtype.Date{Time: to, Valid: true},
	}); err != nil {
		return fmt.Errorf("SummariseUsageSums: %w", err)
	}
	return nil
}

// DeleteUsageBefore runs the four deletes even when one fails: each table
// is kept on its own.
func (r *PostgresRepository) DeleteUsageBefore(ctx context.Context, before time.Time) error {
	day := pgtype.Date{Time: before, Valid: true}
	var errs []error
	if err := r.queries.DeleteUsageBefore(ctx, day); err != nil {
		errs = append(errs, fmt.Errorf("DeleteUsageBefore: %w", err))
	}
	if err := r.queries.DeleteUsageSumsBefore(ctx, day); err != nil {
		errs = append(errs, fmt.Errorf("DeleteUsageSumsBefore: %w", err))
	}
	if err := r.queries.DeleteServerUsageBefore(ctx, day); err != nil {
		errs = append(errs, fmt.Errorf("DeleteServerUsageBefore: %w", err))
	}
	if err := r.queries.DeleteOOMKillsBefore(ctx, pgtype.Timestamptz{Time: before, Valid: true}); err != nil {
		errs = append(errs, fmt.Errorf("DeleteOOMKillsBefore: %w", err))
	}
	return errors.Join(errs...)
}

func (r *PostgresRepository) InsertOOMKill(ctx context.Context, serviceID, composeService, container string, at time.Time) (bool, error) {
	sid, err := pgID(serviceID)
	if err != nil {
		return false, err
	}
	n, err := r.queries.InsertOOMKill(ctx, sqlcdb.InsertOOMKillParams{
		ServiceID: sid, ComposeService: composeService, Container: container,
		KilledAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("InsertOOMKill: %w", err)
	}
	return n > 0, nil
}

func (r *PostgresRepository) InsertLatency(ctx context.Context, serviceID string, at time.Time, buckets []int32, unmeasured string) error {
	sid, err := pgID(serviceID)
	if err != nil {
		return err
	}
	if buckets == nil {
		buckets = []int32{} // NOT NULL: an unmeasured minute has none
	}
	if err := r.queries.InsertLatency(ctx, sqlcdb.InsertLatencyParams{
		ServiceID: sid, CollectedAt: pgtype.Timestamptz{Time: at, Valid: true},
		Buckets: buckets, Unmeasured: unmeasured,
	}); err != nil {
		return fmt.Errorf("InsertLatency: %w", err)
	}
	return nil
}

func (r *PostgresRepository) LatencySince(ctx context.Context, serviceID string, since time.Time, bucketCount int) (LatencyWindow, error) {
	sid, err := pgID(serviceID)
	if err != nil {
		return LatencyWindow{}, err
	}
	ts := pgtype.Timestamptz{Time: since, Valid: true}
	w, err := r.queries.LatencyWindow(ctx, sqlcdb.LatencyWindowParams{ServiceID: sid, Since: ts, BucketCount: int32(bucketCount)})
	if err != nil {
		return LatencyWindow{}, fmt.Errorf("LatencyWindow: %w", err)
	}
	out := LatencyWindow{Minutes: int(w.Minutes), NewestUnmeasured: w.NewestUnmeasured}
	if w.FirstMeasured.Valid {
		t := w.FirstMeasured.Time
		out.FirstMeasured = &t
	}
	if out.FirstMeasured == nil {
		return out, nil
	}
	rows, err := r.queries.SumLatency(ctx, sqlcdb.SumLatencyParams{ServiceID: sid, Since: ts, BucketCount: int32(bucketCount)})
	if err != nil {
		return LatencyWindow{}, fmt.Errorf("SumLatency: %w", err)
	}
	out.Buckets = make([]int64, bucketCount)
	for _, row := range rows {
		if i := int(row.Idx) - 1; i >= 0 && i < bucketCount {
			out.Buckets[i] = row.Requests
		}
	}
	return out, nil
}

func (r *PostgresRepository) DeleteLatencyBefore(ctx context.Context, before time.Time) error {
	if err := r.queries.DeleteLatencyBefore(ctx, pgtype.Timestamptz{Time: before, Valid: true}); err != nil {
		return fmt.Errorf("DeleteLatencyBefore: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpsertServerCPUWait(ctx context.Context, serverID string, day time.Time, pct float64) error {
	parsed, err := uuid.Parse(serverID)
	if err != nil {
		return fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	var wait pgtype.Numeric
	if err := wait.Scan(strconv.FormatFloat(pct, 'f', 2, 64)); err != nil {
		return fmt.Errorf("CPU waiting %v: %w", pct, err)
	}
	if err := r.queries.UpsertServerUsageSample(ctx, sqlcdb.UpsertServerUsageSampleParams{
		ServerID:      pgtype.UUID{Bytes: parsed, Valid: true},
		Day:           pgtype.Date{Time: day, Valid: true},
		CpuWaitSumPct: wait,
	}); err != nil {
		return fmt.Errorf("UpsertServerUsageSample: %w", err)
	}
	return nil
}
