package sweep

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits is the rate budget for one server. A zero field is unset and comes
// from the server's measured budget, else DefaultLimits.
type Limits struct {
	// interval between tokens, and how many can be spent at once
	Every time.Duration
	Burst int
}

// DefaultLimits is for servers without a measured budget. One a second held for
// 44 queries against Google, the strictest measured.
var DefaultLimits = Limits{Every: time.Second, Burst: 3}

// MeasuredLimits are budgets measured per server (no burst, no retries):
//
//	pubapi.registry.google  1/sec clean over 44, 429s at 1.5, 2 and 5
//	rdap.verisign.com       10/sec clean over 500
var MeasuredLimits = map[string]Limits{
	"https://pubapi.registry.google": {Every: time.Second, Burst: 3},
	"https://rdap.verisign.com":      {Every: 100 * time.Millisecond, Burst: 10},
}

// limiters holds one rate limiter per origin. Not per suffix, since .com and
// .net are both Verisign.
type limiters struct {
	mu       sync.Mutex
	byOrigin map[string]*rate.Limiter
	limits   Limits
	measured map[string]Limits
}

func newLimiters(l Limits) *limiters {
	return &limiters{
		byOrigin: make(map[string]*rate.Limiter),
		limits:   l,
		measured: MeasuredLimits,
	}
}

// limiter returns the limiter for origin, creating it on first use. Unresolved
// origins share one.
func (l *limiters) limiter(origin string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	lim, ok := l.byOrigin[origin]
	if !ok {
		b := l.budget(origin)
		lim = rate.NewLimiter(rate.Every(b.Every), b.Burst)
		l.byOrigin[origin] = lim
	}
	return lim
}

// budget fills each unset field from origin's measured budget, else the
// default. An explicit field always wins.
func (l *limiters) budget(origin string) Limits {
	fallback, known := l.measured[origin]
	if !known {
		fallback = DefaultLimits
	}

	b := l.limits
	if b.Every <= 0 {
		b.Every = fallback.Every
	}
	if b.Burst <= 0 {
		b.Burst = fallback.Burst
	}
	return b
}

// wait blocks until origin has a token. It fails straight away if the token
// would come after the context's deadline.
//
// NOTE: Reserve, not Wait, since it reports the delay up front.
func (l *limiters) wait(ctx context.Context, origin string) error {
	r := l.limiter(origin).Reserve()
	if !r.OK() {
		// burst below one, can never be satisfied
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
