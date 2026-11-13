-- name: ListComposeLimitsForReplace :many
-- What a replace of a stack's limits can delete, locked until it commits; its
-- version is computed from these. After LockServiceForReplace.
SELECT compose_service, memory_mb, cpu_cores FROM service_compose_limits
WHERE service_id = $1
ORDER BY compose_service
FOR UPDATE;

-- name: DeleteComposeLimits :exec
DELETE FROM service_compose_limits WHERE service_id = $1;

-- name: InsertComposeLimit :exec
INSERT INTO service_compose_limits (service_id, compose_service, memory_mb, cpu_cores)
VALUES ($1, $2, $3, $4);

-- name: InsertComposeLimitIfAbsent :exec
INSERT INTO service_compose_limits (service_id, compose_service, memory_mb, cpu_cores)
VALUES ($1, $2, $3, NULL)
ON CONFLICT (service_id, compose_service) DO NOTHING;

-- name: InsertOOMKill :execrows
-- 0 rows: the kill was already recorded (uq_service_oom_kills_kill).
INSERT INTO service_oom_kills (service_id, compose_service, container, killed_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING;

-- name: CountOOMKillsByComposeService :many
-- idx_service_oom_kills_service_time.
SELECT compose_service, COUNT(*)::int AS kills FROM service_oom_kills
WHERE service_id = $1 AND killed_at >= $2
GROUP BY compose_service;

-- name: CountRestartsByComposeService :many
-- A service's restarts from since on, per compose service: the sum of its
-- samples' restarts (each the restarts since the previous sample, which
-- the collector counts per container). samples is how many carry a count;
-- earlier whether one before since does, so they cover the whole window
-- (idx_service_metrics_service_time serves both).
SELECT m.compose_service,
       COUNT(*)::int AS samples,
       SUM(m.restarts)::int AS restarts,
       MIN(m.collected_at)::timestamptz AS first_at,
       EXISTS (SELECT 1 FROM service_metrics e
               WHERE e.service_id = sqlc.arg(service_id) AND e.compose_service = m.compose_service
                 AND e.collected_at < sqlc.arg(since) AND e.restarts IS NOT NULL) AS earlier
FROM service_metrics m
WHERE m.service_id = sqlc.arg(service_id) AND m.collected_at >= sqlc.arg(since)
  AND m.restarts IS NOT NULL
GROUP BY m.compose_service;

-- name: UpsertServerUsageSample :exec
INSERT INTO server_usage_daily (server_id, day, samples, cpu_wait_sum_pct, cpu_wait_max_pct)
VALUES ($1, $2, 1, $3, $3)
ON CONFLICT (server_id, day) DO UPDATE SET
    samples          = server_usage_daily.samples + 1,
    cpu_wait_sum_pct = server_usage_daily.cpu_wait_sum_pct + EXCLUDED.cpu_wait_sum_pct,
    cpu_wait_max_pct = GREATEST(server_usage_daily.cpu_wait_max_pct, EXCLUDED.cpu_wait_max_pct);

-- name: SummariseUsageDays :exec
-- Recomputes the daily rows of the UTC days from $1 (inclusive) to $2
-- (exclusive) from the samples and kills; reruns overwrite. The peak is the
-- highest single container's memory (memory_peak_mb), and older rows'
-- summed memory_mb when no sample has one. A day with kills but no samples
-- (a container killed before it was ever sampled) gets a row too: no
-- samples, zero use, its kills. The days are UTC whatever the session's
-- TimeZone, as the day column is.
INSERT INTO service_usage_daily (service_id, compose_service, day, samples, memory_peak_mb,
    memory_p95_mb, cpu_p95_percent, throttled_max_pct, cpu_wait_avg_pct, oom_kills)
SELECT m.service_id, m.compose_service, m.day,
    COUNT(*)::int,
    COALESCE(MAX(m.memory_peak_mb), MAX(m.memory_mb)),
    percentile_cont(0.95) WITHIN GROUP (ORDER BY COALESCE(m.memory_peak_mb, m.memory_mb)),
    percentile_cont(0.95) WITHIN GROUP (ORDER BY m.cpu_percent),
    MAX(m.throttled_pct),
    AVG(m.cpu_wait_pct),
    (SELECT COUNT(*)::int FROM service_oom_kills k
      WHERE k.service_id = m.service_id AND k.compose_service = m.compose_service
        AND (k.killed_at AT TIME ZONE 'UTC')::date = m.day)
