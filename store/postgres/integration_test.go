//go:build integration

// Package postgres integration tests run the shared store conformance,
// concurrency, and outbox suites against a live PostgreSQL instance.
//
// They are gated behind the "integration" build tag and require a PostgreSQL
// DSN in the POSTGRES_DSN environment variable.
//
//	POSTGRES_DSN=postgres://mailbox_test:mailbox_test@localhost:5433/mailbox_test?sslmode=disable \
//	    go test -tags integration -race ./store/postgres/...
//
// docker-compose.test.yml provisions a matching database on port 5433.
package postgres

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rbaliyan/event/v3/outbox"
	"github.com/rbaliyan/event/v3/transport"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/storetest"
)

var tableSeq uint64

// uniqueTable returns a process-unique, SQL-safe table name so each store
// obtained from the factory is fully isolated from every other.
func uniqueTable(prefix string) string {
	n := atomic.AddUint64(&tableSeq, 1)
	return fmt.Sprintf("%s_%d", prefix, n)
}

// dialDB connects to PostgreSQL from POSTGRES_DSN, skipping the test when the
// variable is unset. The connection is closed via t.Cleanup.
func dialDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_DSN to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sqlx.ConnectContext(ctx, "postgres", dsn)
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newStore builds a connected store on a fresh table, dropping that table (and
// its outbox table) on cleanup. extra options let callers enable the outbox.
func newStore(t *testing.T, db *sqlx.DB, extra ...Option) store.Store {
	t.Helper()
	table := uniqueTable("messages")
	outboxTable := uniqueTable("outbox")
	opts := append([]Option{
		WithTable(table),
		WithOutboxTable(outboxTable),
	}, extra...)
	s := New(db, opts...)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("store connect: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
		_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", outboxTable))
		_ = s.Close(context.Background())
	})
	return s
}

// sharedStoreFactory returns a NewStoreFunc that hands every subtest the same
// connected store. The conformance and concurrency suites isolate subtests with
// unique owner IDs, so a single table is sufficient — and sharing it avoids
// creating a table and its secondary-index set per subtest, which otherwise
// churns the test database with DDL on throwaway tables.
func sharedStoreFactory(t *testing.T, db *sqlx.DB, extra ...Option) storetest.NewStoreFunc {
	t.Helper()
	s := newStore(t, db, extra...)
	return func(t *testing.T) store.Store { return s }
}

func TestPostgresConformance(t *testing.T) {
	db := dialDB(t)
	storetest.RunStoreSuite(t, sharedStoreFactory(t, db))
}

func TestPostgresConcurrency(t *testing.T) {
	db := dialDB(t)
	storetest.RunConcurrencySuite(t, sharedStoreFactory(t, db))
}

func TestPostgresOutbox(t *testing.T) {
	db := dialDB(t)
	storetest.RunOutboxSuite(t, sharedStoreFactory(t, db, WithOutbox(true)))
}

// TestPostgresFailure runs the failure-mode suite (context cancellation/deadline)
// against live PostgreSQL, where database/sql propagates context cancellation.
func TestPostgresFailure(t *testing.T) {
	db := dialDB(t)
	storetest.RunFailureSuite(t, sharedStoreFactory(t, db))
}

// pgRelayFactory builds a real event/v3 outbox Relay that reads the same outbox
// table the store writes to and publishes to the supplied transport.
func pgRelayFactory(t *testing.T, s store.Store, tr transport.Transport) storetest.RelayRunner {
	t.Helper()
	ps, ok := s.(*Store)
	if !ok {
		t.Fatalf("pgRelayFactory: store is %T, want *Store", s)
	}
	outboxStore, err := outbox.NewPostgresStore(ps.db.DB, outbox.WithTable(ps.opts.outboxTable))
	if err != nil {
		t.Fatalf("create postgres outbox store: %v", err)
	}
	return outbox.NewRelay(outboxStore, tr, outbox.WithBatchSize(100))
}

// TestPostgresRelay runs the end-to-end outbox relay suite against live PostgreSQL.
func TestPostgresRelay(t *testing.T) {
	db := dialDB(t)
	storetest.RunRelaySuite(t, sharedStoreFactory(t, db, WithOutbox(true)), pgRelayFactory)
}
