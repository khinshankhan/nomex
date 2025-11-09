package sqlite

import (
	"database/sql"
	"errors"
	"strings"

	_ "modernc.org/sqlite"
)

var (
	SQLiteEmptyPathError = errors.New("sqlite: database path cannot be empty")
)

func GetConnection(opts Options) (*sql.DB, error) {
	if opts.Path == "" {
		return nil, SQLiteEmptyPathError
	}

	// Special-case memory: pooled connections create separate DBs so we force single connection.
	if opts.Path == ":memory:" {
		opts.MaxOpenConns = 1
		opts.MaxIdleConns = 1
		// journal_mode=WAL is NOT supported on pure :memory:, so fall back to DELETE.
		if strings.EqualFold(opts.JournalMode, "WAL") {
			opts.JournalMode = "DELETE"
		}
	}

	dsn := buildDSN(opts)

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if opts.MaxOpenConns > 0 {
		conn.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		conn.SetMaxIdleConns(opts.MaxIdleConns)
	}
	conn.SetConnMaxLifetime(0)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func CloseConnection(db *sql.DB) error {
	return db.Close()
}
