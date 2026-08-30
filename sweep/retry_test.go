package sweep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
)

// fakeChecker returns canned results, counting calls per domain.
type fakeChecker struct {
	mu      sync.Mutex
	calls   map[string]int
	results []check.Result
}

func (f *fakeChecker) Source() string { return "fake" }

func (f *fakeChecker) Check(ctx context.Context, domain string) check.Result {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls[domain]++
	i := f.calls[domain] - 1
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	r := f.results[i]
	r.Domain = domain
	return r
}

func newFake(results ...check.Result) *fakeChecker {
	return &fakeChecker{calls: map[string]int{}, results: results}
}

func fastLimiters() *limiters {
	return newLimiters(Limits{Every: time.Microsecond, Burst: 100})
}

// A momentary failure must not cost a full TTL cycle.
func TestRetrySucceedsAfterTransientFailure(t *testing.T) {
	f := newFake(
		check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true},
		check.Result{Status: check.StatusNotFound},
	)

	res := checkWithRetry(context.Background(), f, fastLimiters(), newBackoff(0), "x.dev", "o", 3)

	if res.Failed() {
		t.Errorf("gave up after a retryable failure: %v", res.Err)
	}
	if res.Status != check.StatusNotFound {
		t.Errorf("status = %s, want not_found", res.Status)
	}
	if f.calls["x.dev"] != 2 {
		t.Errorf("checked %d times, want 2", f.calls["x.dev"])
	}
}

// A permanent failure will fail identically however often it is asked.
func TestRetryDoesNotRepeatPermanentFailures(t *testing.T) {
	f := newFake(check.Result{
		Status: check.StatusUnknown,
		Err:    errors.New("malformed"),
		// Not retryable.
		BlockReason: "malformed domain name",
	})

	res := checkWithRetry(context.Background(), f, fastLimiters(), newBackoff(0), "x.dev", "o", 5)

	if f.calls["x.dev"] != 1 {
		t.Errorf("retried a permanent failure %d times", f.calls["x.dev"])
	}
	if !res.Failed() {
		t.Error("permanent failure reported as success")
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFake(check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true})

	res := checkWithRetry(context.Background(), f, fastLimiters(), newBackoff(0), "x.dev", "o", 3)

	if f.calls["x.dev"] != 3 {
		t.Errorf("checked %d times, want 3", f.calls["x.dev"])
	}
	if !res.Failed() {
		t.Error("exhausted retries reported as success")
	}
	if !res.Retryable {
		t.Error("lost the retryable flag, so the row would not be deferred")
	}
}

func TestRetryStopsOnCancellation(t *testing.T) {
	f := newFake(check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	checkWithRetry(ctx, f, fastLimiters(), newBackoff(0), "x.dev", "o", 10)

	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("kept retrying for %v after cancellation", elapsed)
	}
}

// Workers must not retry in lockstep, or the pool arrives at a struggling
// server as a stampede. This is why each gets its own seeded RNG.
func TestBackoffDiffersPerWorker(t *testing.T) {
	a := newBackoff(0)
	b := newBackoff(10_000)

	var same int
	const rounds = 20
	for i := range rounds {
		if a.Next(i%5) == b.Next(i%5) {
			same++
		}
	}

	if same == rounds {
		t.Error("two workers produced identical delays every time")
	}
}

func TestBackoffIsBounded(t *testing.T) {
	s := newBackoff(0)
	for attempt := range 20 {
		d := s.Next(attempt)
		if d < 0 {
			t.Errorf("attempt %d: negative delay %d", attempt, d)
		}
		if d > 8_000 {
			t.Errorf("attempt %d: delay %dms exceeds the 8s cap", attempt, d)
		}
	}
}
