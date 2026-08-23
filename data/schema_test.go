package data

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migrationDir is relative to this package. go:embed cannot reach outside the
// package directory and will not follow a symlink, so the files are read at
// run time instead.
const migrationDir = "../migrations"

// openTest returns a DB with the schema applied.
//
// A temp file rather than :memory: -- an in-memory database cannot use WAL, and
// Open verifies the journal mode, so testing against one would mean relaxing
// the check this package exists to make. t.TempDir cleans up on its own.
func openTest(t *testing.T) *DB {
	t.Helper()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.sql.ExecContext(context.Background(), schemaUp(t)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

// schemaUp returns the Up block of every migration, in order.
func schemaUp(t *testing.T) string {
	t.Helper()

	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	var sb strings.Builder
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(migrationDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		up, _, found := strings.Cut(string(b), "-- +goose Down")
		if !found {
			t.Fatalf("%s: no goose Down marker", e.Name())
		}
		sb.WriteString(strings.TrimPrefix(up, "-- +goose Up"))
		sb.WriteString("\n")
	}
	return sb.String()
}
