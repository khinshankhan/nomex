package rdapchecker

import (
	"context"
	"fmt"

	"github.com/khinshankhan/rdap/bootstrap"
)

// OriginResolver reports which RDAP server serves a domain, without querying
// it.
//
// This is what makes per-server throttling possible: rdap.Result.Server is only
// known after a query, which is too late to decide whether to send one. The
// IANA bootstrap registry is one file, fetched once -- measured at 128ms for
// the first lookup and 1-5us after -- so resolving every domain costs nothing.
type OriginResolver struct {
	resolver *bootstrap.Resolver
}

// NewOriginResolver builds a resolver over the IANA bootstrap registry,
// cached on disk under os.UserCacheDir()/rdap.
//
// Without the cache every invocation refetches the registry from IANA -- about
// 128ms, and traffic they did not need to serve. The documents are freely
// refetchable, so losing the cache costs one request.
func NewOriginResolver(opts ...bootstrap.Option) (*OriginResolver, error) {
	cache, err := bootstrap.NewDiskCache("")
	if err != nil {
		return nil, fmt.Errorf("bootstrap cache: %w", err)
	}

	r, err := bootstrap.NewResolver(append([]bootstrap.Option{bootstrap.WithCache(cache)}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("bootstrap resolver: %w", err)
	}
	return &OriginResolver{resolver: r}, nil
}

// Origin returns the scheme://host serving domain.
//
// An empty string means no server is published for the suffix -- not an error:
// the sweeper cannot check the domain, and the query will fail with
// ErrNoServer, which is one of the three kinds that legitimately blocks it.
func (o *OriginResolver) Origin(ctx context.Context, domain string) (string, error) {
	match, err := o.resolver.LookupDomain(ctx, domain)
	if err != nil {
		return "", err
	}
	if !match.Found() {
		return "", nil
	}
	return originOf(match.URLs[0]), nil
}
