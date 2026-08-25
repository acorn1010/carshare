// Package pgtest connects integration tests to a real Postgres and applies
// db/schema.sql from scratch. A real database is the point: the exclusion
// constraint and the recurrence SQL are the parts worth testing, and no mock
// exercises them.
//
// Tests skip unless CARSHARE_TEST_DATABASE_URL is set. Start a database with
// scripts/dev_db.sh, which prints the export line.
package pgtest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvURL names the environment variable holding the test database URL.
const EnvURL = "CARSHARE_TEST_DATABASE_URL"

// MustPool returns a pool against a freshly rebuilt cars schema, or skips the
// test when no database is configured.
func MustPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("integration test: set %s to run (see scripts/dev_db.sh)", EnvURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	applySchema(t, ctx, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgtest: pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pgtest: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// applySchema drops the cars schema and replays db/schema.sql. The DDL runs on
// a simple-protocol connection because the file holds many statements and the
// extended protocol only accepts one at a time.
func applySchema(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("pgtest: parse url: %v", err)
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("pgtest: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ddl, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatalf("pgtest: read schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS cars CASCADE`); err != nil {
		t.Fatalf("pgtest: drop schema: %v", err)
	}
	if _, err := conn.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("pgtest: apply schema: %v", err)
	}
}

// schemaPath locates db/schema.sql relative to this source file, so tests work
// from any package directory.
func schemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("pgtest: cannot locate caller")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "db", "schema.sql")
}
