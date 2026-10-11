package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// seed puts a row in checks so foreign keys from attempts/blocked resolve.
func seed(t *testing.T, db *DB, domain string) {
	t.Helper()
	if _, err := db.SeedCheck(t.Context(), sqlcgen.SeedCheckParams{
		Domain:     domain,
		FreshUntil: sqltime.At(time.Now().Add(-time.Hour)),
	}); err != nil {
		t.Fatalf("seed %s: %v", domain, err)
	}
}

// within reports whether got is d from now, give or take the test's runtime.
func within(got time.Time, d time.Duration) bool {
	want := time.Now().Add(d)
	return !got.Before(want.Add(-5*time.Second)) && !got.After(want.Add(5*time.Second))
}

func TestStoreAnswer(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seed(t, db, "taken.dev")

	exp := time.Now().Add(365 * 24 * time.Hour).UTC().Truncate(time.Second)
	reg := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	err := db.StoreResult(ctx, check.Result{
		Domain:     "taken.dev",
		Status:     check.StatusRegistered,
		Source:     "rdap",
		Expiration: &exp,
		Registered: &reg,
		Server:     "https://rdap.example/domain/taken.dev",
		Origin:     "https://rdap.example",
	}, 0)
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	got, err := db.GetCheck(ctx, "taken.dev")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}
	if got.Status != "registered" {
		t.Errorf("status = %q, want registered", got.Status)
	}
	if got.Source == nil || *got.Source != "rdap" {
		t.Errorf("source = %v, want rdap", got.Source)
	}
	if got.Expiration == nil || !got.Expiration.Equal(exp) {
		t.Errorf("expiration = %v, want %v", got.Expiration, exp)
	}
	if got.RegisteredAt == nil || !got.RegisteredAt.Equal(reg) {
		t.Errorf("registered_at = %v, want %v", got.RegisteredAt, reg)
	}
	if got.Server == nil || *got.Server != "https://rdap.example/domain/taken.dev" {
		t.Errorf("server = %v, want the answering URL", got.Server)
	}
	if got.CheckedAt == nil || !within(got.CheckedAt.Time, 0) {
		t.Errorf("checked_at = %v, want now", got.CheckedAt)
	}
	// expiry a year out, so the 90 day cap applies
	if !within(got.FreshUntil.Time, 90*24*time.Hour) {
		t.Errorf("fresh_until = %v, want 90 days from now", got.FreshUntil.Time)
	}
}

// A failure should never become a status in checks. It's recorded as an
// attempt and the row is deferred, by the server's Retry-After if it sent one
// and by the failure streak otherwise.
func TestStoreFailure(t *testing.T) {
	tests := []struct {
		name       string
		failures   int
		retryAfter time.Duration
		wantDefer  time.Duration
	}{
		{"first failure", 0, 0, 5 * time.Minute},
		{"backs off by streak", 3, 0, 40 * time.Minute},
		{"retry-after wins", 3, 10 * time.Minute, 10 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTest(t)
			ctx := t.Context()
			seed(t, db, "flaky.dev")

			err := db.StoreResult(ctx, check.Result{
				Domain:     "flaky.dev",
				Status:     check.StatusUnknown,
				Err:        errors.New("rate limited"),
				ErrKind:    "rate limited",
				Retryable:  true,
				RetryAfter: tt.retryAfter,
			}, tt.failures)
			if err != nil {
				t.Fatalf("StoreResult: %v", err)
			}

			got, err := db.GetCheck(ctx, "flaky.dev")
			if err != nil {
				t.Fatalf("GetCheck: %v", err)
			}
			if got.Status != "unchecked" {
				t.Errorf("status = %q, want unchecked (a failure isn't an answer)", got.Status)
			}
			if got.CheckedAt != nil {
				t.Error("checked_at was set by a failure")
			}
			if !within(got.FreshUntil.Time, tt.wantDefer) {
				t.Errorf("fresh_until = %v, want %v from now", got.FreshUntil.Time, tt.wantDefer)
			}

			attempts, err := db.RecentAttempts(ctx, sqlcgen.RecentAttemptsParams{Domain: "flaky.dev", Limit: 10})
			if err != nil {
				t.Fatalf("RecentAttempts: %v", err)
			}
			if len(attempts) != 1 {
				t.Fatalf("%d attempts recorded, want 1", len(attempts))
			}
			a := attempts[0]
			if a.ErrorKind != "rate limited" || !a.Retryable {
				t.Errorf("attempt = %q retryable=%v, want \"rate limited\" retryable=true", a.ErrorKind, a.Retryable)
			}
			switch {
			case tt.retryAfter == 0 && a.RetryAfter != nil:
				t.Errorf("retry_after = %v, want NULL", a.RetryAfter.Time)
			case tt.retryAfter > 0 && (a.RetryAfter == nil || !within(a.RetryAfter.Time, tt.retryAfter)):
				t.Errorf("retry_after = %v, want %v from now", a.RetryAfter, tt.retryAfter)
			}
		})
	}
}

// data blocks exactly when the checker says to (see check/rdapchecker).
func TestStoreBlocksOnlyWhenTold(t *testing.T) {
	tests := []struct {
		name        string
		blockReason string
		wantBlock   bool
	}{
		{"checker said block", "no RDAP server published for this suffix", true},
		{"checker said nothing", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			seed(t, db, "x.dev")

			err := db.StoreResult(ctx, check.Result{
				Domain:      "x.dev",
				Status:      check.StatusUnknown,
				Err:         errors.New("failed"),
				ErrKind:     "some kind",
				BlockReason: tt.blockReason,
			}, 0)
			if err != nil {
				t.Fatalf("StoreResult: %v", err)
			}

			blocked, err := db.IsBlocked(ctx, "x.dev")
			if err != nil {
				t.Fatalf("IsBlocked: %v", err)
			}
			if blocked != tt.wantBlock {
				t.Errorf("blocked = %v, want %v", blocked, tt.wantBlock)
			}
		})
	}
}

// Retention has to actually delete old attempts, but leave recent ones since
// they drive the backoff.
func TestPruneAttemptsKeepsRecentFailures(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()
	seed(t, db, "x.dev")

	now := time.Now().UTC()
	for _, age := range []time.Duration{
		90 * 24 * time.Hour, // old
		31 * 24 * time.Hour, // old
		29 * 24 * time.Hour, // keep
		time.Hour,           // keep
	} {
		if err := db.RecordAttempt(ctx, sqlcgen.RecordAttemptParams{
			Domain:      "x.dev",
			AttemptedAt: sqltime.At(now.Add(-age)),
			ErrorKind:   "timeout",
			Retryable:   true,
		}); err != nil {
			t.Fatalf("RecordAttempt: %v", err)
		}
	}

	cutoff := sqltime.Text(now.Add(-30 * 24 * time.Hour))
	deleted, err := db.PruneAttempts(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneAttempts: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d, want 2", deleted)
	}

	left, err := db.RecentAttempts(ctx, sqlcgen.RecentAttemptsParams{Domain: "x.dev", Limit: 10})
	if err != nil {
		t.Fatalf("RecentAttempts: %v", err)
	}
	if len(left) != 2 {
		t.Errorf("%d attempts remain, want the 2 recent ones", len(left))
	}
	for _, a := range left {
		if now.Sub(a.AttemptedAt.Time) > 30*24*time.Hour {
			t.Errorf("kept an attempt from %v", a.AttemptedAt.Time)
		}
	}
}
