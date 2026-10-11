// Package sweep works through the queue: pull domains whose answers have gone
// stale, check them, and write the results back.
//
// It never really finishes. 37 million candidates at a few queries a second is
// months, and early rows expire before the late ones are reached.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
	"github.com/khinshankhan/nomex/platform/logx"
)

// Defaults lean polite over fast. Google's .dev registry returned 429 after 10
// queries at 2/sec, so DefaultLimits starts at one per second.
const (
	DefaultWorkers = 4
	DefaultBatch   = 100

	// idleDelay is how long to wait when nothing is due.
	idleDelay = 30 * time.Second

	// failureWindow bounds how far back consecutive failures are counted for
	// backoff, so a domain that failed last year starts fresh.
	failureWindow = 7 * 24 * time.Hour

	// AttemptRetention is how long a failure is kept. Attempts are only for
	// debugging, and a throttled registry piles them up fast (a live run
	// recorded 6 from 16 checks).
	AttemptRetention = 30 * 24 * time.Hour

	// pruneEvery is how often old attempts get pruned.
	pruneEvery = time.Hour
)

// Options configures a sweep.
type Options struct {
	// Limits is the rate budget per server. Each origin gets its own so a
	// slow registry doesn't hold up a fast one.
	Limits Limits

	// MaxAttempts bounds retries per domain within one sweep. Zero means
	// DefaultMaxAttempts.
	MaxAttempts int

	// Workers is how many checks run concurrently. More workers don't raise
	// the rate, they just hide latency.
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
	// it finds a domain registered. Meant for DNS, which is only worth it on
	// dense suffixes (see dnschecker). Its failures are ignored since the real
	// checker runs next anyway.
	PreCheck check.Checker

	// Origins, if set, maps a domain to its RDAP server so throttled servers
	// can be skipped without spending a query.
	Origins OriginResolver
}

// withDefaults fills in zero values.
func (o Options) withDefaults() Options {
	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}
	if o.Batch <= 0 {
		o.Batch = DefaultBatch
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	return o
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

	// Skipped means the domain's registry is throttled and no query was sent.
	// It doesn't count against Limit.
	Skipped bool
}