-- The day is computed in a derived table: Postgres rejects the same expression
-- inside the correlated subquery as an ungrouped outer column.
FROM (SELECT service_id, compose_service, memory_mb, memory_peak_mb, cpu_percent,
             throttled_pct, cpu_wait_pct, (collected_at AT TIME ZONE 'UTC')::date AS day
      FROM service_metrics
      WHERE collected_at >= (sqlc.arg(from_day)::date::timestamp AT TIME ZONE 'UTC')
        AND collected_at < (sqlc.arg(to_day)::date::timestamp AT TIME ZONE 'UTC')) m
GROUP BY m.service_id, m.compose_service, m.day
UNION ALL
-- Kill-only days. NOT EXISTS looks at the whole UTC day, so no key comes from
-- both halves (ON CONFLICT cannot update a row twice in one statement);
-- idx_service_metrics_service_time serves it.
SELECT k.service_id, k.compose_service, k.day, 0, 0, 0, 0, NULL, NULL, COUNT(*)::int
FROM (SELECT service_id, compose_service, (killed_at AT TIME ZONE 'UTC')::date AS day
      FROM service_oom_kills
      WHERE killed_at >= (sqlc.arg(from_day)::date::timestamp AT TIME ZONE 'UTC')
        AND killed_at < (sqlc.arg(to_day)::date::timestamp AT TIME ZONE 'UTC')) k
WHERE NOT EXISTS (
    SELECT 1 FROM service_metrics s
    WHERE s.service_id = k.service_id AND s.compose_service = k.compose_service
      AND s.collected_at >= (k.day::timestamp AT TIME ZONE 'UTC')
      AND s.collected_at < ((k.day + 1)::timestamp AT TIME ZONE 'UTC'))
GROUP BY k.service_id, k.compose_service, k.day
ON CONFLICT (service_id, compose_service, day) DO UPDATE SET
    samples = EXCLUDED.samples, memory_peak_mb = EXCLUDED.memory_peak_mb,
    memory_p95_mb = EXCLUDED.memory_p95_mb, cpu_p95_percent = EXCLUDED.cpu_p95_percent,
    throttled_max_pct = EXCLUDED.throttled_max_pct, cpu_wait_avg_pct = EXCLUDED.cpu_wait_avg_pct,
    oom_kills = EXCLUDED.oom_kills;

-- name: SummariseUsageSums :exec
-- Recomputes service_usage_daily_sums for the UTC days from $1 (inclusive)
-- to $2 (exclusive); reruns overwrite. A sample row is already its compose
-- service's containers summed; the whole service's (compose_service '')
-- sums its rows at each collection time, so for a single-container service
-- it is its own rows; the first half leaves '' out so those rows are not
-- counted twice. The outer GROUP BY gives each key one row (ON CONFLICT
-- cannot update a row twice). The days are UTC whatever the session's
-- TimeZone, as the day column is.
INSERT INTO service_usage_daily_sums (service_id, compose_service, day, memory_peak_mb, cpu_p95_percent)
SELECT t.service_id, t.compose_service, t.day, MAX(t.memory),
       percentile_cont(0.95) WITHIN GROUP (ORDER BY t.cpu)
