// Package dnschecker checks domain registration with DNS.
//
// DNS only proves the positive: nameservers mean registered, but no records
// proves nothing, so a miss is StatusUnknown. It saves RDAP queries on dense
// suffixes (1-2 character .com and .net are all taken, .dev about 5%).
//
// NOTE: a DNS answer has no expiry, so the row gets the default TTL.
package dnschecker

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/khinshankhan/nomex/check"
)

// DefaultTimeout bounds a single lookup. Measured latency was 178-410ms.
const DefaultTimeout = 5 * time.Second

// lookupFunc is the nameserver lookup, swappable for tests.
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

// Check looks up nameservers for domain:
//
//   - records exist: StatusRegistered
//   - authoritative no: StatusUnknown
//   - lookup failed: StatusUnknown with a retryable error
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

	// NS, not host records, a delegated domain may have no addresses
	ns, err := c.lookup(ctx, domain)
	switch {
	case err == nil && len(ns) > 0:
		out.Status = check.StatusRegistered
		return out

	case err == nil:
		return out

	case isNotFound(err):
		// doesn't resolve, but could still be registered
		return out

	default:
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

// Checker satisfies check.Checker.
var _ check.Checker = (*Checker)(nil)
