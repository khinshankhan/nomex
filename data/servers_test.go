package data

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
)

func TestRecordServer(t *testing.T) {
	const origin = "https://rdap.example"

	var (
		ok        = check.Result{Domain: "a.dev", Origin: origin, Status: check.StatusNotFound}
		permanent = check.Result{Domain: "a.dev", Origin: origin, Err: errNotDone{}}
		// eg a 503, worth retrying the domain but not about our rate
		transient = check.Result{Domain: "a.dev", Origin: origin, Err: errNotDone{}, Retryable: true}
		bare429   = check.Result{Domain: "a.dev", Origin: origin, Err: errNotDone{}, Retryable: true, Throttled: true}
	)
	retryAfter := func(d time.Duration) check.Result {
		r := bare429
		r.RetryAfter = d
		return r
	}

	tests := []struct {
		name         string
		results      []check.Result
		wantFailures int64
		wantWait     time.Duration // 0 means not throttled
	}{
		{"success", []check.Result{ok}, 0, 0},
		{"retry-after is honoured", []check.Result{retryAfter(10 * time.Minute)}, 1, 10 * time.Minute},
		{"bare 429 starts at 5m", []check.Result{bare429}, 1, 5 * time.Minute},
		// two failures already, 5m doubled twice
		{"bare 429 backs off by streak", []check.Result{permanent, permanent, bare429}, 3, 20 * time.Minute},
		{"permanent failure doesn't throttle", []check.Result{permanent, permanent}, 2, 0},
		{"transient failure doesn't throttle", []check.Result{transient, transient}, 2, 0},
		{"a wait is never shortened", []check.Result{retryAfter(time.Hour), retryAfter(time.Minute)}, 2, time.Hour},
		{"a wait can be extended", []check.Result{retryAfter(time.Minute), retryAfter(time.Hour)}, 2, time.Hour},
		{"success clears streak and throttle", []check.Result{retryAfter(time.Hour), bare429, ok}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTest(t)
			ctx := t.Context()

			for _, res := range tt.results {
				if _, err := db.RecordServer(ctx, res); err != nil {
					t.Fatalf("RecordServer: %v", err)
				}
			}

			state, err := db.ServerState(ctx, origin)
			if err != nil {
				t.Fatalf("ServerState: %v", err)
			}
			if state.ConsecutiveFailures != tt.wantFailures {
				t.Errorf("failures = %d, want %d", state.ConsecutiveFailures, tt.wantFailures)
			}

			throttled, err := db.Throttled(ctx)
			if err != nil {
				t.Fatalf("Throttled: %v", err)
			}
			until, ok := throttled[origin]
			if tt.wantWait == 0 {
				if ok {
					t.Errorf("throttled until %v, want not throttled", until)
				}
				return
			}
			if !ok || !within(until, tt.wantWait) {
				t.Errorf("throttled until %v (ok=%v), want %v from now", until, ok, tt.wantWait)
			}
		})
	}
}

func TestRecordServerWithoutOrigin(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	if _, err := db.RecordServer(ctx, check.Result{Domain: "a.dev", Err: errNotDone{}, Retryable: true}); err != nil {
		t.Fatalf("RecordServer: %v", err)
	}
	if _, err := db.ServerState(ctx, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ServerState(\"\") err = %v, want no row", err)
	}
}
