package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// New creates and validates a pgx connection pool from the given config.
func New(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// The daily usage queries compare timestamptz with dates, which Postgres
	// casts in the session time zone; they mean UTC days.
	pcfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	// pgx defaults MaxConns to max(4, NumCPU); the backend container has one
	// CPU, so four connections served the API, the collectors and every
	// deploy. The URL's pool_max_conns and pool_max_conn_lifetime still win.
	if !strings.Contains(cfg.DatabaseURL, "pool_max_conns") {
		pcfg.MaxConns = 10
	}
	if !strings.Contains(cfg.DatabaseURL, "pool_max_conn_lifetime") {
		pcfg.MaxConnLifetime = 30 * time.Minute
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

// LogPoolStats writes the pool's counters at debug level every interval, so
// a wait for a connection (EmptyAcquireCount growing) can be seen before
// it is felt. Stops when ctx ends.
func LogPoolStats(ctx context.Context, pool *pgxpool.Pool, log *zap.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := pool.Stat()
			log.Debug("pool",
				zap.Int32("total", st.TotalConns()), zap.Int32("idle", st.IdleConns()), zap.Int32("max", st.MaxConns()),
				zap.Int64("acquires", st.AcquireCount()), zap.Int64("waited", st.EmptyAcquireCount()),
				zap.Duration("wait_total", st.AcquireDuration()))
		}
	}
}
