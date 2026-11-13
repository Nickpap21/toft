package resources_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The minutes of the window are summed per bucket, only those of the
// current bounds' length; the newest row says whether it was measured; the
// GC keeps what is after its time; an unmeasured minute can't carry counts.
func TestLatencyRepository(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := resources.NewPostgresRepository(db)
	var svc string
	require.NoError(t, db.QueryRow(ctx, `WITH u AS (INSERT INTO users (email, name, password_hash) VALUES ('dev@toft.test', 'Dev', 'x') RETURNING id),
		p AS (INSERT INTO projects (user_id, name) SELECT id, 'Shop' FROM u RETURNING id),
		e AS (INSERT INTO environments (project_id, name) SELECT id, 'production' FROM p RETURNING id)
		INSERT INTO services (environment_id, name, type, source_type, port) SELECT id, 'web', 'web', 'docker-image', 80 FROM e RETURNING id::text`).Scan(&svc))

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	b := func(first, fourth int32) []int32 {
		out := make([]int32, 12)
		out[0], out[3] = first, fourth
		return out
	}
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-40*time.Hour), b(math.MaxInt32, math.MaxInt32), ""), "a minute's buckets sum past int32")
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-30*time.Hour), b(1000, 0), ""))      // before the window
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-3*time.Hour), b(10, 2), ""))         // first in it
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-2*time.Hour), []int32{5, 5, 5}, "")) // other bounds: skipped
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-time.Hour), b(4, 1), ""))            //
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-time.Hour), b(999, 999), ""))        // the same minute again: kept as it was
	require.NoError(t, repo.InsertLatency(ctx, svc, now.Add(-time.Minute), nil, "traefik"))       // the newest: not measured

	w, err := repo.LatencySince(ctx, svc, now.Add(-24*time.Hour), 12)
	require.NoError(t, err)
	assert.Equal(t, 4, w.Minutes)
	assert.Equal(t, "traefik", w.NewestUnmeasured)
	require.NotNil(t, w.FirstMeasured)
	assert.True(t, now.Add(-3*time.Hour).Equal(*w.FirstMeasured))
	assert.Equal(t, []int64{14, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0}, w.Buckets)

	_, err = db.Exec(ctx, `INSERT INTO service_latency (service_id, collected_at, buckets, unmeasured) VALUES ($1, now(), '{3}', 'agent')`, svc)
	assert.ErrorContains(t, err, "service_latency_unmeasured_empty", "an unmeasured minute has no counts")

	require.NoError(t, repo.DeleteLatencyBefore(ctx, now.Add(-48*time.Hour)))
	var n int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM service_latency`).Scan(&n))
	assert.Equal(t, 6, n, "nothing older than 48 hours yet")
	require.NoError(t, repo.DeleteLatencyBefore(ctx, now.Add(-24*time.Hour)))
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM service_latency`).Scan(&n))
	assert.Equal(t, 4, n)

	w, err = repo.LatencySince(ctx, svc, now, 12)
	require.NoError(t, err)
	assert.Equal(t, resources.LatencyWindow{}, w, "no minute after since")
}
