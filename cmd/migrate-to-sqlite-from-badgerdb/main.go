package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	_ "modernc.org/sqlite"
)

type BadgerRow struct {
	Domain string     `json:"Domain"`
	Code   *int       `json:"Code"` // -1 in badger means "unset"; treat as NULL
	At     *time.Time `json:"At"`   // optional
}

const (
	sqlitePath = "domains.sqlite"
	schemaDDL  = `
CREATE TABLE IF NOT EXISTS checks (
  domain      TEXT PRIMARY KEY,
  code        INTEGER NULL,
  checked_at  DATETIME NULL
);
`
	upsertSQL = `
INSERT INTO checks (domain, code, checked_at)
VALUES (?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET
  code = excluded.code,
  checked_at = excluded.checked_at;
`
)

func keyFor(domain string) []byte {
	return []byte("checks/" + domain)
}

func openBadger(dir string) (*badger.DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return badger.Open(
		badger.DefaultOptions(dir).
			WithValueDir(dir).
			WithLoggingLevel(badger.WARNING),
	)
}

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// reasonable pragmas for batch writes supposedly
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		return nil, err
	}
	return db, nil
}

// format as RFC3339 in UTC, which SQLite accepts as DATETIME TEXT
func toSQLiteDT(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func migrate(ctx context.Context, bdb *badger.DB, sdb *sql.DB) error {
	tx, err := sdb.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, upsertSQL)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	const prefixStr = "checks/"
	prefix := []byte(prefixStr)
	count := 0
	const commitEvery = 10_000

	err = bdb.View(func(btx *badger.Txn) error {
		it := btx.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()
			domain := strings.TrimPrefix(string(key), prefixStr)

			var row BadgerRow
			if err := item.Value(func(v []byte) error {
				// some rows might be raw or partially filled; be lenient
				if len(v) == 0 || v[0] != '{' {
					// tolerate non-JSON values by treating as empty with derived domain
					return nil
				}
				return json.Unmarshal(v, &row)
			}); err != nil {
				return fmt.Errorf("unmarshal %q: %w", key, err)
			}
			row.Domain = domain // key is source of truth

			// Code: nil or -1 -> NULL
			var code any
			if row.Code == nil || *row.Code == -1 {
				code = nil
			} else {
				code = int64(*row.Code)
			}

			if _, err := stmt.ExecContext(ctx, row.Domain, code, toSQLiteDT(row.At)); err != nil {
				return fmt.Errorf("sqlite upsert %q: %w", row.Domain, err)
			}

			count++
			if count%commitEvery == 0 {
				if err := tx.Commit(); err != nil {
					return err
				}
				tx, err = sdb.BeginTx(ctx, &sql.TxOptions{})
				if err != nil {
					return err
				}
				stmt, err = tx.PrepareContext(ctx, upsertSQL)
				if err != nil {
					_ = tx.Rollback()
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func main() {
	ctx := context.Background()

	bdb, err := openBadger("db")
	if err != nil {
		log.Fatal(err)
	}
	defer bdb.Close()

	sdb, err := openSQLite(sqlitePath)
	if err != nil {
		log.Fatal(err)
	}
	defer sdb.Close()

	start := time.Now()
	if err := migrate(ctx, bdb, sdb); err != nil {
		log.Fatal(err)
	}
	log.Println("Migration complete in", time.Since(start).Round(time.Millisecond))
}
