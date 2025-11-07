package main

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Row struct {
	Domain string
	Code   *int // nil until set (represented as -1 in badger)
	At     *time.Time
}

func openDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS checks (
  domain TEXT PRIMARY KEY,
  code INTEGER NULL,           -- HTTP status code (NULL until first attempt)
  checked_at DATETIME NULL     -- last attempt time (NULL until first attempt)
);
CREATE INDEX IF NOT EXISTS idx_checks_code ON checks(code);
`); err != nil {
		return nil, err
	}

	return db, nil
}

func bulkEnsureRows(db *sql.DB, domains []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO checks(domain) VALUES(?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, d := range domains {
		if _, err := stmt.Exec(d); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// format as RFC3339 in UTC, which SQLite accepts as DATETIME TEXT
func toSQLiteDT(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func saveCode(db *sql.DB, domain string, code int, at *time.Time) error {
	_, err := db.Exec(
		`UPDATE checks SET code=?, checked_at=? WHERE domain=?`, code, toSQLiteDT(at), domain,
	)
	return err
}

func loadPendingFromDB(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT domain FROM checks WHERE code IS NULL OR code NOT IN (200,404)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadAvailableFromDB(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT domain FROM checks WHERE code = 404`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
