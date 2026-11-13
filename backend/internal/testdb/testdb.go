// Package testdb gives a test its own Postgres database with schema.sql
// applied, for the tests that need the real SQL (security close-out plan,
// S1). `make test-db` starts one; TOFT_TEST_DATABASE_URL points at it.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvURL names the server the databases are made on. Its own database is
// only connected to, never written.
const EnvURL = "TOFT_TEST_DATABASE_URL"

// New returns a pool on a new database with db/migrations/schema.sql
// applied, dropped when the test ends. It skips when EnvURL is unset and
// fails when the server set there doesn't answer, so CI can't pass by not
// running it.
//
// ponytail: one database per test (~0.6 s each to apply the schema) keeps
// the conversions, which scan whole tables, apart from each other's rows;
// clone a template database per test if the count makes it slow.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skip(EnvURL + " is unset (make test-db starts a database for it)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}
	defer admin.Close(context.Background())

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("name the database: %v", err)
	}
	name := "toft_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { drop(t, url, name) })

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvURL, err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(pool.Close)

	schema, err := os.ReadFile(schemaPath())
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	// No arguments: pgx sends it over the simple protocol, which runs every
	// statement in the file.
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}
	return pool
}

// drop removes the test's database; FORCE ends connections still open.
func drop(t *testing.T, url, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Errorf("drop %s: %v", name, err)
		return
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Errorf("drop %s: %v", name, err)
	}
}

// schemaPath is db/migrations/schema.sql, found from this file: go:embed
// can't reach outside the package's directory.
func schemaPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "schema.sql")
}
