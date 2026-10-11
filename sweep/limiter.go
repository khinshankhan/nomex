package sweep

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits is the rate budget for one server.
type Limits struct {
	// Every is the interval between tokens and Burst is how many can be spent
	// at once.
	Every time.Duration
	Burst int
}

// DefaultLimits is what an unmeasured registry gets. One per second held for
// 44 straight queries against the strictest server measured.
var DefaultLimits = Limits{Every: time.Second, Burst: 3}

// MeasuredLimits are per-origin budgets from testing (quiet window, no burst,
// no retries):
//
//	pubapi.registry.google  1/sec clean over 44; 429s at 1.5, 2 and 5
//	rdap.verisign.com       10/sec clean over 500, no failures at all
//
// Verisign handling ten times Google's rate is why limits are per origin.
var MeasuredLimits = map[string]Limits{
	"https://pubapi.registry.google": {Every: time.Second, Burst: 3},
	"https://rdap.verisign.com":      {Every: 100 * time.Millisecond, Burst: 10},
}

// limiters hands out one rate limiter per origin. It's keyed on origin, not
// suffix, since .com and .net are the same Verisign server.
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

// limiter returns the limiter for origin, creating it on first use. Unresolved
// origins all share one bucket instead of going unlimited.
func (l *limiters) limiter(origin string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	lim, ok := l.byOrigin[origin]
	if !ok {
		// a measured budget beats the default but never an explicit -rate
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
// NOTE: Reserve instead of Wait, since it reports the delay up front and a
// query that can't finish in time gets dropped instead of queued.
func (l *limiters) wait(ctx context.Context, origin string) error {
	r := l.limiter(origin).Reserve()
	if !r.OK() {
		// burst below one request, this can never be satisfied
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
