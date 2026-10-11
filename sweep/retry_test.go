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

func TestCheckWithRetry(t *testing.T) {
	rateLimited := check.Result{Status: check.StatusUnknown, Err: errors.New("429"), Retryable: true}
	malformed := check.Result{Status: check.StatusUnknown, Err: errors.New("malformed"), BlockReason: "malformed domain name"}
	available := check.Result{Status: check.StatusNotFound}

	tests := []struct {
		name      string
		results   []check.Result
		attempts  int
		cancelled bool
		wantCalls int
		wantErr   bool
	}{
		// a momentary failure shouldn't cost a full backoff cycle
		{"retries a transient failure", []check.Result{rateLimited, available}, 3, false, 2, false},
		// it would just fail the same way
		{"permanent failure isn't retried", []check.Result{malformed}, 5, false, 1, true},
		{"gives up after max attempts", []check.Result{rateLimited}, 3, false, 3, true},
		{"stops on cancellation", []check.Result{rateLimited}, 10, true, 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if tt.cancelled {
				cancel()
			}
			defer cancel()

			f := newFake(tt.results...)
			res := checkWithRetry(ctx, f, fastLimiters(), newBackoff(0), "x.dev", "o", tt.attempts)

			if f.calls["x.dev"] != tt.wantCalls {
				t.Errorf("checked %d times, want %d", f.calls["x.dev"], tt.wantCalls)
			}
			if res.Failed() != tt.wantErr {
				t.Errorf("Err = %v, want error=%v", res.Err, tt.wantErr)
			}
			// the last result comes back as is, so a retryable failure still
			// gets deferred rather than blocked
			if want := tt.results[len(tt.results)-1]; res.Status != want.Status || res.Retryable != want.Retryable {
				t.Errorf("got %s retryable=%v, want %s retryable=%v",
					res.Status, res.Retryable, want.Status, want.Retryable)
			}
		})
	}
}
