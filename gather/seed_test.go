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

// Widening a space must add only the new candidates.
func TestSeedIsIncremental(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	// A small alphabet keeps this in the hundreds of rows: the property under
	// test is that widening adds only new candidates, which does not need the
	// full 26-letter space to demonstrate.
	two := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2, Alphabet: "abcdefgh"}
	three := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 3, Alphabet: "abcdefgh"}

	n, err := Seed(ctx, db, two, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if n != mustCount(t, two) {
		t.Errorf("first seed inserted %d, want %d", n, mustCount(t, two))
	}

	n, err = Seed(ctx, db, three, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("widened seed: %v", err)
	}
	if want := mustCount(t, three) - mustCount(t, two); n != want {
		t.Errorf("widened seed inserted %d, want %d (only the new labels)", n, want)
	}

	n, err = Seed(ctx, db, three, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("repeat seed: %v", err)
	}
	if n != 0 {
		t.Errorf("repeat seed inserted %d, want 0", n)
	}

	if got := count(t, db); got != mustCount(t, three) {
		t.Errorf("total rows = %d, want %d", got, mustCount(t, three))
	}
}

// A row that has been checked must survive re-seeding untouched, or widening a
// space would silently discard results.
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
		FreshUntil: fresh,
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

// Seeded rows must be immediately due, or the sweep would never pick them up.
func TestSeedRowsAreDue(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 1}
	if _, err := Seed(ctx, db, spec, SeedOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	due, err := db.DueChecks(ctx, 100)
	if err != nil {
		t.Fatalf("DueChecks: %v", err)
	}
	if int64(len(due)) != mustCount(t, spec) {
		t.Errorf("%d rows due, want all %d", len(due), mustCount(t, spec))
	}
}

// Cancellation keeps what was committed, so the next run resumes cheaply.
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

	// A fresh run finishes the job.
	n, err := Seed(context.Background(), db, spec, SeedOptions{Batch: 100})
	if err != nil {
		t.Fatalf("resumed seed: %v", err)
	}
	if got := count(t, db); got != mustCount(t, spec) {
		t.Errorf("after resume: %d rows, want %d (added %d)", got, mustCount(t, spec), n)
	}
}

func TestSeedRejectsBadSpec(t *testing.T) {
	db := openDB(t)
	if _, err := Seed(context.Background(), db, Spec{}, SeedOptions{}); err == nil {
		t.Error("Seed accepted a spec with no TLDs")
	}
}
