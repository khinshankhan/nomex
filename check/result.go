package check

import (
	"context"
	"time"
)

// Checker asks a registry about a domain, one per protocol.
//
// NOTE: a failure is never a status. "Couldn't find out" isn't "not
// registered".
type Checker interface {
	// Check asks about one domain. A failure goes in Result.Err with
	// StatusUnknown.
	Check(ctx context.Context, domain string) Result

	// Source names the protocol.
	Source() string
}

// Result is what a check established about one domain.
type Result struct {
	Domain string
	Status Status

	Source string

	// Expiration and Registered are nil when the registry doesn't say, and
	// always for DNS.
	Expiration *time.Time
	Registered *time.Time

	// Server is the URL that answered, Origin its scheme://host.
	Server string
	Origin string

	// Stale means the server may no longer be authoritative.
	Stale bool

	// Err is set when the check established nothing.
	Err        error
	Retryable  bool
	RetryAfter time.Duration

	// Throttled means the server is pushing back on our rate, a 429 or a
	// timeout, so the whole server should be backed off, not just the domain.
	Throttled bool

	// ErrKind names the failure, for debugging.
	ErrKind string

	// BlockReason is set only when the failure is about the domain itself: no
	// server for its suffix, a malformed name, or a refusal.
	//
	// NOTE: not the same as "not retryable", which also covers server problems
	// like redirect loops.
	BlockReason string
}

// Blockable reports whether the failure rules the domain out for good.
func (r Result) Blockable() bool {
	return r.BlockReason != ""
}

// Failed reports whether the check established nothing.
func (r Result) Failed() bool {
	return r.Err != nil
}
