package sweep

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// servers don't share a budget, and a token that can't arrive before the
// deadline is refused up front
func TestLimiterWait(t *testing.T) {
	const (
		google   = "https://pubapi.registry.google"
		verisign = "https://rdap.verisign.com"
	)

	// an hour per token, so a second never arrives
	lims := newLimiters(Limits{Every: time.Hour, Burst: 1})

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	if err := lims.wait(ctx, google); err != nil {
		t.Fatalf("first google token: %v", err)
	}
	if err := lims.wait(ctx, verisign); err != nil {
		t.Errorf("verisign was refused a token google spent: %v", err)
	}

	err := lims.wait(ctx, google)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second google token: err = %v, want DeadlineExceeded", err)
	}
	// still live means it didn't sleep to the deadline
	if ctx.Err() != nil {
		t.Error("wait slept until the deadline before refusing")
	}
}

func TestLimiterWaitsWithoutDeadline(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Millisecond, Burst: 1})

	for i := range 3 {
		if err := lims.wait(context.Background(), "o"); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
}

// a measured budget beats the default, an explicit -rate or -burst beats both
func TestLimiterBudgets(t *testing.T) {
	const verisign = "https://rdap.verisign.com"

	tests := []struct {
		name      string
		limits    Limits
		origin    string
		wantRate  rate.Limit
		wantBurst int
	}{
		{"default for an unmeasured origin", Limits{}, "https://rdap.example", 1, 3},
		{"measured verisign", Limits{}, verisign, 10, 10},
		{"measured google", Limits{}, "https://pubapi.registry.google", 1, 3},
		{"explicit beats measured", Limits{Every: time.Hour, Burst: 1}, verisign, rate.Every(time.Hour), 1},
		// the same values as the default are still explicit
		{"explicit default beats measured", Limits{Every: time.Second, Burst: 3}, verisign, 1, 3},
		{"explicit rate keeps measured burst", Limits{Every: time.Second}, verisign, 1, 10},
		{"explicit burst keeps measured rate", Limits{Burst: 1}, verisign, 10, 1},
		{"unresolved origin is still limited", Limits{}, "", 1, 3},
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
