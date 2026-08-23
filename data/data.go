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

	// One writer. SQLite serialises writes regardless, and a pool of them
	// turns a queued write into SQLITE_BUSY against our own connections.
	// Reads are unaffected: WAL lets them proceed during a write.
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
// parameters, which is the only way to set them for every connection the pool
// opens rather than just the first.
func dsn(path string) string {
	return "file:" + filepath.ToSlash(path) +
		// Wait rather than returning SQLITE_BUSY the instant a lock is held.
		"?_pragma=busy_timeout(5000)" +
		// Readers do not block the writer and vice versa. Persistent, but set
		// here so a fresh database gets it without a migration.
		"&_pragma=journal_mode(WAL)" +
		// Sync at checkpoints rather than every commit. With WAL this risks
		// losing the last transactions on power loss, not corruption, and
		// every row here is re-derivable by checking the domain again.
		"&_pragma=synchronous(NORMAL)" +
		// Off by default in SQLite, and required for the references between
		// attempts/blocked and checks to mean anything.
		"&_pragma=foreign_keys(ON)"
}

// applyPragmas sets what the DSN cannot and verifies what it claims to.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	// journal_mode is persistent and returns the resulting mode, so this both
	// applies and confirms it: a database opened read-only, or on a filesystem
	// without shared-memory support, silently stays in rollback mode.
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

// Tx runs fn in a transaction, rolling back if it returns an error.
//
// The Queries handed to fn is bound to the transaction; the receiver's own
// Queries is not, so writing through db inside fn escapes the transaction.
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
