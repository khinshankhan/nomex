// Package sweep drains the work queue: pull domains whose answers have gone
// stale, check them, write the results back.
//
// There is no finish line. The candidate space is far larger than the rate
// limit can drain -- 37 million rows against a few queries a second is months
// -- and TTLs expire early rows before late ones are reached. "Caught up" and
// "working through the backlog" are the same state.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
)

// Defaults chosen to be polite rather than fast. Google's .dev registry
// returned 429 after 10 queries at 2/sec, so the per-registry budget in
// DefaultLimits starts at one per second.
const (
	DefaultWorkers = 4
	DefaultBatch   = 100

	// idleDelay is how long to wait when nothing is due. Long enough not to
	// spin, short enough that an on-demand request is picked up promptly.
	idleDelay = 30 * time.Second

	// failureWindow bounds how far back consecutive failures are counted for
	// backoff, so a domain that failed last year starts fresh.
	failureWindow = 7 * 24 * time.Hour

	// AttemptRetention is how long a failure is kept. Attempts grows by one
	// row per transient failure forever, and a throttled registry produces
	// them faster than results -- a live run recorded 6 from 16 checks. They
	// are diagnostic, so anything older than the backoff window has already
	// served its purpose.
	AttemptRetention = 30 * 24 * time.Hour

	// pruneEvery bounds how often retention runs. It is a single indexed
	// DELETE, but there is no reason to run it per round.
	pruneEvery = time.Hour
)

// Options configures a sweep.
type Options struct {
	// Limits is the per-server rate budget. Each origin gets its own, so a
	// slow registry does not hold up a fast one.
	Limits Limits

	// MaxAttempts bounds retries per domain within one sweep. Zero means
	// DefaultMaxAttempts.
	MaxAttempts int

	// Workers is how many checks run concurrently. More workers do not exceed
	// Rate; they hide latency, since a query spends most of its time waiting.
	Workers int

	// Batch is how many due domains to claim per round.
	Batch int

	// Limit stops after this many checks. Zero runs until the context ends.
	Limit int64

	// Once returns after a single round, even if more work is due.
	Once bool

	// Progress, if set, is called after each result is stored.
	Progress func(Stat)

	// PreCheck, if set, runs before the main checker and short-circuits when
	// it establishes a status. Intended for DNS, which can prove a domain is
	// registered without spending an RDAP query.
	//
	// It pays in proportion to how registered a suffix is -- measured here,
	// .com and .net at 1-2 characters are 100% registered and .dev is 5% -- so
	// it is worth enabling for dense suffixes and a waste for sparse ones.
	// A pre-check failure is ignored rather than recorded: it established
	// nothing, and the real checker is about to run anyway.
	PreCheck check.Checker

	// Origins, if set, maps a domain to the RDAP server that would serve it,
	// so a throttled registry can be skipped before a query is spent on it.
	// Without it the sweep still runs, it just cannot avoid known-bad servers.
	Origins OriginResolver
}

// OriginResolver reports which server serves a domain, without querying it.
type OriginResolver interface {
	Origin(ctx context.Context, domain string) (string, error)
}

// Stat reports one completed check.
type Stat struct {
	Domain  string
	Origin  string
	Status  check.Status
	Err     error
	Done    int64
	Elapsed time.Duration

	// Skipped reports that the domain's registry is throttled, so no query
	// was sent. Skipped work is not counted against Limit.
	Skipped bool
}

// Run sweeps until the context is cancelled, the limit is reached, or -- with
// Once -- one round completes.
func Run(ctx context.Context, db *data.DB, checker check.Checker, opts Options) (int64, error) {
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	batch := opts.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}

	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}

	// One limiter per origin, created on first use. A single global rate would
	// make every registry wait for the slowest.
	lims := newLimiters(opts.Limits)

	// Prune once at startup, then hourly. Doing it here rather than in a
	// separate command means retention happens for anyone who runs the sweep,
	// instead of being a chore nobody remembers.
	lastPrune := time.Now()
	if n, err := db.PruneAttempts(ctx, pruneCutoff()); err != nil {
		log.Printf("[sweep] pruning attempts: %v", err)
	} else if n > 0 {
		log.Printf("[sweep] pruned %d attempts older than %s", n, AttemptRetention)
	}

	start := time.Now()
	var done int64

	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}

		if time.Since(lastPrune) > pruneEvery {
			lastPrune = time.Now()
			if _, err := db.PruneAttempts(ctx, pruneCutoff()); err != nil {
				log.Printf("[sweep] pruning attempts: %v", err)
			}
		}

		suffixes, err := db.DueSuffixes(ctx)
		if err != nil {
			return done, fmt.Errorf("claim work: %w", err)
		}

		if len(suffixes) == 0 {
			if opts.Once {
				return done, nil
			}
			if err := sleep(ctx, idleDelay); err != nil {
				return done, err
			}
			continue
		}

		// Once per round: a handful of rows, and a stale answer costs at most
		// one wasted query.
		throttled, err := db.Throttled(ctx)
		if err != nil {
			return done, err
		}

		n, err := sweepRound(ctx, db, checker, suffixes, lims, attempts, workers, opts, done, start, throttled)
		done = n
		if err != nil {
			return done, err
		}

		if opts.Once {
			return done, nil
		}
		if opts.Limit > 0 && done >= opts.Limit {
			return done, nil
		}
	}
}

