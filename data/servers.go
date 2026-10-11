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

// RecordServer updates the answering server's state from one result, and
// returns when it should next be tried (zero if it isn't throttled).
func (db *DB) RecordServer(ctx context.Context, res check.Result) (time.Time, error) {
	if res.Origin == "" {
		// nothing answered
		return time.Time{}, nil
	}

	now := sqltime.Now()

	if !res.Failed() {
		return time.Time{}, db.RecordServerSuccess(ctx, sqlcgen.RecordServerSuccessParams{
			Origin:      res.Origin,
			LastSuccess: &now,
		})
	}

	// most 429s have no Retry-After (Google's don't), so back off by streak
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

// throttling reports whether a failure means backing off the whole server.
// Retryable ones are about the server, permanent ones about the domain.
func throttling(res check.Result) bool {
	return res.Retryable
}

// ThrottleUntil marks an origin as unavailable until t.
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
