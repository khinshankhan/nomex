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

// Open connects to the SQLite database at path. It doesn't run migrations, use
// `make migrate`.
func Open(ctx context.Context, path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// SQLite serializes writes anyway, a pool just gets SQLITE_BUSY from itself
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

// dsn builds the connection string, pragmas included so every connection gets
// them.
func dsn(path string) string {
	return "file:" + filepath.ToSlash(path) +
		// NOTE: otherwise the driver writes Go's time format, which datetime()
		// can't parse
		"?_time_format=sqlite" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		// a power loss can lose the last few writes, every row can be rechecked
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"
}

// applyPragmas checks the DSN's pragmas actually took.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	// NOTE: a read only database or a filesystem without shared memory quietly
	// stays in rollback mode
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

// Exec runs a statement outside the generated queries, for DDL and
// maintenance. Real queries belong in queries/.
func (db *DB) Exec(ctx context.Context, stmt string, args ...any) error {
	_, err := db.sql.ExecContext(ctx, stmt, args...)
	return err
}

// Tx runs fn in a transaction, rolling back if it returns an error.
//
// NOTE: only the Queries passed to fn is in the transaction.
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
