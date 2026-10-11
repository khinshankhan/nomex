package sweep

import (
	"context"
	"math/rand"
	"time"

	"github.com/khinshankhan/jitter-go/v2"
	"github.com/khinshankhan/nomex/check"
)

// DefaultMaxAttempts bounds tries per domain. After that the failure is
// stored and the row deferred.
const DefaultMaxAttempts = 3

// newBackoff builds a jitter strategy, seeded so workers differ.
func newBackoff(seed int64) jitter.Strategy {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + seed))

	s, err := jitter.New(jitter.Config{
		Base:   250,   // ms
		Cap:    8_000, // ms
		Random: r.Int63n,
	})
	if err != nil {
		// only on a zero Base or Cap, both constants here
		panic("sweep: " + err.Error())
	}
	return s
}

// checkWithRetry queries a domain, retrying failures marked retryable.
func checkWithRetry(
	ctx context.Context,
	checker check.Checker,
	lim *limiters,
	backoff jitter.Strategy,
	domain, origin string,
	maxAttempts int,
) check.Result {
	var res check.Result

	for attempt := range maxAttempts {
		if err := lim.wait(ctx, origin); err != nil {
			// no token in time, retryable so the row gets deferred
			return check.Result{
				Domain:    domain,
				Status:    check.StatusUnknown,
				Origin:    origin,
				Err:       err,
				ErrKind:   "rate limiter timeout",
				Retryable: true,
			}
		}

		res = checker.Check(ctx, domain)
		if !res.Failed() || !res.Retryable {
			return res
		}
		if attempt == maxAttempts-1 {
			break
		}

		// a server's Retry-After beats our guess
		delay := time.Duration(backoff.Next(attempt)) * time.Millisecond
		if res.RetryAfter > 0 {
			delay = res.RetryAfter
		}

		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()

		// NOTE: select picks at random when both are ready, so a zero delay
		// could retry after cancellation without this
		if ctx.Err() != nil {
			return res
		}
	}

	return res
}
