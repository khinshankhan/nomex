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
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
)

// Defaults chosen to be polite rather than fast. The real safe rate per
// registry is unmeasured -- November's 18,752 timeouts recorded the wall, not
// the limit -- so these start conservative and are meant to be tuned.
const (
	DefaultRate    = 3.0 // queries per second
	DefaultWorkers = 4
	DefaultBatch   = 100

	// idleDelay is how long to wait when nothing is due. Long enough not to
	// spin, short enough that an on-demand request is picked up promptly.
	idleDelay = 30 * time.Second

	// failureWindow bounds how far back consecutive failures are counted for
	// backoff, so a domain that failed last year starts fresh.
	failureWindow = 7 * 24 * time.Hour
)

// Options configures a sweep.
type Options struct {
	// Rate is queries per second across all workers. Zero means DefaultRate.
	Rate float64

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
	rate := opts.Rate
	if rate <= 0 {
		rate = DefaultRate
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	batch := opts.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}

	// One limiter shared by every worker, so the rate is a property of the
	// process rather than of each goroutine.
	tick := time.Duration(float64(time.Second) / rate)
	limiter := time.NewTicker(tick)
	defer limiter.Stop()

	start := time.Now()
	var done int64

	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}

		domains, err := db.DueChecks(ctx, int64(batch))
		if err != nil {
			return done, fmt.Errorf("claim work: %w", err)
		}

		if len(domains) == 0 {
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

		n, err := round(ctx, db, checker, domains, limiter.C, workers, opts, &done, start, throttled)
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

// round checks one batch of domains across a worker pool.
func round(
	ctx context.Context,
	db *data.DB,
	checker check.Checker,
	domains []string,
	tick <-chan time.Time,
	workers int,
	opts Options,
	done *int64,
	start time.Time,
	throttled map[string]time.Time,
) (int64, error) {
	work := make(chan string)

	var (
		mu       sync.Mutex
		total    = *done
		firstErr error
	)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
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

				// Every query waits for the shared limiter, so adding workers
				// hides latency without raising the rate.
				select {
				case <-tick:
				case <-ctx.Done():
					return
				}

				res := checker.Check(ctx, domain)

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

				mu.Lock()
				if err != nil && firstErr == nil {
					// A write failure is ours, not the registry's: stop rather
					// than sweeping on with results going nowhere.
					firstErr = fmt.Errorf("store %s: %w", domain, err)
				}
				total++
				count := total
				mu.Unlock()

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
		}()
	}

feed:
	for _, d := range domains {
		if opts.Limit > 0 && total >= opts.Limit {
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
		return total, firstErr
	}
	return total, ctx.Err()
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