// sweepRound runs one pipeline per suffix, concurrently.
//
// Each suffix drains at its own registry's pace: the per-origin limiters do the
// pacing, and a slow registry no longer holds up a fast one because they are
// not sharing a batch.
func sweepRound(
	ctx context.Context,
	db *data.DB,
	checker check.Checker,
	suffixes []sqlcgen.DueSuffixesRow,
	lims *limiters,
	attempts int,
	workers int,
	opts Options,
	done int64,
	start time.Time,
	throttled map[string]time.Time,
) (int64, error) {
	// Atomic rather than a copied int: pipelines run concurrently, and each
	// taking its own copy of the running total means they overwrite one
	// another's progress instead of accumulating.
	var total atomic.Int64
	total.Store(done)

	var (
		mu       sync.Mutex
		firstErr error
	)

	var wg sync.WaitGroup
	for _, suf := range suffixes {
		wg.Add(1)
		go func(suffix string) {
			defer wg.Done()

			batch := opts.Batch
			if batch <= 0 {
				batch = DefaultBatch
			}

			// Each suffix keeps claiming until it runs dry. Rejoining at a
			// round boundary would pace every registry to the slowest: a fast
			// one finishes its batch in seconds and then waits.
			for {
				if ctx.Err() != nil {
					return
				}
				if opts.Limit > 0 && total.Load() >= opts.Limit {
					return
				}

				domains, err := db.DueChecksForSuffix(ctx, sqlcgen.DueChecksForSuffixParams{
					Suffix: suffix,
					Limit:  int64(batch),
				})
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("claim %s: %w", suffix, err)
					}
					mu.Unlock()
					return
				}
				if len(domains) == 0 {
					return
				}

				if err := round(ctx, db, checker, domains, lims, attempts, workers, opts, &total, start, throttled, &mu); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}

				// Once means one batch per suffix, not one batch overall.
				if opts.Once {
					return
				}
			}
		}(suf.Suffix)
	}
	wg.Wait()

	if firstErr != nil {
		return total.Load(), firstErr
	}
	return total.Load(), ctx.Err()
}

// round checks one batch of domains across a worker pool.
func round(
	ctx context.Context,
	db *data.DB,
	checker check.Checker,
	domains []string,
	lims *limiters,
	attempts int,
	workers int,
	opts Options,
	total *atomic.Int64,
	start time.Time,
	throttled map[string]time.Time,
	mu *sync.Mutex,
) error {
	work := make(chan string)

	var firstErr error

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Its own RNG, seeded apart: a shared one has every worker retry
			// at the same instant, which arrives at the server as a stampede.
			backoff := newBackoff(int64(workerID) * 10_000)

			for domain := range work {
				// Resolve the server first: a throttled registry is skipped
				// without spending a rate-limiter tick or a query on it.
				origin := ""
				if opts.Origins != nil {
					if o, err := opts.Origins.Origin(ctx, domain); err == nil {
						origin = o
					}
				}
				mu.Lock()
				until, blocked := throttled[origin]
				mu.Unlock()
				if blocked && origin != "" && time.Now().Before(until) {
					if opts.Progress != nil {
						opts.Progress(Stat{Domain: domain, Origin: origin, Skipped: true})
					}
					continue
				}

				// DNS first, when configured. It can only establish the
				// positive: anything else falls through to the real checker.
				var res check.Result
				if opts.PreCheck != nil {
					if pre := opts.PreCheck.Check(ctx, domain); pre.Status == check.StatusRegistered {
						pre.Origin = origin
						res = pre
					}
				}
				if res.Status == check.StatusUnchecked || res.Status == "" {
					res = checkWithRetry(ctx, checker, lims, backoff, domain, origin, attempts)
				}

				failures := 0
				if res.Failed() {
					failures = recentFailures(ctx, db, domain)
				}

				err := db.StoreResult(ctx, res, failures)

				// Per-origin state, so a 429 aimed at one domain protects
				// every other domain on that server.
				nextTry, serr := db.RecordServer(ctx, res)
				if serr != nil && err == nil {
					err = serr
				}
				if !nextTry.IsZero() && res.Origin != "" {
					// Honour it for the rest of this round too. Waiting for
					// the next reload would let the whole batch through.
					mu.Lock()
					if cur, ok := throttled[res.Origin]; !ok || nextTry.After(cur) {
						throttled[res.Origin] = nextTry
					}
					mu.Unlock()
				}

				if err != nil {
					mu.Lock()
					if firstErr == nil {
						// A write failure is ours, not the registry's: stop
						// rather than sweeping on with results going nowhere.
						firstErr = fmt.Errorf("store %s: %w", domain, err)
					}
					mu.Unlock()
				}
				count := total.Add(1)

				if opts.Progress != nil {
					opts.Progress(Stat{
						Domain:  domain,
						Origin:  res.Origin,
						Status:  res.Status,
						Err:     res.Err,
						Done:    count,
						Elapsed: time.Since(start),
					})
				}
			}
		}(w)
	}

feed:
	for _, d := range domains {
		if opts.Limit > 0 && total.Load() >= opts.Limit {
			break
		}
		select {
		case work <- d:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// recentFailures counts failures in the backoff window. A count of zero on
// error is deliberate: a failed lookup should not inflate the backoff.
func recentFailures(ctx context.Context, db *data.DB, domain string) int {
	n, err := db.CountRecentFailures(ctx, sqlcgen.CountRecentFailuresParams{
		Domain: domain,
		// Text rather than time.Time: the query casts it for datetime(), and
		// RFC 3339 is what the driver writes, so the formats match.
		Since: time.Now().Add(-failureWindow).UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		log.Printf("[sweep] counting failures for %s: %v", domain, err)
		return 0
	}
	return int(n)
}

// pruneCutoff formats the retention boundary. PruneAttempts casts its argument
// to TEXT for datetime(), and RFC3339Nano is the format the driver writes.
func pruneCutoff() string {
	return time.Now().Add(-AttemptRetention).UTC().Format(time.RFC3339Nano)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancelled reports whether err is just the sweep stopping on request.
func Cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
