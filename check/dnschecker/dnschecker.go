// Package dnschecker establishes domain registration from DNS.
//
// DNS is an approximation, and only in one direction. Nameserver records prove
// a domain is registered; their absence proves nothing, because parked,
// expired-but-in-grace and registered-not-delegated domains are all common.
// So a lookup that finds nothing reports StatusUnknown rather than
// StatusNotFound, and the caller has to ask RDAP.
//
// What it buys is skipping RDAP for domains that are taken, which pays in
// proportion to how registered a suffix is. Measured on this database: .com and
// .net at 1-2 characters are 100% registered, .dev is 5%. So it is worth
// running in front of RDAP for the dense suffixes and a waste for the sparse
// ones.
//
// What it costs, beyond the lookup: a DNS answer carries no expiration date, so
// a row established this way falls back to the default TTL and returns for a
// full RDAP check later. It defers RDAP work rather than removing it.
package dnschecker

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/khinshankhan/nomex/check"
)

// DefaultTimeout bounds a single lookup. Measured latency was 178-410ms, so
// this is generous enough for a slow resolver and short enough that a hung one
// does not hold a worker.
const DefaultTimeout = 5 * time.Second

// lookupFunc is the nameserver lookup, injectable so the classification can be
// tested without a network. net.Resolver's Dial hook cannot fabricate results,
// only supply a transport, so faking has to happen at this level.
type lookupFunc func(ctx context.Context, domain string) ([]*net.NS, error)

// Checker resolves domains against DNS.
type Checker struct {
	lookup  lookupFunc
	timeout time.Duration
}

// Option configures a Checker.
type Option func(*Checker)

// WithResolver uses a specific resolver rather than the system one.
func WithResolver(r *net.Resolver) Option {
	return func(c *Checker) {
		c.lookup = r.LookupNS
	}
}

// withLookup replaces the lookup entirely. Test-only.
func withLookup(f lookupFunc) Option {
	return func(c *Checker) {
		c.lookup = f
	}
}

// WithTimeout bounds each lookup.
func WithTimeout(d time.Duration) Option {
	return func(c *Checker) {
		c.timeout = d
	}
}

// New returns a Checker.
func New(opts ...Option) *Checker {
	c := &Checker{lookup: net.DefaultResolver.LookupNS, timeout: DefaultTimeout}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Source implements check.Checker.
func (c *Checker) Source() string {
	return "dns"
}

// Check looks up nameservers for domain.
//
// Three outcomes, and keeping them apart is the whole point:
//
//   - records exist        -> StatusRegistered, no expiry
//   - authoritative "no"   -> StatusUnknown, because DNS cannot see a
//     registered domain that is not delegated
//   - lookup failed        -> StatusUnknown with a retryable error
//
// The second and third both report StatusUnknown but differ in Err: a
// not-found is a real answer that happens to be uninformative, while a resolver
// failure established nothing at all. Conflating them is how a DNS hiccup
// becomes a stored result.
func (c *Checker) Check(ctx context.Context, domain string) check.Result {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	out := check.Result{
		Domain: domain,
		Source: c.Source(),
		Status: check.StatusUnknown,
	}

	// NS rather than host records: a registered domain may have no addresses
	// but must have nameservers to be delegated, so NS is the closer proxy for
	// registration.
	ns, err := c.lookup(ctx, domain)
	switch {
	case err == nil && len(ns) > 0:
		out.Status = check.StatusRegistered
		return out

	case err == nil:
		// No error and no records: nothing to conclude.
		return out

	case isNotFound(err):
		// Authoritative "this name does not resolve". Still not "available":
		// see the package comment.
		return out

	default:
		// A resolver failure establishes nothing. Retryable so the row is
		// deferred rather than treated as an answer -- the old code returned
		// 500 here and stored it alongside real results.
		out.Err = err
		out.ErrKind = errKind(err)
		out.Retryable = true
		return out
	}
}

// isNotFound reports an authoritative negative rather than a failure.
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func errKind(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsTimeout:
			return "dns timeout"
		case dnsErr.IsTemporary:
			return "dns temporary failure"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "dns timeout"
	}
	return "dns failure"
}

// Compile-time proof that Checker satisfies the interface.
var _ check.Checker = (*Checker)(nil)
