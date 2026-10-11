// Package rdapchecker checks domains against RDAP, through
// github.com/khinshankhan/rdap.
package rdapchecker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/rdap"
	"github.com/khinshankhan/rdap/bootstrap"
)

// blockable lists the error kinds that are about the domain itself.
//
// NOTE: not !rdap.IsRetryable(err), which also covers server problems like
// redirect loops and ErrNotSupported.
var blockable = map[rdap.ErrorKind]string{
	rdap.ErrNoServer:     "no RDAP server published for this suffix",
	rdap.ErrInvalidQuery: "malformed domain name",
	rdap.ErrRefused:      "server refused the query",
}

// Checker resolves domains against RDAP.
type Checker struct {
	client   *rdap.Client
	resolver *bootstrap.Resolver
}

// Option configures a Checker.
type Option func(*options)

type options struct {
	bootstrap []bootstrap.Option
	client    []rdap.ClientOption
}

// WithBootstrap configures the bootstrap resolver, applied after the defaults.
func WithBootstrap(opts ...bootstrap.Option) Option {
	return func(o *options) {
		o.bootstrap = append(o.bootstrap, opts...)
	}
}

// WithClient configures the RDAP client, applied after the defaults.
func WithClient(opts ...rdap.ClientOption) Option {
	return func(o *options) {
		o.client = append(o.client, opts...)
	}
}

// New returns a Checker that identifies itself with userAgent.
func New(userAgent string, opts ...Option) (*Checker, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	// so each run doesn't refetch the IANA registry
	cache, err := bootstrap.NewDiskCache("")
	if err != nil {
		return nil, fmt.Errorf("bootstrap cache: %w", err)
	}
	resolver, err := bootstrap.NewResolver(append([]bootstrap.Option{bootstrap.WithCache(cache)}, o.bootstrap...)...)
	if err != nil {
		return nil, fmt.Errorf("bootstrap resolver: %w", err)
	}

	defaults := []rdap.ClientOption{
		rdap.WithUserAgent(userAgent),
		rdap.WithResolver(resolver),
	}
	client, err := rdap.New(append(defaults, o.client...)...)
	if err != nil {
		return nil, fmt.Errorf("rdap client: %w", err)
	}
	return &Checker{client: client, resolver: resolver}, nil
}

// Source implements check.Checker.
func (c *Checker) Source() string {
	return "rdap"
}

// Check asks the registry about one domain.
func (c *Checker) Check(ctx context.Context, domain string) check.Result {
	res, err := c.client.QueryDomain(ctx, domain)

	out := check.Result{
		Domain: domain,
		Server: res.Server,
		Origin: originOf(res.Server),
		Stale:  res.Stale,
		Source: c.Source(),
	}

	switch {
	case err != nil, res.Status == rdap.StatusUnknown:
		if err != nil {
			res.Err = err
		}
		return classify(out, res)

	case res.Status == rdap.StatusNotFound:
		out.Status = check.StatusNotFound
		return out

	default:
		out.Status = check.StatusRegistered
		// still registered if the dates don't parse
		if events, err := parseEvents(res.Body); err == nil {
			out.Expiration = events.expiration
			out.Registered = events.registration
		}
		return out
	}
}

// classify fills in a failed Result, including whether it blocks the domain.
func classify(out check.Result, res rdap.Result) check.Result {
	out.Status = check.StatusUnknown
	out.Err = res.Err
	out.Retryable = res.Retryable
	out.RetryAfter = res.RetryAfter

	// not an *rdap.Error (eg a cancelled context), never about the domain
	kind, ok := rdap.KindOf(out.Err)
	if !ok {
		out.ErrKind = "non-rdap failure"
		return out
	}

	out.ErrKind = kind.String()
	out.BlockReason = blockable[kind]
	return out
}

// events holds the RFC 9083 event dates nomex stores.
type events struct {
	expiration   *time.Time
	registration *time.Time
}

// parseEvents reads the event dates out of an RDAP domain object.
//
// NOTE: "last update of RDAP database" is about the answer, not the domain.
func parseEvents(body []byte) (events, error) {
	var doc struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return events{}, fmt.Errorf("parse rdap body: %w", err)
	}

	var out events
	for _, e := range doc.Events {
		// covers both Verisign's seconds and Google's milliseconds
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			continue
		}
		t = t.UTC()

		switch e.Action {
		case "expiration":
			out.expiration = &t
		case "registration":
			out.registration = &t
		}
	}
	return out, nil
}

// originOf reduces a request URL to scheme://host.
func originOf(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Checker satisfies check.Checker.
var _ check.Checker = (*Checker)(nil)
