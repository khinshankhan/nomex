package sweep

import (
	"context"
	"testing"
	"time"
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

// Burst lets a sweep start quickly instead of trickling.
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

// A measured registry gets its own budget instead of the default, 10/sec vs
// 1/sec on Verisign.
func TestMeasuredLimitsApply(t *testing.T) {
	lims := newLimiters(DefaultLimits)
	ctx := context.Background()

	// verisign is measured at 10/sec, so ten in a row should be fast
	start := time.Now()
	for range 10 {
		if err := lims.wait(ctx, "https://rdap.verisign.com"); err != nil {
			t.Fatal(err)
		}
	}
	verisign := time.Since(start)

	// unmeasured falls back to 1/sec, three burst tokens then wait
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

// An explicit -rate should win over a measured budget.
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
