package check

import (
	"context"
	"time"
)

// Checker establishes what a registry says about a domain.
//
// Implementations are per-protocol: RDAP today, plausibly WHOIS for suffixes
// that publish no RDAP server, or DNS as a cheap approximation.
//
// A Checker never reports a failure as a status. "We could not find out" and
// "the answer is no" are different claims, and conflating them is what banned
// 42k domains in November.
type Checker interface {
	// Check asks about one domain. A failure is reported in Result.Err, and
	// Result.Status is StatusUnknown, never StatusNotFound.
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
	// them. Both nil for an available domain, and nil from any checker that
	// cannot see them -- DNS, for instance, can establish that a name is
	// registered without learning when the registration ends.
	Expiration *time.Time
	Registered *time.Time

	// Server is the full URL that answered, kept as provenance. Origin is its
	// scheme://host, which is what per-server state is keyed on.
	Server string
	Origin string

	// Stale reports that the answer may have come from a server that is no
	// longer authoritative.
	Stale bool

	// Err is set when the check established nothing. Never store this as a
	// status.
	Err        error
	Retryable  bool
	RetryAfter time.Duration

	// ErrKind names the failure for attempts.error_kind. Protocol-specific,
	// stored as text for diagnosis rather than interpreted.
	ErrKind string

	// BlockReason is set only when the failure is a fact about the DOMAIN --
	// no server published for its suffix, a malformed name, a deliberate
	// refusal -- rather than about a server or about us.
	//
	// Deciding this belongs to the checker, which knows its own error
	// taxonomy. It is deliberately NOT "the error is not retryable": for RDAP
	// that also covers unclassified failures and redirect problems, and
	// blocking on those writes a domain off permanently because of someone
	// else's misconfiguration.
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
