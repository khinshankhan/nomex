package data

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/khinshankhan/nomex/data/sqlcgen"
)

// DB is a database handle and the queries that run against it.
type DB struct {
	*sqlcgen.Queries

	sql *sql.DB
}

// Open connects to the SQLite database at path and applies the pragmas the
// sweeper needs. It does not run migrations; use `make migrate`.
func Open(ctx context.Context, path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// one connection: SQLite serializes writes anyway, and a pool turns a
	// queued write into SQLITE_BUSY against ourselves (WAL still lets reads
	// through during a write)
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("connect %s: %w", path, err)
	}

	if err := applyPragmas(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}

	return &DB{Queries: sqlcgen.New(sqlDB), sql: sqlDB}, nil
}

// dsn builds the connection string. modernc.org/sqlite takes pragmas as query
// parameters, which is the only way to apply them to every pooled connection.
func dsn(path string) string {
	return "file:" + filepath.ToSlash(path) +
		// otherwise the driver writes Go's format ("2026-08-23 16:31:45.48
		// +0000 UTC"), which datetime() can't parse
		// NOTE: comparisons then fall back to string ordering, which is right
		// often enough to hide the bug
		"?_time_format=sqlite" +
		// wait on a held lock instead of failing with SQLITE_BUSY right away
		"&_pragma=busy_timeout(5000)" +
		// readers and the writer don't block each other, set here so a fresh
		// database gets it without a migration
		"&_pragma=journal_mode(WAL)" +
		// sync at checkpoints instead of every commit, a power loss can lose
		// the last few transactions but every row can be rechecked anyway
		"&_pragma=synchronous(NORMAL)" +
		// off by default in SQLite, needed for attempts/blocked to reference
		// checks
		"&_pragma=foreign_keys(ON)"
}

// applyPragmas verifies the pragmas the DSN asked for actually took.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	// NOTE: a read only database, or a filesystem without shared memory,
	// quietly stays in rollback mode, so check what we actually got
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("journal_mode is %q, want wal", mode)
	}

	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("foreign_keys is off")
	}

	return nil
}

// Close releases the connection. Outstanding queries are not cancelled.
func (db *DB) Close() error {
	return db.sql.Close()
}

// Exec runs a statement outside the generated queries. It's meant for DDL and
// maintenance (test schemas, VACUUM, ANALYZE); real queries belong in queries/.
func (db *DB) Exec(ctx context.Context, stmt string, args ...any) error {
	_, err := db.sql.ExecContext(ctx, stmt, args...)
	return err
}

// Tx runs fn in a transaction, rolling back if it returns an error.
//
// NOTE: only the Queries passed to fn is bound to the transaction. Writing
// through db inside fn goes around it.
func (db *DB) Tx(ctx context.Context, fn func(*sqlcgen.Queries) error) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if err := fn(db.Queries.WithTx(tx)); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
