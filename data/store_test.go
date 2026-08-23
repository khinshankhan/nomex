package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data/sqlcgen"
)

// seed puts a row in checks so foreign keys from attempts/blocked resolve.
func seed(t *testing.T, db *DB, domain string) {
	t.Helper()
	if _, err := db.SeedCheck(t.Context(), sqlcgen.SeedCheckParams{
		Domain:     domain,
		FreshUntil: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed %s: %v", domain, err)
	}
}

func TestStoreAnswer(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seed(t, db, "taken.dev")

	exp := time.Date(2027, 7, 2, 3, 23, 42, 0, time.UTC)
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
	if got.Expiration == nil || !got.Expiration.Equal(exp) {
		t.Errorf("expiration = %v, want %v", got.Expiration, exp)
	}
	if got.CheckedAt == nil {
		t.Error("checked_at was not set")
	}
	if !got.FreshUntil.After(time.Now()) {
		t.Errorf("fresh_until = %v, not in the future", got.FreshUntil)
	}
}

// The central rule: a failure must never become a status in checks.
func TestStoreFailureNeverBecomesAnAnswer(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seed(t, db, "flaky.dev")

	before, err := db.GetCheck(ctx, "flaky.dev")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}

	err = db.StoreResult(ctx, check.Result{
		Domain:    "flaky.dev",
		Status:    check.StatusUnknown,
		Err:       errors.New("timeout"),
		ErrKind:   "timeout",
		Retryable: true,
	}, 0)
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	after, err := db.GetCheck(ctx, "flaky.dev")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}
	if after.Status != before.Status {
		t.Errorf("status changed from %q to %q on a failure", before.Status, after.Status)
	}
	if after.CheckedAt != nil {
		t.Error("checked_at was set by a failure")
	}
	if !after.FreshUntil.After(before.FreshUntil) {
		t.Error("fresh_until was not deferred, so the sweep would spin on this row")
	}

	attempts, err := db.RecentAttempts(ctx, sqlcgen.RecentAttemptsParams{Domain: "flaky.dev", Limit: 10})
	if err != nil {
		t.Fatalf("RecentAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("%d attempts recorded, want 1", len(attempts))
	}
	if !attempts[0].Retryable {
		t.Error("a timeout was recorded as non-retryable")
	}
}

// data blocks exactly when the checker said to, and never otherwise. Which
// errors qualify is the checker's decision; see check/rdapchecker.
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

// A failure with no BlockReason must never reach blocked.
func TestStoreCancellationDoesNotBlock(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seed(t, db, "cancelled.dev")

	err := db.StoreResult(ctx, check.Result{
		Domain:  "cancelled.dev",
		Status:  check.StatusUnknown,
		Err:     context.Canceled,
		ErrKind: "non-rdap failure",
	}, 0)
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	blocked, err := db.IsBlocked(ctx, "cancelled.dev")
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if blocked {
		t.Error("a cancelled context blocked the domain")
	}
}
