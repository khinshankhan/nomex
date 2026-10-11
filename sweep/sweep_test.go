package sweep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// openDB returns a DB with every migration's Up block applied.
func openDB(t *testing.T) *data.DB {
	t.Helper()

	db, err := data.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	files, err := filepath.Glob("../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("find migrations: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		up, _, _ := strings.Cut(string(b), "-- +goose Down")
		if err := db.Exec(t.Context(), up); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return db
}

type fixedOrigin string

func (o fixedOrigin) Origin(context.Context, string) (string, error) {
	return string(o), nil
}

// A throttled registry's domains should be set aside until it reopens, not
// re-claimed and re-skipped as fast as the loop can spin.
func TestThrottledDomainsAreNotReclaimed(t *testing.T) {
	db := openDB(t)
	const origin = "https://rdap.example"

	if _, err := db.SeedCheck(t.Context(), sqlcgen.SeedCheckParams{
		Domain:     "a.dev",
		FreshUntil: sqltime.At(time.Now().Add(-time.Hour)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.ThrottleUntil(t.Context(), origin, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("throttle: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	var skips atomic.Int64
	_, err := Run(ctx, db, newFake(check.Result{Status: check.StatusNotFound}), Options{
		Limits:  Limits{Every: time.Microsecond, Burst: 100},
		Origins: fixedOrigin(origin),
		Progress: func(s Stat) {
			if s.Skipped {
				skips.Add(1)
			}
		},
	})
	if !Cancelled(err) {
		t.Fatalf("Run: %v", err)
	}
	if n := skips.Load(); n != 1 {
		t.Errorf("skipped %d times, want 1", n)
	}
}

// Once a result can't be saved, the rest of the batch shouldn't be sent to the
// registry only to be thrown away.
func TestStoreFailureStopsTheBatch(t *testing.T) {
	db := openDB(t)

	// unseeded, so recording the failed attempt breaks its foreign key
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

// A DNS hit never reached the RDAP server, so it says nothing about that
// server's health.
func TestPreCheckHitLeavesServerStateAlone(t *testing.T) {
	db := openDB(t)
	const origin = "https://rdap.example"

	if _, err := db.SeedCheck(t.Context(), sqlcgen.SeedCheckParams{
		Domain:     "a.dev",
		FreshUntil: sqltime.At(time.Now().Add(-time.Hour)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// a failure streak whose throttle has already lapsed, so nothing is skipped
	if err := db.ThrottleUntil(t.Context(), origin, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("throttle: %v", err)
	}

	_, err := CheckDomains(t.Context(), db, newFake(check.Result{Status: check.StatusNotFound}), []string{"a.dev"}, Options{
		Limits:   Limits{Every: time.Microsecond, Burst: 100},
		Origins:  fixedOrigin(origin),
		PreCheck: newFake(check.Result{Status: check.StatusRegistered, Source: "dns"}),
	})
	if err != nil {
		t.Fatalf("CheckDomains: %v", err)
	}

	if n := db.ServerFailures(t.Context(), origin); n != 1 {
		t.Errorf("failures = %d, want 1 (untouched by a DNS answer)", n)
	}
}
