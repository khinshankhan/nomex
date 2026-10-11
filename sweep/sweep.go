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
	"sync/atomic"
	"time"

	"github.com/khinshankhan/jitter-go/v2"
	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
	"github.com/khinshankhan/nomex/platform/logx"
	"golang.org/x/sync/errgroup"
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

// sweeper holds what every pipeline and worker in a sweep shares.
type sweeper struct {
	db      *data.DB
	checker check.Checker
	opts    Options
	lims    *limiters
	start   time.Time

	// throttled is reloaded each round, between pipelines
	throttled *throttles

	// done is shared by every pipeline's workers
	done atomic.Int64
}

func newSweeper(db *data.DB, checker check.Checker, opts Options) *sweeper {
	opts = opts.withDefaults()
	return &sweeper{
		db:      db,
		checker: checker,
		opts:    opts,
		lims:    newLimiters(opts.Limits),
		start:   time.Now(),
	}
}

// limitReached reports whether Limit checks have been done.
func (s *sweeper) limitReached() bool {
	return s.opts.Limit > 0 && s.done.Load() >= s.opts.Limit
}

// Run sweeps until the context is cancelled, the limit is reached, or one round
// completes when Once is set.
func Run(ctx context.Context, db *data.DB, checker check.Checker, opts Options) (int64, error) {
	s := newSweeper(db, checker, opts)

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

	for {
		if err := ctx.Err(); err != nil {
			return s.done.Load(), err
		}

		if time.Since(lastPrune) > pruneEvery {
			lastPrune = time.Now()
			if _, err := db.PruneAttempts(ctx, pruneCutoff()); err != nil {
				log.Warn("pruning attempts", fields.Error(err))
			}
		}

		suffixes, err := db.DueSuffixes(ctx)
		if err != nil {
			return s.done.Load(), fmt.Errorf("claim work: %w", err)
		}

		if len(suffixes) == 0 {
			if s.opts.Once {
				return s.done.Load(), nil
			}
			if err := sleep(ctx, idleDelay); err != nil {
				return s.done.Load(), err
			}
			continue
		}

		// once per round, a stale answer costs at most one wasted query
		until, err := db.Throttled(ctx)
		if err != nil {
			return s.done.Load(), err
		}
		s.throttled = newThrottles(until)

		if err := s.sweepRound(ctx, suffixes); err != nil {
			return s.done.Load(), err
		}

		if s.opts.Once || s.limitReached() {
			return s.done.Load(), nil
		}
	}
}

