package rdapchecker

import (
	"context"
	"fmt"

	"github.com/khinshankhan/rdap/bootstrap"
)

// OriginResolver reports which RDAP server serves a domain, without querying
// it. This is what lets the sweep skip throttled servers, since
// rdap.Result.Server is only known after a query.
//
// The IANA bootstrap registry is fetched once (128ms for the first lookup,
// 1-5us after), so resolving every domain is basically free.
type OriginResolver struct {
	resolver *bootstrap.Resolver
}

// NewOriginResolver builds a resolver over the IANA bootstrap registry, cached
// on disk under os.UserCacheDir()/rdap so each run doesn't refetch it. Losing
// the cache just costs one request.
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
// An empty string means no server is published for the suffix. That isn't an
// error here, the query itself will fail with ErrNoServer, which blocks the
// domain.
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
