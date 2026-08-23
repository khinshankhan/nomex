package gather

import (
	"context"
	"fmt"
	"time"

	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
)

// DefaultBatch is how many rows are written per transaction. Large enough that
// per-commit overhead disappears, small enough that an interrupted run loses
// little and holds the write lock briefly.
const DefaultBatch = 10_000

// SeedOptions configures a seeding run.
type SeedOptions struct {
	// Priority for seeded rows. The sweep band is 0; use a negative value for
	// bulk fill that should never outrank ordinary background work.
	Priority int64

	// Batch is rows per transaction. Zero means DefaultBatch.
	Batch int

	// Progress, if set, is called after each batch commits.
	Progress func(Progress)
}

// Progress reports how far a seeding run has got.
type Progress struct {
	// Seen is candidates generated so far, Inserted those that were new.
	// They diverge when re-seeding over existing rows, which is the normal
	// case when widening a space.
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

// Seed writes every candidate in spec that is not already present.
//
// Existing rows are left alone: a domain already checked keeps its status,
// freshness and priority, so widening a space (2 characters, then 3) re-walks
// the earlier candidates cheaply rather than resetting them.
//
// Returns the number of rows actually inserted.
func Seed(ctx context.Context, db *data.DB, spec Spec, opts SeedOptions) (int64, error) {
	if err := spec.Validate(); err != nil {
		return 0, err
	}

	batch := opts.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}

	// Seeded rows are due immediately: the sweep finds work by looking for
	// fresh_until in the past, so an unchecked row has to be already stale.
	due := time.Now().Add(-time.Minute)

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
			// Everything committed so far stays; the next run picks up where
			// this one stopped because the same sequence is regenerated and
			// already-present rows are skipped.
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

// insertBatch writes one transaction's worth, returning how many were new.
//
// The count comes from rows-affected per statement -- 1 for an inserted row, 0
// for one OR IGNORE skipped -- rather than counting the table before and after.
// At 10k rows per batch that saved two queries per batch, which is 7,400 round
// trips over the full 37M candidate space.
func insertBatch(ctx context.Context, db *data.DB, domains []string, due time.Time, priority int64) (int64, error) {
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
		// The transaction rolled back, so nothing in this batch landed.
		return 0, err
	}
	return inserted, nil
}
