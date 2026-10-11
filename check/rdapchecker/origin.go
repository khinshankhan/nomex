package rdapchecker

import (
	"context"
)

// Origin returns the scheme://host serving domain, without querying it. This
// is what lets the sweep skip throttled servers, since rdap.Result.Server is
// only known after a query.
//
// It reads the same cached IANA bootstrap registry the checker queries with, so
// resolving every domain is basically free.
//
// An empty string means no server is published for the suffix. That isn't an
// error here, the query itself will fail with ErrNoServer, which blocks the
// domain.
func (c *Checker) Origin(ctx context.Context, domain string) (string, error) {
	match, err := c.resolver.LookupDomain(ctx, domain)
	if err != nil {
		return "", err
	}
	if !match.Found() {
		return "", nil
	}
	return originOf(match.URLs[0]), nil
}