// sweepRound runs one pipeline per suffix, concurrently, so each drains at its
// own registry's pace instead of sharing a batch with a slow one.
//
// One pipeline failing stops the rest, since a failed claim or write is on us.
func (s *sweeper) sweepRound(ctx context.Context, suffixes []sqlcgen.DueSuffixesRow) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, suf := range suffixes {
		g.Go(func() error {
			return s.drain(gctx, suf.Suffix)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

// drain keeps claiming batches for suffix until it runs dry, so a fast registry
// doesn't sit waiting on a slow one between rounds.
func (s *sweeper) drain(ctx context.Context, suffix string) error {
	for {
		if ctx.Err() != nil || s.limitReached() {
			return nil
		}

		domains, err := s.db.DueChecksForSuffix(ctx, sqlcgen.DueChecksForSuffixParams{
			Suffix: suffix,
			Limit:  int64(s.opts.Batch),
		})
		if err != nil {
			return fmt.Errorf("claim %s: %w", suffix, err)
		}
		if len(domains) == 0 {
			return nil
		}

		if err := s.round(ctx, domains); err != nil {
			return err
		}

		// Once means one batch per suffix, not one overall
		if s.opts.Once {
			return nil
		}
	}
}

// round checks one batch of domains across a worker pool. A failed write
// stops the batch, so the rest isn't checked only for its results to be thrown
// away.
func (s *sweeper) round(ctx context.Context, domains []string) error {
	work := make(chan string)

	g, gctx := errgroup.WithContext(ctx)
	for w := range s.opts.Workers {
		// separate RNG per worker so they don't all retry at the same instant
		backoff := newBackoff(int64(w) * 10_000)

		g.Go(func() error {
			for domain := range work {
				if gctx.Err() != nil {
					return nil
				}
				if err := s.checkOne(gctx, backoff, domain); err != nil {
					return err
				}
			}
			return nil
		})
	}

feed:
	for _, d := range domains {
		if s.limitReached() {
			break
		}
		select {
		case work <- d:
		case <-gctx.Done():
			break feed
		}
	}
	close(work)

	if err := g.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

// checkOne checks and stores one domain. An error means a write failed.
func (s *sweeper) checkOne(ctx context.Context, backoff jitter.Strategy, domain string) error {
	opts := s.opts

	// resolve the server first so a throttled registry is skipped without
	// spending a token or a query
	origin := ""
	if opts.Origins != nil {
		if o, err := opts.Origins.Origin(ctx, domain); err == nil {
			origin = o
		}
	}
	if until, blocked := s.throttled.active(origin, time.Now()); blocked {
		if opts.Progress != nil {
			opts.Progress(Stat{Domain: domain, Origin: origin, Skipped: true})
		}
		// push it past the throttle, otherwise it stays due and the next claim
		// hands back the same rows to skip again
		if err := s.db.DeferCheck(ctx, sqlcgen.DeferCheckParams{
			Domain:     domain,
			FreshUntil: sqltime.At(until),
		}); err != nil {
			return fmt.Errorf("defer %s: %w", domain, err)
		}
		return nil
	}

	// DNS first if configured, it can only prove "registered" so anything else
	// falls through to the real checker
	var res check.Result
	if opts.PreCheck != nil {
		// no Origin: the RDAP server wasn't asked, so its state shouldn't move
		if pre := opts.PreCheck.Check(ctx, domain); pre.Status == check.StatusRegistered {
			res = pre
		}
	}
	if res.Status == check.StatusUnchecked || res.Status == "" {
		res = checkWithRetry(ctx, s.checker, s.lims, backoff, domain, origin, opts.MaxAttempts)
	}

	failures := 0
	if res.Failed() {
		failures = recentFailures(ctx, s.db, domain)
	}

	err := s.db.StoreResult(ctx, res, failures)

	// per-origin state, so a 429 on one domain covers every domain on that
	// server
	nextTry, serr := s.db.RecordServer(ctx, res)
	if serr != nil && err == nil {
		err = serr
	}
	// apply it to the rest of this round too, not just the next reload
	s.throttled.extend(res.Origin, nextTry)

	count := s.done.Add(1)
	if opts.Progress != nil {
		opts.Progress(Stat{
			Domain:  domain,
			Origin:  res.Origin,
			Status:  res.Status,
			Err:     res.Err,
			Done:    count,
			Elapsed: time.Since(s.start),
		})
	}

	if err != nil {
		return fmt.Errorf("store %s: %w", domain, err)
	}
	return nil
}

// recentFailures counts failures in the backoff window. It returns zero on
// error so a failed lookup doesn't inflate the backoff.
func recentFailures(ctx context.Context, db *data.DB, domain string) int {
	n, err := db.CountRecentFailures(ctx, sqlcgen.CountRecentFailuresParams{
		Domain: domain,
		Since:  sqltime.Text(time.Now().Add(-failureWindow)),
	})
	if err != nil {
		logx.Default().Named("sweep").Warn("counting failures",
			fields.String("domain", domain),
			fields.Error(err))
		return 0
	}
	return int(n)
}

// pruneCutoff is the retention boundary.
func pruneCutoff() string {
	return sqltime.Text(time.Now().Add(-AttemptRetention))
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
	opts.Limit = 0
	s := newSweeper(db, checker, opts)

	until, err := db.Throttled(ctx)
	if err != nil {
		return 0, err
	}
	s.throttled = newThrottles(until)

	err = s.round(ctx, domains)
	return s.done.Load(), err
}
