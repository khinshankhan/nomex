package sweep

import (
	"context"
	"testing"
	"time"
)

// The whole point of keying on origin: one registry's budget must not be spent
// by another's traffic.
func TestLimitersAreIndependentPerOrigin(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Hour, Burst: 1})
	ctx := context.Background()

	// Spend google's single token.
	if err := lims.wait(ctx, "https://pubapi.registry.google"); err != nil {
		t.Fatalf("first google token: %v", err)
	}

	// Verisign must still have its own, immediately.
	start := time.Now()
	if err := lims.wait(ctx, "https://rdap.verisign.com"); err != nil {
		t.Fatalf("verisign token: %v", err)
	}
	if waited := time.Since(start); waited > 50*time.Millisecond {
		t.Errorf("verisign waited %v for a token google spent", waited)
	}
}

func TestLimiterRefills(t *testing.T) {
	lims := newLimiters(Limits{Every: 20 * time.Millisecond, Burst: 1})
	ctx := context.Background()

	if err := lims.wait(ctx, "o"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := lims.wait(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 10*time.Millisecond {
		t.Errorf("second token came after %v, want at least the refill interval", waited)
	}
}

// Burst is what lets a sweep start promptly instead of trickling from cold.
func TestLimiterAllowsBurst(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Hour, Burst: 3})
	ctx := context.Background()

	start := time.Now()
	for i := range 3 {
		if err := lims.wait(ctx, "o"); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if waited := time.Since(start); waited > 50*time.Millisecond {
		t.Errorf("a burst of 3 took %v", waited)
	}
}

// Reserve rather than Wait: a query that could not finish before the deadline
// is abandoned instead of queued behind a token it will never use.
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
	// It must give up immediately rather than sleeping to the deadline.
	if waited := time.Since(start); waited > 15*time.Millisecond {
		t.Errorf("gave up after %v; expected an immediate refusal", waited)
	}
}

func TestLimiterCancellation(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Hour, Burst: 1})
	ctx, cancel := context.WithCancel(context.Background())

	if err := lims.wait(ctx, "o"); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := lims.wait(ctx, "o"); err == nil {
		t.Error("cancelled wait returned nil")
	}
}

// A measured registry gets its own budget rather than the conservative
// default, which is the difference between 1/sec and 10/sec on Verisign.
func TestMeasuredLimitsApply(t *testing.T) {
	lims := newLimiters(DefaultLimits)
	ctx := context.Background()

	// Verisign is measured at 10/sec, so ten back-to-back must be fast.
	start := time.Now()
	for range 10 {
		if err := lims.wait(ctx, "https://rdap.verisign.com"); err != nil {
			t.Fatal(err)
		}
	}
	verisign := time.Since(start)

	// An unmeasured origin falls back to 1/sec: three burst tokens, then wait.
	start = time.Now()
	for range 5 {
		if err := lims.wait(ctx, "https://rdap.unknown.example"); err != nil {
			t.Fatal(err)
		}
	}
	unknown := time.Since(start)

	if verisign > unknown {
		t.Errorf("measured origin was slower (%v) than the default (%v)", verisign, unknown)
	}
}

// An explicit -rate must win: a measurement taken elsewhere may not hold here.
func TestExplicitLimitsOverrideMeasured(t *testing.T) {
	lims := newLimiters(Limits{Every: time.Hour, Burst: 1})
	ctx := context.Background()

	if err := lims.wait(ctx, "https://rdap.verisign.com"); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := lims.wait(ctx2, "https://rdap.verisign.com"); err == nil {
		t.Error("measured budget overrode an explicit -rate")
	}
}
