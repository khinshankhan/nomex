package data

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/data/sqlcgen"
)

// The pragmas are the reason this package exists rather than callers holding a
// *sql.DB, so they are worth asserting rather than trusting the DSN.
func TestOpenAppliesPragmas(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"}, // NORMAL
	}

	for _, tt := range tests {
		var got string
		if err := db.sql.QueryRowContext(ctx, "PRAGMA "+tt.pragma).Scan(&got); err != nil {
			t.Errorf("PRAGMA %s: %v", tt.pragma, err)
			continue
		}
		if got != tt.want {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
}

func TestOpenRejectsUnwritablePath(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "nonexistent-dir", "x.db"))
	if err == nil {
		t.Error("Open on a path with no parent directory returned nil error")
	}
}

func TestTxCommits(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	err := db.Tx(ctx, func(q *sqlcgen.Queries) error {
		return insert(ctx, q, "committed.dev")
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}

	if _, err := db.GetCheck(ctx, "committed.dev"); err != nil {
		t.Errorf("row missing after commit: %v", err)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	sentinel := errNotDone{}
	err := db.Tx(ctx, func(q *sqlcgen.Queries) error {
		if err := insert(ctx, q, "rolled-back.dev"); err != nil {
			return err
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("Tx error = %v, want the sentinel", err)
	}

	if _, err := db.GetCheck(ctx, "rolled-back.dev"); err == nil {
		t.Error("row survived a rolled-back transaction")
	}
}

type errNotDone struct{}

func (errNotDone) Error() string { return "not done" }

func insert(ctx context.Context, q *sqlcgen.Queries, domain string) error {
	return q.UpsertCheck(ctx, sqlcgen.UpsertCheckParams{
		Domain:     domain,
		Status:     "unchecked",
		FreshUntil: time.Now().Add(-time.Hour),
	})
}
