// Package dnschecker checks domain registration with DNS.
//
// DNS only works in one direction. Nameserver records prove a domain is
// registered, but no records proves nothing since parked, expired and
// undelegated domains are common. So a miss is StatusUnknown, not
// StatusNotFound, and RDAP still has to be asked.
//
// It saves RDAP queries on taken domains, so it pays off on dense suffixes. On
// this database .com and .net at 1-2 characters are 100% registered and .dev
// is 5%.
//
// NOTE: a DNS answer has no expiry, so the row gets the default TTL and comes
// back for a full RDAP check later.
package dnschecker

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/khinshankhan/nomex/check"
)

// DefaultTimeout bounds a single lookup. Measured latency was 178-410ms, so
// this leaves room for a slow resolver without letting a hung one hold a
// worker.
const DefaultTimeout = 5 * time.Second

// lookupFunc is the nameserver lookup, swappable so tests don't need a network.
// net.Resolver's Dial hook can only supply a transport, not fake results.
type lookupFunc func(ctx context.Context, domain string) ([]*net.NS, error)

// Checker resolves domains against DNS.
type Checker struct {
	lookup  lookupFunc
	timeout time.Duration
}

// Option configures a Checker.
type Option func(*Checker)

// WithResolver uses a specific resolver instead of the system one.
func WithResolver(r *net.Resolver) Option {
	return func(c *Checker) {
		c.lookup = r.LookupNS
	}
}

// withLookup replaces the lookup entirely, for tests.
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

// Check looks up nameservers for domain. There are three outcomes:
//
//   - records exist: StatusRegistered, no expiry
//   - authoritative "no": StatusUnknown, since DNS can't see a registered
//     domain that isn't delegated
//   - lookup failed: StatusUnknown with a retryable error
//
// The last two only differ in Err. A not-found is a real (if useless) answer,
// a resolver failure is no answer at all and must not be stored as one.
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

	// NS instead of host records: a registered domain may have no addresses
	// but needs nameservers to be delegated
	ns, err := c.lookup(ctx, domain)
	switch {
	case err == nil && len(ns) > 0:
		out.Status = check.StatusRegistered
		return out

	case err == nil:
		// no error and no records, nothing to conclude
		return out

	case isNotFound(err):
		// authoritative "does not resolve", still not "available" (see the
		// package comment)
		return out

	default:
		// a resolver failure establishes nothing, so retry later
		// NOTE: the old code returned a 500 here and stored it with the real
		// results
		out.Err = err
		out.ErrKind = errKind(err)
		out.Retryable = true
		return out
	}
}

// isNotFound reports an authoritative negative, as opposed to a failure.
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

// Checker satisfies check.Checker.
var _ check.Checker = (*Checker)(nil)