// Run sweeps until the context is cancelled, the limit is reached, or one round
// completes when Once is set.
func Run(ctx context.Context, db *data.DB, checker check.Checker, opts Options) (int64, error) {
	opts = opts.withDefaults()

	// one limiter per origin, created on first use
	lims := newLimiters(opts.Limits)

	// prune at startup, then hourly, so retention just happens whenever the
	// sweep runs
	log := logx.Default().Named("sweep")

	lastPrune := time.Now()
	if n, err := db.PruneAttempts(ctx, pruneCutoff()); err != nil {
		log.Warn("pruning attempts", fields.Error(err))
	} else if n > 0 {
		log.Info("pruned old attempts",
			fields.Int64("n", n),
			fields.Duration("retention", AttemptRetention))
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
				log.Warn("pruning attempts", fields.Error(err))
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

		// once per round, a stale answer costs at most one wasted query
		until, err := db.Throttled(ctx)
		if err != nil {
			return done, err
		}
		throttled := newThrottles(until)

		n, err := sweepRound(ctx, db, checker, suffixes, lims, opts, done, start, throttled)
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

// sweepRound runs one pipeline per suffix, concurrently, so each drains at its
// own registry's pace instead of sharing a batch with a slow one.
func sweepRound(
	ctx context.Context,
	db *data.DB,
	checker check.Checker,
	suffixes []sqlcgen.DueSuffixesRow,
	lims *limiters,
	opts Options,
	done int64,
	start time.Time,
	throttled *throttles,
) (int64, error) {
	// atomic since pipelines run concurrently and share the running total
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

			// keep claiming until this suffix runs dry, so a fast registry
			// doesn't sit waiting on a slow one between rounds
			for {
				if ctx.Err() != nil {
					return
				}
				if opts.Limit > 0 && total.Load() >= opts.Limit {
					return
				}

				domains, err := db.DueChecksForSuffix(ctx, sqlcgen.DueChecksForSuffixParams{
					Suffix: suffix,
					Limit:  int64(opts.Batch),
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

				if err := round(ctx, db, checker, domains, lims, opts, &total, start, throttled, &mu); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}

				// Once means one batch per suffix, not one overall
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
	opts Options,
	total *atomic.Int64,
	start time.Time,
	throttled *throttles,
	mu *sync.Mutex,
) error {
	work := make(chan string)

	var firstErr error
	// set on a store failure so the rest of the batch isn't checked only for
	// its results to be thrown away
	var failed atomic.Bool

	var wg sync.WaitGroup
	for w := range opts.Workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// separate RNG per worker so they don't all retry at the same
			// instant
			backoff := newBackoff(int64(workerID) * 10_000)

			for domain := range work {
				if failed.Load() {
					continue
				}

				// resolve the server first so a throttled registry is skipped
				// without spending a token or a query
				origin := ""
				if opts.Origins != nil {
					if o, err := opts.Origins.Origin(ctx, domain); err == nil {
						origin = o
					}
				}
				if until, blocked := throttled.active(origin, time.Now()); blocked {
					// push it past the throttle, otherwise it stays due and the
					// next claim hands back the same rows to skip again
					if err := db.DeferCheck(ctx, sqlcgen.DeferCheckParams{
						Domain:     domain,
						FreshUntil: sqltime.At(until),
					}); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("defer %s: %w", domain, err)
						}
						mu.Unlock()
						failed.Store(true)
					}
					if opts.Progress != nil {
						opts.Progress(Stat{Domain: domain, Origin: origin, Skipped: true})
					}
					continue
				}

				// DNS first if configured, it can only prove "registered" so
				// anything else falls through to the real checker
				var res check.Result
				if opts.PreCheck != nil {
					// no Origin: the RDAP server wasn't asked, so its state
					// shouldn't move
					if pre := opts.PreCheck.Check(ctx, domain); pre.Status == check.StatusRegistered {
						res = pre
					}
				}
				if res.Status == check.StatusUnchecked || res.Status == "" {
					res = checkWithRetry(ctx, checker, lims, backoff, domain, origin, opts.MaxAttempts)
				}

				failures := 0
				if res.Failed() {
					failures = recentFailures(ctx, db, domain)
				}

				err := db.StoreResult(ctx, res, failures)

				// per-origin state, so a 429 on one domain covers every domain
				// on that server
				nextTry, serr := db.RecordServer(ctx, res)
				if serr != nil && err == nil {
					err = serr
				}
				// apply it to the rest of this round too, not just the next reload
				throttled.extend(res.Origin, nextTry)

				if err != nil {
					mu.Lock()
					if firstErr == nil {
						// a write failure is on us, stop instead of checking
						// domains whose results can't be saved
						firstErr = fmt.Errorf("store %s: %w", domain, err)
					}
					mu.Unlock()
					failed.Store(true)
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
		if failed.Load() {
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

// recentFailures counts failures in the backoff window. It returns zero on
// error so a failed lookup doesn't inflate the backoff.
func recentFailures(ctx context.Context, db *data.DB, domain string) int {
	n, err := db.CountRecentFailures(ctx, sqlcgen.CountRecentFailuresParams{
		Domain: domain,
		// text since the query casts it for datetime(), and RFC 3339 matches
		// what the driver writes
		Since: time.Now().Add(-failureWindow).UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		logx.Default().Named("sweep").Warn("counting failures",
			fields.String("domain", domain),
			fields.Error(err))
		return 0
	}
	return int(n)
}

// pruneCutoff formats the retention boundary as RFC3339Nano, which matches what
// the driver writes since PruneAttempts compares it as text.
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

// CheckDomains checks exactly the given domains, once each, through the same
// pipeline as Run (rate limits, retries, pre-check, throttle skipping) and
// stores the results the same way.
//
// Every domain needs a row already, so seed them first. Limit, Once and Batch
// are ignored.
func CheckDomains(ctx context.Context, db *data.DB, checker check.Checker, domains []string, opts Options) (int64, error) {
	opts = opts.withDefaults()
	opts.Limit = 0

	until, err := db.Throttled(ctx)
	if err != nil {
		return 0, err
	}

	var (
		total atomic.Int64
		mu    sync.Mutex
	)
	err = round(ctx, db, checker, domains, newLimiters(opts.Limits), opts, &total, time.Now(), newThrottles(until), &mu)
	return total.Load(), err
}
