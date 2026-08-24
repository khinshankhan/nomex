package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// Throttled reports which origins asked us to wait, and until when.
//
// The sweeper reads this once per round rather than per domain: it is a handful
// of rows, and a stale answer only costs one wasted query.
func (db *DB) Throttled(ctx context.Context) (map[string]time.Time, error) {
	rows, err := db.ThrottledServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("throttled servers: %w", err)
	}

	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		if r.RateLimitedUntil != nil {
			out[r.Origin] = r.RateLimitedUntil.Time
		}
	}
	return out, nil
}

// RecordServer updates per-origin state from one result.
//
// A rate limit is a property of the server, not of the domain that happened to
// hit it: when Verisign says Retry-After: 300 that applies to every .com
// lookup. Without somewhere to put that, the knowledge dies with the worker
// that received it and the rest of the pool keeps hammering.
// Returns when the origin should next be tried, or the zero time if it is not
// throttled.
func (db *DB) RecordServer(ctx context.Context, res check.Result) (time.Time, error) {
	if res.Origin == "" {
		// Nothing answered, so there is no server to attribute this to.
		return time.Time{}, nil
	}

	now := sqltime.Now()

	if !res.Failed() {
		return time.Time{}, db.RecordServerSuccess(ctx, sqlcgen.RecordServerSuccessParams{
			Origin:      res.Origin,
			LastSuccess: &now,
		})
	}

	// A server that rate-limits without a Retry-After is the common case --
	// Google's registry sends a bare 429 -- so a wait has to be derived from
	// the failure streak, or nothing ever throttles and the pool keeps
	// hammering a server that has already said no.
	wait := res.RetryAfter
	if wait <= 0 && throttling(res) {
		wait = check.Backoff(time.Now(), db.ServerFailures(ctx, res.Origin), 0).Sub(time.Now())
	}

	var (
		until   *sqltime.UTC
		nextTry time.Time
	)
	if wait > 0 {
		nextTry = time.Now().Add(wait)
		t := sqltime.At(nextTry)
		until = &t
	}

	return nextTry, db.RecordServerFailure(ctx, sqlcgen.RecordServerFailureParams{
		Origin:           res.Origin,
		LastFailure:      &now,
		RateLimitedUntil: until,
	})
}

// throttling reports whether a failure means "stop asking this server for a
// while" rather than "this one domain went wrong".
//
// Retryable covers it: rate limits, timeouts, transport failures and 5xx are
// all properties of the server. A permanent failure -- a malformed name, an
// unserved suffix -- says nothing about whether the server is healthy.
func throttling(res check.Result) bool {
	return res.Retryable
}

// ThrottleUntil marks an origin as unavailable until t, for a server that
// refused without saying when to come back.
func (db *DB) ThrottleUntil(ctx context.Context, origin string, t time.Time) error {
	if origin == "" {
		return nil
	}
	now := sqltime.Now()
	until := sqltime.At(t)
	return db.RecordServerFailure(ctx, sqlcgen.RecordServerFailureParams{
		Origin:           origin,
		LastFailure:      &now,
		RateLimitedUntil: &until,
	})
}

// ServerFailures returns the consecutive failure count for an origin.
func (db *DB) ServerFailures(ctx context.Context, origin string) int {
	s, err := db.ServerState(ctx, origin)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return 0
		}
		return 0
	}
	return int(s.ConsecutiveFailures)
}
