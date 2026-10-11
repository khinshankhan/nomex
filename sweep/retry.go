package sweep

import (
	"context"
	"math/rand"
	"time"

	"github.com/khinshankhan/jitter-go/v2"
	"github.com/khinshankhan/nomex/check"
)

// DefaultMaxAttempts bounds retries per domain within one sweep. After that the
// failure is recorded and the row deferred, which beats holding a worker.
const DefaultMaxAttempts = 3

// newBackoff builds a jitter strategy. Each worker gets its own, seeded
// differently, so they don't all retry at the same instant.
func newBackoff(seed int64) jitter.Strategy {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + seed))

	s, err := jitter.New(jitter.Config{
		Base:   250,   // ms
		Cap:    8_000, // ms
		Random: r.Int63n,
	})
	if err != nil {
		// only possible with a zero Base or Cap, which are constants here
		panic("sweep: " + err.Error())
	}
	return s
}

// checkWithRetry queries a domain, retrying transient failures. Retrying here
// matters since otherwise a momentary 429 waits a full backoff cycle. Permanent
// failures aren't retried, they'd just fail the same way.
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
			// no token in time, report a retryable failure so the row gets
			// deferred instead of treated as an answer
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

		// select picks at random when both are ready, so a zero delay could
		// retry after cancellation without this
		if ctx.Err() != nil {
			return res
		}
	}

	return res
}
