package sweep

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/datatest"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

type fixedOrigin string

func (o fixedOrigin) Origin(context.Context, string) (string, error) {
	return string(o), nil
}

// a throttled server's domains wait until it reopens instead of being claimed
// again straight away
func TestThrottledDomainsAreDeferred(t *testing.T) {
	db := datatest.Open(t)
	ctx := t.Context()
	const origin = "https://rdap.example"

	if _, err := db.SeedCheck(ctx, sqlcgen.SeedCheckParams{
		Domain:     "a.dev",
		FreshUntil: sqltime.At(time.Now().Add(-time.Hour)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reopens := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := db.ThrottleUntil(ctx, origin, reopens); err != nil {
		t.Fatalf("throttle: %v", err)
	}

	var skips int
	checker := newFake(check.Result{Status: check.StatusNotFound})
	_, err := Run(ctx, db, checker, Options{
		Limits:  Limits{Every: time.Microsecond, Burst: 100},
		Origins: fixedOrigin(origin),
		Once:    true,
		Progress: func(s Stat) {
			if s.Skipped {
				skips++
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if skips != 1 {
		t.Errorf("skipped %d times, want 1", skips)
	}
	if n := checker.calls["a.dev"]; n != 0 {
		t.Errorf("queried a throttled registry %d times", n)
	}

	row, err := db.GetCheck(ctx, "a.dev")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}
	if !row.FreshUntil.Equal(reopens) {
		t.Errorf("fresh_until = %v, want %v when the registry reopens", row.FreshUntil.Time, reopens)
	}
	due, err := db.DueSuffixes(ctx)
	if err != nil {
		t.Fatalf("DueSuffixes: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("still due: %v", due)
	}
}

// once a result can't be stored, the rest of the batch isn't checked
func TestStoreFailureStopsTheBatch(t *testing.T) {
	db := datatest.Open(t)

	// unseeded, so storing the failure breaks a foreign key
	domains := []string{"a.dev", "b.dev", "c.dev", "d.dev", "e.dev"}
	checker := newFake(check.Result{Status: check.StatusUnknown, Err: errors.New("refused")})

	_, err := CheckDomains(t.Context(), db, checker, domains, Options{
		Limits:  Limits{Every: time.Microsecond, Burst: 100},
		Workers: 1,
	})
	if err == nil {
		t.Fatal("CheckDomains succeeded, want a store error")
	}

	checker.mu.Lock()
	defer checker.mu.Unlock()
	if n := len(checker.calls); n != 1 {
		t.Errorf("checked %d domains after the first store failed, want 1", n)
	}
}

// seedDue seeds domains that are due now.
func seedDue(t *testing.T, db *data.DB, domains ...string) {
	t.Helper()
	for _, d := range domains {
		if _, err := db.SeedCheck(t.Context(), sqlcgen.SeedCheckParams{
			Domain:     d,
			FreshUntil: sqltime.At(time.Now().Add(-time.Hour)),
		}); err != nil {
			t.Fatalf("seed %s: %v", d, err)
		}
	}
}

// fast never makes a test wait.
var fast = Limits{Every: time.Microsecond, Burst: 100}

// anything DNS can't prove registered goes to RDAP, and a DNS hit leaves the
// RDAP server's state alone
func TestPreCheck(t *testing.T) {
	const origin = "https://rdap.example"

	tests := []struct {
		name         string
		dns          check.Status
		wantRDAP     int
		wantStatus   string
		wantSource   string
		wantFailures int
	}{
		{"hit skips rdap", check.StatusRegistered, 0, "registered", "dns", 1},
		{"miss falls through to rdap", check.StatusUnknown, 1, "not_found", "rdap", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := datatest.Open(t)
			ctx := t.Context()
			seedDue(t, db, "a.dev")
			// a failure streak whose throttle has lapsed
			if err := db.ThrottleUntil(ctx, origin, time.Now().Add(-time.Minute)); err != nil {
				t.Fatalf("throttle: %v", err)
			}

			rdap := newFake(check.Result{Status: check.StatusNotFound, Source: "rdap", Origin: origin})
			_, err := CheckDomains(ctx, db, rdap, []string{"a.dev"}, Options{
				Limits:   fast,
				Origins:  fixedOrigin(origin),
				PreCheck: newFake(check.Result{Status: tt.dns, Source: "dns"}),
			})
			if err != nil {
				t.Fatalf("CheckDomains: %v", err)
			}

			if n := rdap.calls["a.dev"]; n != tt.wantRDAP {
				t.Errorf("rdap queried %d times, want %d", n, tt.wantRDAP)
			}
			row, err := db.GetCheck(ctx, "a.dev")
			if err != nil {
				t.Fatalf("GetCheck: %v", err)
			}
			if row.Status != tt.wantStatus || row.Source == nil || *row.Source != tt.wantSource {
				t.Errorf("stored %s from %v, want %s from %s", row.Status, row.Source, tt.wantStatus, tt.wantSource)
			}
			if n := db.ServerFailures(ctx, origin); n != tt.wantFailures {
				t.Errorf("server failures = %d, want %d", n, tt.wantFailures)
			}
		})
	}
}

func TestRunStops(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want int64
	}{
		{"at the limit", Options{Limit: 2, Workers: 1}, 2},
		{"at the limit with several workers", Options{Limit: 2, Workers: 4}, 2},
		{"after one batch with once", Options{Once: true, Batch: 3, Workers: 1}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := datatest.Open(t)
			seedDue(t, db, "a.dev", "b.dev", "c.dev", "d.dev", "e.dev")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			checker := newFake(check.Result{Status: check.StatusNotFound})
			opts := tt.opts
			opts.Limits = fast
			done, err := Run(ctx, db, checker, opts)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if done != tt.want {
				t.Errorf("Run reported %d checks, want %d", done, tt.want)
			}
			if n := int64(len(checker.calls)); n != tt.want {
				t.Errorf("queried %d domains, want %d", n, tt.want)
			}
		})
	}
}

// strayChecker answers for a domain with no row, so storing it fails.
type strayChecker struct{}

func (strayChecker) Source() string {
	return "stray"
}

func (strayChecker) Check(context.Context, string) check.Result {
	return check.Result{Domain: "ghost.dev", Status: check.StatusUnknown, Err: errors.New("refused")}
}

func TestRunReturnsStoreErrors(t *testing.T) {
	db := datatest.Open(t)
	seedDue(t, db, "a.dev")

	_, err := Run(t.Context(), db, strayChecker{}, Options{Limits: fast})
	if err == nil || !strings.Contains(err.Error(), "store") {
		t.Errorf("Run = %v, want a store error", err)
	}
}

// a sweep prunes attempts older than a month when it starts
func TestRunPrunesOldAttempts(t *testing.T) {
	db := datatest.Open(t)
	ctx := t.Context()
	seedDue(t, db, "a.dev")
	// nothing due, so Run only prunes
	if err := db.DeferCheck(ctx, sqlcgen.DeferCheckParams{
		Domain:     "a.dev",
		FreshUntil: sqltime.At(time.Now().Add(time.Hour)),
	}); err != nil {
		t.Fatalf("defer: %v", err)
	}

	for _, age := range []time.Duration{31 * 24 * time.Hour, 29 * 24 * time.Hour} {
		if err := db.RecordAttempt(ctx, sqlcgen.RecordAttemptParams{
			Domain:      "a.dev",
			AttemptedAt: sqltime.At(time.Now().Add(-age)),
			ErrorKind:   "timeout",
			Retryable:   true,
		}); err != nil {
			t.Fatalf("RecordAttempt: %v", err)
		}
	}

	if _, err := Run(ctx, db, newFake(check.Result{Status: check.StatusNotFound}), Options{Limits: fast, Once: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	left, err := db.RecentAttempts(ctx, sqlcgen.RecentAttemptsParams{Domain: "a.dev", Limit: 10})
	if err != nil {
		t.Fatalf("RecentAttempts: %v", err)
	}
	if len(left) != 1 || time.Since(left[0].AttemptedAt.Time) > 30*24*time.Hour {
		t.Errorf("kept %d attempts, want only the one from 29 days ago", len(left))
	}
}

// only failures from the last week count toward backoff
func TestFailureStreakCountsTheLastWeek(t *testing.T) {
	db := datatest.Open(t)
	ctx := t.Context()
	seedDue(t, db, "a.dev")

	for _, age := range []time.Duration{8 * 24 * time.Hour, 2 * 24 * time.Hour, time.Hour} {
		if err := db.RecordAttempt(ctx, sqlcgen.RecordAttemptParams{
			Domain:      "a.dev",
			AttemptedAt: sqltime.At(time.Now().Add(-age)),
			ErrorKind:   "timeout",
			Retryable:   true,
		}); err != nil {
			t.Fatalf("RecordAttempt: %v", err)
		}
	}

	failing := newFake(check.Result{Status: check.StatusUnknown, Err: errors.New("timeout"), ErrKind: "timeout", Retryable: true})
	if _, err := CheckDomains(ctx, db, failing, []string{"a.dev"}, Options{Limits: fast, MaxAttempts: 1}); err != nil {
		t.Fatalf("CheckDomains: %v", err)
	}

	row, err := db.GetCheck(ctx, "a.dev")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}
	// two in the window, 5m doubled twice
	want := time.Now().Add(20 * time.Minute)
	if row.FreshUntil.Before(want.Add(-5*time.Second)) || row.FreshUntil.After(want.Add(5*time.Second)) {
		t.Errorf("deferred to %v, want about %v", row.FreshUntil.Time, want)
	}
}

// stalledChecker hangs on .dev until released and answers everything else
// straight away.
type stalledChecker struct {
	release chan struct{}
}

func (stalledChecker) Source() string {
	return "stalled"
}

func (c stalledChecker) Check(ctx context.Context, domain string) check.Result {
	if strings.HasSuffix(domain, ".dev") {
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	}
	return check.Result{Domain: domain, Status: check.StatusNotFound}
}

// a stalled suffix doesn't hold up another. .dev only answers once every .com
// domain is done, which deadlocks if they share workers
func TestStalledSuffixDoesNotBlockOthers(t *testing.T) {
	db := datatest.Open(t)
	// more .dev due, so it's claimed first
	seedDue(t, db, "a.dev", "b.dev", "c.dev", "a.com", "b.com")

	// so a regression fails instead of hanging
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	checker := stalledChecker{release: make(chan struct{})}
	var (
		mu      sync.Mutex
		comDone int
		checked []string
	)
	_, err := Run(ctx, db, checker, Options{
		Limits:  fast,
		Workers: 1,
		Once:    true,
		Progress: func(s Stat) {
			mu.Lock()
			defer mu.Unlock()

			checked = append(checked, s.Domain)
			if strings.HasSuffix(s.Domain, ".com") {
				comDone++
				if comDone == 2 {
					close(checker.release)
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v (a stalled .dev held up .com)", err)
	}
	if len(checked) != 5 {
		t.Errorf("checked %v, want all 5", checked)
	}
}
