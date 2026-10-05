package data

import (
	"context"
	"fmt"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// StoreResult persists one check result:
//
//   - an answer updates checks
//   - a failure about the domain itself writes blocked
//   - any other failure writes attempts and defers the row
//
// A failure never reaches checks.status, so a timeout can't be stored as an
// answer.
func (db *DB) StoreResult(ctx context.Context, res check.Result, failures int) error {
	now := time.Now().UTC()

	if res.Err != nil {
		return db.storeFailure(ctx, res, now, failures)
	}

	source := res.Source
	return db.UpsertCheckResult(ctx, sqlcgen.UpsertCheckResultParams{
		Domain:       res.Domain,
		Status:       string(res.Status),
		Source:       nullable(source),
		CheckedAt:    &sqltime.UTC{Time: now},
		FreshUntil:   sqltime.At(check.FreshUntil(now, res.Status, res.Expiration)),
		Expiration:   sqltime.Ptr(res.Expiration),
		RegisteredAt: sqltime.Ptr(res.Registered),
		Server:       nullable(res.Server),
		Stale:        res.Stale,
	})
}

// storeFailure records an attempt, blocks the domain if the error is about it,
// and pushes fresh_until forward so the sweep stops returning the row.
func (db *DB) storeFailure(ctx context.Context, res check.Result, now time.Time, failures int) error {
	var retryAfter *sqltime.UTC
	if res.RetryAfter > 0 {
		t := sqltime.At(now.Add(res.RetryAfter))
		retryAfter = &t
	}

	err := db.RecordAttempt(ctx, sqlcgen.RecordAttemptParams{
		Domain:      res.Domain,
		AttemptedAt: sqltime.At(now),
		ErrorKind:   res.ErrKind,
		Retryable:   res.Retryable,
		RetryAfter:  retryAfter,
	})
	if err != nil {
		return fmt.Errorf("record attempt for %s: %w", res.Domain, err)
	}

	// the checker decides what's blockable, data shouldn't know every
	// protocol's errors
	if res.Blockable() {
		if err := db.BlockDomain(ctx, sqlcgen.BlockDomainParams{
			Domain:    res.Domain,
			Reason:    res.BlockReason,
			BlockedAt: sqltime.At(now),
		}); err != nil {
			return fmt.Errorf("block %s: %w", res.Domain, err)
		}
	}

	// defer even when blocked, otherwise the row keeps getting re-read until
	// the block lands
	if err := db.DeferCheck(ctx, sqlcgen.DeferCheckParams{
		Domain:     res.Domain,
		FreshUntil: sqltime.At(check.Backoff(now, failures, res.RetryAfter)),
	}); err != nil {
		return fmt.Errorf("defer %s: %w", res.Domain, err)
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
