package rdapchecker

import (
	"context"
)

// Origin returns the scheme://host serving domain, from the cached bootstrap
// registry, without querying it. Empty means no server for the suffix.
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
