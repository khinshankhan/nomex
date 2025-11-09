package dnsresolver

import (
	"context"
	"net"
	"time"
)

type Resolver struct {
	Timeout time.Duration
}

type Config struct {
	Timeout time.Duration
}

func New(cfg Config) *Resolver {
	return &Resolver{
		Timeout: cfg.Timeout,
	}
}

/** Check checks if a domain name is taken using DNS lookup. Returns true if taken, false if available, and error if any
 * other error occurs.
 *
 * NOTE: This method may produce false negatives for domains that exist but have no DNS records so it is recommended to
 * use RDAP check or another method as a secondary check.
 */
func (r *Resolver) Check(ctx context.Context, domain string) (bool, error) {
	// optional per-call bound if caller didn't set one
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	// net.Resolver has Dial context options but for simplicity we use LookupHost (for now)
	_, err := net.DefaultResolver.LookupHost(ctx, domain)
	if err != nil {
		// treat "no such host" as available though the domain is available
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.Err == "no such host" {
			return false, nil
		}
		// some other error occurred
		return false, err
	}
	// domain is taken
	return true, nil
}
