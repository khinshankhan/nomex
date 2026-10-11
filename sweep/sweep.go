// Package sweep checks domains whose answers have gone stale and stores the
// results.
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

const (
	DefaultWorkers = 4
	DefaultBatch   = 100

	// how long to wait when nothing is due
	idleDelay = 30 * time.Second

	// failures older than this don't count toward backoff
	failureWindow = 7 * 24 * time.Hour

	// AttemptRetention is how long failed attempts are kept.
	AttemptRetention = 30 * 24 * time.Hour

	pruneEvery = time.Hour
)

// Options configures a sweep.
type Options struct {
	// Limits is the rate budget for each server.
	Limits Limits

	// MaxAttempts bounds tries per domain. Zero means DefaultMaxAttempts.
	MaxAttempts int

	// Workers is how many checks run at once per suffix. It hides latency, it
	// doesn't raise the rate.
	Workers int

	// Batch is how many due domains to claim at a time.
	Batch int

	// Limit stops after this many checks. Zero runs until the context ends.
	Limit int64

	// Once returns after a single round, even if more work is due.
	Once bool

	// Progress, if set, is called after each result is stored.
	Progress func(Stat)

	// PreCheck, if set, runs first and skips the main checker when it finds a
	// domain registered. Meant for DNS.
	PreCheck check.Checker

	// Origins, if set, maps a domain to its server so throttled servers are
	// skipped without a query.
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

	// Skipped means the server was throttled and nothing was sent. It doesn't
	// count against Limit.
	Skipped bool
}

// sweeper holds what every pipeline and worker in a sweep shares.
type sweeper struct {
	db      *data.DB
	checker check.Checker
	opts    Options
	lims    *limiters
	start   time.Time

	// reloaded each round
	throttled *throttles

	done atomic.Int64

	// checks started, so Limit holds with workers mid-check
	claimed atomic.Int64
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

// limitReached reports whether Limit checks have been started.
func (s *sweeper) limitReached() bool {
	return s.opts.Limit > 0 && s.claimed.Load() >= s.opts.Limit
}

// claim takes one of Limit's checks, reporting false once they're all taken.
func (s *sweeper) claim() bool {
	return s.opts.Limit <= 0 || s.claimed.Add(1) <= s.opts.Limit
}

// Run sweeps until the context is cancelled, the limit is reached, or one round
// completes when Once is set.
func Run(ctx context.Context, db *data.DB, checker check.Checker, opts Options) (int64, error) {
	s := newSweeper(db, checker, opts)

	// prune at startup, then hourly
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

		// once per round, a stale entry costs at most one query
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

// sweepRound drains each suffix in its own pipeline, so a slow registry doesn't
// hold up the others. One failing stops the rest.
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

// drain claims batches for suffix until none are due.
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

		// one batch per suffix
		if s.opts.Once {
			return nil
		}
	}
}

// round checks one batch across a worker pool. A failed write stops the
// batch.
func (s *sweeper) round(ctx context.Context, domains []string) error {
	work := make(chan string)

	g, gctx := errgroup.WithContext(ctx)
	for w := range s.opts.Workers {
		// own RNG per worker so they don't retry in lockstep
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

// checkOne checks and stores one domain. It only errors when a write fails.
func (s *sweeper) checkOne(ctx context.Context, backoff jitter.Strategy, domain string) error {
	opts := s.opts

	// resolve the server first so a throttled one costs nothing
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
		// defer past the throttle, otherwise the next claim returns it again
		if err := s.db.DeferCheck(ctx, sqlcgen.DeferCheckParams{
			Domain:     domain,
			FreshUntil: sqltime.At(until),
		}); err != nil {
			return fmt.Errorf("defer %s: %w", domain, err)
		}
		return nil
	}

	if !s.claim() {
		return nil
	}

	// DNS can only prove registered, anything else goes to the checker
	var res check.Result
	if opts.PreCheck != nil {
		// no Origin, the server wasn't asked so its state shouldn't change
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

	// a 429 is about the server, so it holds for all its domains
	nextTry, serr := s.db.RecordServer(ctx, res)
	if serr != nil && err == nil {
		err = serr
	}
	// apply to the rest of this round too
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

// recentFailures counts failures in the backoff window, or zero if it can't.
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

// Cancelled reports whether err is the sweep being stopped.
func Cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// CheckDomains checks the given domains once each, the same way Run does. Each
// needs a row already. Limit, Once and Batch are ignored.
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
