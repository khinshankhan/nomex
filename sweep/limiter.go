package sweep

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits is the rate budget for one server.
type Limits struct {
	// Every is the interval between tokens: rate.Every(Every) is the refill
	// rate. Burst is how many may be spent at once.
	Every time.Duration
	Burst int
}

// DefaultLimits is what an unmeasured registry gets: one per second, which
// held for 44 consecutive queries against the strictest server measured.
var DefaultLimits = Limits{Every: time.Second, Burst: 3}

// MeasuredLimits are per-origin budgets established by testing, quiet window,
// no burst and no retry so nothing masked the result.
//
//	pubapi.registry.google  1/sec clean over 44; 429s at 1.5, 2 and 5
//	rdap.verisign.com       10/sec clean over 500, no failures at all
//
// Verisign tolerating ten times Google is the reason rates are per origin
// rather than global: one shared number is either unsafe for the strictest
// server or wastes an order of magnitude on the most permissive.
var MeasuredLimits = map[string]Limits{
	"https://pubapi.registry.google": {Every: time.Second, Burst: 3},
	"https://rdap.verisign.com":      {Every: 100 * time.Millisecond, Burst: 10},
}

// limiters hands out one rate limiter per origin.
//
// A single global rate is wrong: registries are independent servers with
// independent budgets, so sweeping .com and .dev together would waste
// Verisign's headroom on Google's limit. Keyed on origin rather than suffix
// because .com and .net are one Verisign server.
type limiters struct {
	mu       sync.Mutex
	byOrigin map[string]*rate.Limiter
	limits   Limits
	measured map[string]Limits
}

func newLimiters(l Limits) *limiters {
	if l.Every <= 0 {
		l.Every = DefaultLimits.Every
	}
	if l.Burst <= 0 {
		l.Burst = DefaultLimits.Burst
	}
	return &limiters{
		byOrigin: make(map[string]*rate.Limiter),
		limits:   l,
		measured: MeasuredLimits,
	}
}

// for returns the limiter for origin, creating it on first use.
//
// An unknown origin -- bootstrap could not resolve one -- shares a single
// bucket rather than getting an unlimited one.
func (l *limiters) for_(origin string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	lim, ok := l.byOrigin[origin]
	if !ok {
		// A measured budget beats the default, but never the caller's own
		// choice: passing -rate is an explicit instruction, and a measurement
		// taken on one machine may not hold on another.
		limits := l.limits
		if m, known := l.measured[origin]; known && l.limits == DefaultLimits {
			limits = m
		}
		lim = rate.NewLimiter(rate.Every(limits.Every), limits.Burst)
		l.byOrigin[origin] = lim
	}
	return lim
}

// wait blocks until origin has a token, the context ends, or waiting would
// outlast the context's deadline.
//
// Reserve rather than Wait: it reports the delay before committing, so a query
// that could not finish in time is abandoned instead of queued behind a token
// it will never get to use.
func (l *limiters) wait(ctx context.Context, origin string) error {
	r := l.for_(origin).Reserve()
	if !r.OK() {
		// Burst smaller than one request: the limiter can never satisfy this.
		return context.DeadlineExceeded
	}

	delay := r.Delay()
	if delay == 0 {
		return nil
	}

	if deadline, ok := ctx.Deadline(); ok && time.Now().Add(delay).After(deadline) {
		r.Cancel()
		return context.DeadlineExceeded
	}

	t := time.NewTimer(delay)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		r.Cancel()
		return ctx.Err()
	}
}
