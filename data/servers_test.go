package data

import (
	"testing"

	"github.com/khinshankhan/nomex/check"
)

func TestServerStateReadableAfterUnthrottledFailures(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	// a permanent failure carries no wait, so neither write sets a throttle
	res := check.Result{
		Domain: "a.dev",
		Origin: "https://rdap.example",
		Err:    errNotDone{},
	}
	for range 2 {
		if _, err := db.RecordServer(ctx, res); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	s, err := db.ServerState(ctx, res.Origin)
	if err != nil {
		t.Fatalf("server state: %v", err)
	}
	if s.ConsecutiveFailures != 2 {
		t.Errorf("failures = %d, want 2", s.ConsecutiveFailures)
	}
	if s.RateLimitedUntil != nil {
		t.Errorf("rate_limited_until = %v, want NULL", s.RateLimitedUntil)
	}
}
