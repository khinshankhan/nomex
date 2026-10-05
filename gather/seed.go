package gather

import (
	"context"
	"fmt"
	"time"

	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// DefaultBatch is how many rows are written per transaction, big enough to
// amortize commits and small enough to not hold the write lock for long.
const DefaultBatch = 10_000

// SeedOptions configures a seeding run.
type SeedOptions struct {
	// Priority for seeded rows. The sweep uses 0, so go negative for bulk
	// fill that shouldn't outrank regular work.
	Priority int64

	// Batch is rows per transaction. Zero means DefaultBatch.
	Batch int

	// Progress, if set, is called after each batch commits.
	Progress func(Progress)
}

// Progress reports how far a seeding run has got.
type Progress struct {
	// Seen is candidates generated so far and Inserted is the ones that were
	// new. They differ when re-seeding over existing rows.
	Seen     int64
	Inserted int64
	Total    int64
	Elapsed  time.Duration
}

// Rate returns candidates per second.
func (p Progress) Rate() float64 {
	if p.Elapsed <= 0 {
		return 0
	}
	return float64(p.Seen) / p.Elapsed.Seconds()
}

// Remaining estimates time left at the current rate.
func (p Progress) Remaining() time.Duration {
	rate := p.Rate()
	if rate <= 0 || p.Seen >= p.Total {
		return 0
	}
	return time.Duration(float64(p.Total-p.Seen)/rate) * time.Second
}

// Seed writes every candidate in spec that isn't already present and returns
// how many rows were inserted. Existing rows keep their status, freshness and
// priority, so widening a space (2 characters, then 3) doesn't reset anything.
func Seed(ctx context.Context, db *data.DB, spec Spec, opts SeedOptions) (int64, error) {
	if err := spec.Validate(); err != nil {
		return 0, err
	}

	batch := opts.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}

	// due immediately, the sweep looks for fresh_until in the past
	due := sqltime.At(time.Now().Add(-time.Minute))

	total, err := spec.Count()
	if err != nil {
		return 0, err
	}

	start := time.Now()
	var seen, inserted int64

	pending := make([]string, 0, batch)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}

		n, err := insertBatch(ctx, db, pending, due, opts.Priority)
		if err != nil {
			return err
		}
		inserted += n
		pending = pending[:0]

		if opts.Progress != nil {
			opts.Progress(Progress{
				Seen:     seen,
				Inserted: inserted,
				Total:    total,
				Elapsed:  time.Since(start),
			})
		}
		return nil
	}

	for domain := range spec.All() {
		if err := ctx.Err(); err != nil {
			// committed batches stay, and a rerun skips what already landed
			return inserted, err
		}

		pending = append(pending, domain)
		seen++

		if len(pending) >= batch {
			if err := flush(); err != nil {
				return inserted, err
			}
		}
	}

	if err := flush(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// insertBatch writes one transaction's worth and returns how many were new,
// using rows affected per statement (1 inserted, 0 skipped by OR IGNORE)
// instead of counting the table before and after.
func insertBatch(ctx context.Context, db *data.DB, domains []string, due sqltime.UTC, priority int64) (int64, error) {
	var inserted int64

	err := db.Tx(ctx, func(q *sqlcgen.Queries) error {
		for _, d := range domains {
			n, err := q.SeedCheck(ctx, sqlcgen.SeedCheckParams{
				Domain:     d,
				FreshUntil: due,
				Priority:   priority,
			})
			if err != nil {
				return fmt.Errorf("seed %s: %w", d, err)
			}
			inserted += n
		}
		return nil
	})
	if err != nil {
		// rolled back, nothing in this batch landed
		return 0, err
	}
	return inserted, nil
}
