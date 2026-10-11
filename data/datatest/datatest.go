// Package datatest opens throwaway databases for tests outside data.
//
// NOTE: data's own tests can't use it, importing it from there is a cycle.
package datatest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/khinshankhan/nomex/data"
)

// Open returns a DB in a temp dir with every migration's Up block applied.
func Open(t *testing.T) *data.DB {
	t.Helper()

	db, err := data.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})

	files, err := filepath.Glob(filepath.Join(migrationsDir(t), "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find migrations: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		up, _, found := strings.Cut(string(b), "-- +goose Down")
		if !found {
			t.Fatalf("%s: no goose Down marker", f)
		}
		if err := db.Exec(t.Context(), up); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return db
}

// migrationsDir finds migrations/ relative to this file, from any package.
func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate datatest source")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
