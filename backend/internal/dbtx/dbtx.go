// Package dbtx runs several modules' writes in one transaction (7g: a
// project and its first service). Runner.InTx puts the transaction in the
// context; a repository that takes part runs its statements on On(ctx, db),
// so outside InTx nothing changes and inside it the repository's own Begin
// is a savepoint in the one transaction. The use case sees only a function
// of a context, never pgx.
package dbtx

import (
	"context"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is what a repository runs statements on: its pool, or the transaction
// in the context.
type DB interface {
	sqlcdb.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type key struct{}

// Runner begins the transactions. Satisfies the use cases' Transactor.
type Runner struct{ pool *pgxpool.Pool }

func NewRunner(pool *pgxpool.Pool) Runner { return Runner{pool: pool} }

// InTx runs fn in one transaction: committed when fn returns nil, rolled
// back otherwise. A transaction is one connection: fn must not use its
// context from more than one goroutine.
func (r Runner) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after Commit
	if err := fn(context.WithValue(ctx, key{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// On is the transaction InTx put in ctx, or db outside one.
func On(ctx context.Context, db DB) DB {
	if tx, ok := ctx.Value(key{}).(pgx.Tx); ok {
		return tx
	}
	return db
}
