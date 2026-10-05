package check

import (
	"context"
	"time"
)

// Checker asks a registry about a domain. There is one per protocol: RDAP, DNS,
// and maybe WHOIS later for suffixes without an RDAP server.
//
// A Checker never reports a failure as a status. "Could not find out" is not
// "not registered", and mixing the two up is what blocked 42k domains in
// November.
type Checker interface {
	// Check asks about one domain. A failure goes in Result.Err with
	// StatusUnknown, never StatusNotFound.
	Check(ctx context.Context, domain string) Result

	// Source names the protocol, for checks.source.
	Source() string
}

// Result is what a check established about one domain.
type Result struct {
	Domain string
	Status Status

	// Source is the protocol that answered, from Checker.Source.
	Source string

	// Expiration and Registered come from the registry when it publishes
	// them. Both are nil for an available domain, and for checkers like DNS
	// that can't see dates.
	Expiration *time.Time
	Registered *time.Time

	// Server is the full URL that answered. Origin is its scheme://host, which
	// per-server state is keyed on.
	Server string
	Origin string

	// Stale reports that the answer may have come from a server that is no
	// longer authoritative.
	Stale bool

	// Err is set when the check established nothing. It is never stored as a
	// status.
	Err        error
	Retryable  bool
	RetryAfter time.Duration

	// ErrKind names the failure for attempts.error_kind. Protocol specific and
	// only kept for debugging.
	ErrKind string

	// BlockReason is set only when the failure is about the domain itself: no
	// server for its suffix, a malformed name, or an explicit refusal.
	//
	// The checker decides this since it knows its own errors.
	// NOTE: this is not the same as "not retryable". For RDAP that also covers
	// unclassified and redirect failures, and blocking on those would write a
	// domain off for good over someone else's misconfiguration.
	BlockReason string
}

// Blockable reports whether the failure permanently disqualifies the domain.
func (r Result) Blockable() bool {
	return r.BlockReason != ""
}

// Failed reports whether the check established nothing.
func (r Result) Failed() bool {
	return r.Err != nil
}
