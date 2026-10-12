package sweep

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
)

// fakeChecker returns canned results in order and counts calls per domain.
type fakeChecker struct {
	mu      sync.Mutex
	calls   map[string]int
	results []check.Result
}

func (f *fakeChecker) Source() string {
	return "fake"
}

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

// spyBackoff records which attempts asked for a delay. It never waits.
type spyBackoff struct {
	asked []int
}

func (s *spyBackoff) Next(attempt int) int64 {
	s.asked = append(s.asked, attempt)
	return 0
}

func TestCheckWithRetry(t *testing.T) {
	transient := check.Result{Status: check.StatusUnknown, Err: errors.New("503"), Retryable: true}
	throttled := check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true, Throttled: true}
	malformed := check.Result{Status: check.StatusUnknown, Err: errors.New("malformed"), BlockReason: "malformed domain name"}
	available := check.Result{Status: check.StatusNotFound}
	shortWait := check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true, RetryAfter: time.Millisecond}

	tests := []struct {
		name      string
		results   []check.Result
		attempts  int
		cancelled bool
		wantCalls int
		wantErr   bool
		wantWaits []int // attempts that backed off before the next try
	}{
		{"retries a transient failure", []check.Result{transient, available}, 3, false, 2, false, []int{0}},
		{"permanent failure isn't retried", []check.Result{malformed}, 5, false, 1, true, nil},
		// no wait after the last attempt
		{"gives up after max attempts", []check.Result{transient}, 3, false, 3, true, []int{0, 1}},
		{"stops on cancellation", []check.Result{transient}, 10, true, 1, true, []int{0}},
		// the caller throttles the whole server instead
		{"throttled failure isn't retried", []check.Result{throttled}, 3, false, 1, true, nil},
		{"waits out a short Retry-After", []check.Result{shortWait, available}, 3, false, 2, false, []int{0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if tt.cancelled {
				cancel()
			}
			defer cancel()

			f := newFake(tt.results...)
			backoff := &spyBackoff{}
			res := checkWithRetry(ctx, f, fastLimiters(), backoff, "x.dev", "o", tt.attempts)

			if f.calls["x.dev"] != tt.wantCalls {
				t.Errorf("checked %d times, want %d", f.calls["x.dev"], tt.wantCalls)
			}
			if res.Failed() != tt.wantErr {
				t.Errorf("Err = %v, want error=%v", res.Err, tt.wantErr)
			}
			if !slices.Equal(backoff.asked, tt.wantWaits) {
				t.Errorf("backed off after attempts %v, want %v", backoff.asked, tt.wantWaits)
			}
			// the last result comes back as is
			if want := tt.results[len(tt.results)-1]; res.Status != want.Status || res.Retryable != want.Retryable {
				t.Errorf("got %s retryable=%v, want %s retryable=%v",
					res.Status, res.Retryable, want.Status, want.Retryable)
			}
		})
	}
}

// a long Retry-After goes back to the caller, which throttles the server,
// instead of parking the worker
func TestLongRetryAfterIsNotWaitedOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	f := newFake(check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true, RetryAfter: time.Hour})
	res := checkWithRetry(ctx, f, fastLimiters(), &spyBackoff{}, "x.dev", "o", 3)

	if ctx.Err() != nil {
		t.Fatalf("waited until the context ended")
	}
	if f.calls["x.dev"] != 1 {
		t.Errorf("checked %d times, want 1", f.calls["x.dev"])
	}
	if res.RetryAfter != time.Hour {
		t.Errorf("RetryAfter = %v, want 1h passed on", res.RetryAfter)
	}
}
