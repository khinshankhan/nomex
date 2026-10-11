package sweep

import (
	"context"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// One registry's traffic shouldn't spend another's budget.
func TestLimitersAreIndependentPerOrigin(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Hour, Burst: 1})
	ctx := context.Background()

	// spend google's single token
	if err := lims.wait(ctx, "https://pubapi.registry.google"); err != nil {
		t.Fatalf("first google token: %v", err)
	}

	// verisign should still have its own right away
	start := time.Now()
	if err := lims.wait(ctx, "https://rdap.verisign.com"); err != nil {
		t.Fatalf("verisign token: %v", err)
	}
	if waited := time.Since(start); waited > 50*time.Millisecond {
		t.Errorf("verisign waited %v for a token google spent", waited)
	}
}

// A query that can't finish before the deadline should be dropped, not queued.
func TestLimiterRespectsDeadline(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Hour, Burst: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := lims.wait(ctx, "o"); err != nil {
		t.Fatalf("first token: %v", err)
	}

	start := time.Now()
	err := lims.wait(ctx, "o")
	if err == nil {
		t.Fatal("waiting past the deadline returned nil")
	}
	// should give up right away instead of sleeping to the deadline
	if waited := time.Since(start); waited > 15*time.Millisecond {
		t.Errorf("gave up after %v; expected an immediate refusal", waited)
	}
}

// A measured registry gets its own budget instead of the default, but an
// explicit -rate wins over both.
func TestLimiterBudgets(t *testing.T) {
	tests := []struct {
		name      string
		limits    Limits
		origin    string
		wantRate  rate.Limit
		wantBurst int
	}{
		{"default for an unmeasured origin", DefaultLimits, "https://rdap.example", 1, 3},
		{"measured verisign", DefaultLimits, "https://rdap.verisign.com", 10, 10},
		{"measured google", DefaultLimits, "https://pubapi.registry.google", 1, 3},
		{"unset falls back to measured", Limits{}, "https://rdap.verisign.com", 10, 10},
		{"explicit beats measured", Limits{Every: time.Hour, Burst: 1}, "https://rdap.verisign.com", rate.Every(time.Hour), 1},
		{"unresolved origin is still limited", DefaultLimits, "", 1, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := newLimiters(tt.limits).limiter(tt.origin)
			if lim.Limit() != tt.wantRate || lim.Burst() != tt.wantBurst {
				t.Errorf("got %v/s burst %d, want %v/s burst %d",
					lim.Limit(), lim.Burst(), tt.wantRate, tt.wantBurst)
			}
		})
	}
}
