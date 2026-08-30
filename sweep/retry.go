package sweep

import (
	"context"
	"math/rand"
	"time"

	"github.com/khinshankhan/jitter-go/v2"
	"github.com/khinshankhan/nomex/check"
)

// DefaultMaxAttempts bounds how many times one domain is retried within a
// single sweep. Beyond this the failure is recorded and the row is deferred,
// which is a cheaper place to wait than holding a worker.
const DefaultMaxAttempts = 3

// newBackoff builds a jitter strategy. Each worker gets its own, seeded
// differently: a shared RNG makes every worker retry at the same instant, so
// the pool arrives at the server as a stampede rather than spread out.
func newBackoff(seed int64) jitter.Strategy {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + seed))

	s, err := jitter.New(jitter.Config{
		Base:   250,   // ms
		Cap:    8_000, // ms
		Random: r.Int63n,
	})
	if err != nil {
		// Only reachable with a Base or Cap of zero, both of which are
		// constants here.
		panic("sweep: " + err.Error())
	}
	return s
}

// checkWithRetry queries a domain, retrying transient failures.
//
// Retrying here rather than leaving it to the next sweep matters because the
// alternative is a full TTL cycle: a domain that hits a momentary 429 would
// otherwise not be looked at again for hours.
//
// Only retryable failures are retried. A malformed name or an unserved suffix
// will fail identically however many times it is asked.
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
			// No token in time. Report it as a retryable failure so the row is
			// deferred rather than treated as an answer.
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

		// A server-supplied Retry-After outranks our guess.
		delay := time.Duration(backoff.Next(attempt)) * time.Millisecond
		if res.RetryAfter > 0 {
			delay = res.RetryAfter
		}

		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return res
		}
		t.Stop()
	}

	return res
}
