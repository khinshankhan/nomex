package gather

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

func openDB(t *testing.T) *data.DB {
	t.Helper()

	db, err := data.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	entries, err := os.ReadDir("../migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("../migrations", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		up, _, _ := strings.Cut(string(b), "-- +goose Down")
		if err := db.Exec(t.Context(), up); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
	}
	return db
}

func count(t *testing.T, db *data.DB) int64 {
	t.Helper()
	n, err := db.CountChecks(t.Context())
	if err != nil {
		t.Fatalf("CountChecks: %v", err)
	}
	return n
}

// Widening a space should only add the new candidates.
func TestSeedIsIncremental(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	// small alphabet keeps this to a few hundred rows
	two := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2, Alphabet: "abcdefgh"}
	three := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 3, Alphabet: "abcdefgh"}

	// 8 + 8^2
	n, err := Seed(ctx, db, two, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if n != 72 {
		t.Errorf("first seed inserted %d, want 72", n)
	}

	n, err = Seed(ctx, db, three, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("widened seed: %v", err)
	}
	// 8^3, only the new labels
	if n != 512 {
		t.Errorf("widened seed inserted %d, want 512", n)
	}

	n, err = Seed(ctx, db, three, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("repeat seed: %v", err)
	}
	if n != 0 {
		t.Errorf("repeat seed inserted %d, want 0", n)
	}

	if got := count(t, db); got != 584 {
		t.Errorf("total rows = %d, want 584", got)
	}
}

// A checked row should survive re-seeding untouched.
func TestSeedPreservesExistingRows(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2}
	if _, err := Seed(ctx, db, spec, SeedOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fresh := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	if err := db.UpsertCheck(ctx, sqlcgen.UpsertCheckParams{
		Domain:     "ab.dev",
		Status:     "not_found",
		FreshUntil: sqltime.At(fresh),
		Priority:   100,
	}); err != nil {
		t.Fatalf("UpsertCheck: %v", err)
	}

	if _, err := Seed(ctx, db, spec, SeedOptions{}); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	got, err := db.GetCheck(ctx, "ab.dev")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}
	if got.Status != "not_found" {
		t.Errorf("status = %q, want not_found (re-seed reset it)", got.Status)
	}
	if got.Priority != 100 {
		t.Errorf("priority = %d, want 100 (re-seed reset it)", got.Priority)
	}
	if !got.FreshUntil.Equal(fresh) {
		t.Errorf("fresh_until = %v, want %v (re-seed reset it)", got.FreshUntil, fresh)
	}
}

// Seeded rows should be due right away so the sweep picks them up.
func TestSeedRowsAreDue(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 1}
	if _, err := Seed(ctx, db, spec, SeedOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	due, err := db.DueChecksForSuffix(ctx, sqlcgen.DueChecksForSuffixParams{Suffix: "dev", Limit: 100})
	if err != nil {
		t.Fatalf("DueChecksForSuffix: %v", err)
	}
	if len(due) != 26 {
		t.Errorf("%d rows due, want all 26", len(due))
	}
}

// Cancelling keeps what was committed, so the next run resumes.
func TestSeedCancellationKeepsProgress(t *testing.T) {
	db := openDB(t)
	ctx, cancel := context.WithCancel(context.Background())

	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 3, Alphabet: "abcdefgh"}

	var inserted int64
	_, err := Seed(ctx, db, spec, SeedOptions{
		Batch: 100,
		Progress: func(p Progress) {
			inserted = p.Inserted
			if p.Seen >= 300 {
				cancel()
			}
		},
	})
	if err == nil {
		t.Fatal("Seed returned nil error after cancellation")
	}

	if got := count(t, db); got != inserted {
		t.Errorf("rows in db = %d, want %d committed before cancellation", got, inserted)
	}
	if inserted == 0 {
		t.Error("cancellation discarded every batch")
	}

	// a fresh run finishes the job
	n, err := Seed(context.Background(), db, spec, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("resumed seed: %v", err)
	}
	if got := count(t, db); got != 584 {
		t.Errorf("after resume: %d rows, want 584 (added %d)", got, n)
	}
}

// The last partial batch is written after the loop, and its failure has to
// reach the caller, or gather reports rows it never wrote.
func TestSeedReportsWriteErrors(t *testing.T) {
	db := openDB(t)
	db.Close()

	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 1}
	if _, err := Seed(t.Context(), db, spec, SeedOptions{Batch: 100}); err == nil {
		t.Error("Seed into a closed database returned nil")
	}
}
