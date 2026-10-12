package gather

import (
	"context"
	"fmt"
	"time"

	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// DefaultBatch is rows per transaction.
const DefaultBatch = 10_000

// SeedOptions configures a seeding run.
type SeedOptions struct {
	// Priority for seeded rows. The sweep uses 0, negative sorts below it.
	Priority int64

	// Batch is rows per transaction. Zero means DefaultBatch.
	Batch int

	// Progress, if set, is called after each batch commits.
	Progress func(Progress)
}

// Progress reports how far a seeding run has got.
type Progress struct {
	// Seen is candidates generated so far, Inserted the ones that were new.
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
// how many were new. Existing rows are left alone.
func Seed(ctx context.Context, db *data.DB, spec Spec, opts SeedOptions) (int64, error) {
	if err := spec.Validate(); err != nil {
		return 0, err
	}

	batch := opts.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}

	// due now
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
			// committed batches stay, a rerun skips them
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

// insertBatch writes one transaction's worth and returns how many were new.
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
		return 0, err
	}
	return inserted, nil
}