FROM (
    SELECT service_id, compose_service, (collected_at AT TIME ZONE 'UTC')::date AS day,
           memory_mb AS memory, cpu_percent AS cpu
    FROM service_metrics
    WHERE collected_at >= (sqlc.arg(from_day)::date::timestamp AT TIME ZONE 'UTC')
      AND collected_at < (sqlc.arg(to_day)::date::timestamp AT TIME ZONE 'UTC')
      AND compose_service <> ''
    UNION ALL
    SELECT service_id, '', (collected_at AT TIME ZONE 'UTC')::date,
           SUM(memory_mb), SUM(cpu_percent)
    FROM service_metrics
    WHERE collected_at >= (sqlc.arg(from_day)::date::timestamp AT TIME ZONE 'UTC')
      AND collected_at < (sqlc.arg(to_day)::date::timestamp AT TIME ZONE 'UTC')
    GROUP BY service_id, collected_at
) t
GROUP BY t.service_id, t.compose_service, t.day
ON CONFLICT (service_id, compose_service, day) DO UPDATE SET
    memory_peak_mb = EXCLUDED.memory_peak_mb, cpu_p95_percent = EXCLUDED.cpu_p95_percent;

-- name: ListUsageDailyForServices :many
-- The services' daily rows, in one query (the primary key leads with
-- service_id).
SELECT service_id, compose_service, day, samples, memory_peak_mb, cpu_p95_percent,
       throttled_max_pct, cpu_wait_avg_pct, oom_kills
FROM service_usage_daily
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND day >= sqlc.arg(since)
ORDER BY service_id, compose_service, day;

-- name: ListComposeLimitsForServices :many
SELECT service_id, compose_service, memory_mb, cpu_cores FROM service_compose_limits
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY service_id, compose_service;

-- name: ListServerUsageDaily :many
SELECT day, samples, cpu_wait_sum_pct, cpu_wait_max_pct FROM server_usage_daily
WHERE server_id = $1 AND day >= $2 ORDER BY day;

-- name: DeleteUsageBefore :exec
DELETE FROM service_usage_daily WHERE day < $1;

-- name: DeleteUsageSumsBefore :exec
DELETE FROM service_usage_daily_sums WHERE day < $1;

-- name: DeleteOOMKillsBefore :exec
DELETE FROM service_oom_kills WHERE killed_at < $1;

-- name: DeleteServerUsageBefore :exec
DELETE FROM server_usage_daily WHERE day < $1;

-- name: InsertLatency :exec
-- A service's minute of requests (D-156). A minute already stored (an
-- agent's sample read twice) stays as it was.
INSERT INTO service_latency (service_id, collected_at, buckets, unmeasured)
VALUES (@service_id, @collected_at, @buckets::int[], @unmeasured)
ON CONFLICT (service_id, collected_at) DO NOTHING;

-- name: SumLatency :many
-- A service's requests per bucket (1-based) since since, over the minutes
-- measured with bucket_count buckets; served by the primary key.
SELECT b.idx::int AS idx, SUM(b.n)::bigint AS requests
FROM service_latency l
CROSS JOIN LATERAL unnest(l.buckets) WITH ORDINALITY AS b(n, idx)
WHERE l.service_id = @service_id AND l.collected_at > @since AND cardinality(l.buckets) = @bucket_count::int
GROUP BY b.idx
ORDER BY b.idx;

-- name: LatencyWindow :one
-- How a service's minutes since since were read: how many there are, why
-- the newest was not measured (empty when it was), and the first one measured
-- with bucket_count buckets (NULL when none was).
SELECT COUNT(*)::int AS minutes,
       COALESCE((SELECT n.unmeasured FROM service_latency n
                 WHERE n.service_id = @service_id AND n.collected_at > @since
                 ORDER BY n.collected_at DESC LIMIT 1), '')::text AS newest_unmeasured,
       MIN(l.collected_at) FILTER (WHERE cardinality(l.buckets) = @bucket_count::int)::timestamptz AS first_measured
FROM service_latency l
WHERE l.service_id = @service_id AND l.collected_at > @since;

-- name: DeleteLatencyBefore :exec
DELETE FROM service_latency WHERE collected_at < $1;

-- name: ClaimInstanceSetting :execrows
INSERT INTO instance_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO NOTHING;

-- name: SetMemoryLimitGrace :execrows
UPDATE services SET memory_limit_grace_until = $1
WHERE memory_limit_grace_until IS NULL
  AND (source_type = 'docker-compose' OR COALESCE(memory_limit, '') = '');

-- name: GetMemoryLimitGrace :one
SELECT memory_limit_grace_until FROM services WHERE id = $1;
