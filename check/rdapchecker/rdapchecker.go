// Package rdapchecker checks domains against RDAP (RFC 7480, 9082, 9083).
//
// It's a thin adapter over github.com/khinshankhan/rdap, which does the actual
// protocol work.
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

// blockable lists the error kinds that are about the domain itself, not a
// server or our config.
//
// NOTE: this is not !rdap.IsRetryable(err). Retryable only allows five kinds,
// so "not retryable" also covers ErrUnknown, ErrTooManyRedirects,
// ErrRedirectRefused, ErrRedirectLoop and ErrNotSupported. Blocking on those
// would write a domain off for good over a redirect loop on someone else's
// server.
var blockable = map[rdap.ErrorKind]string{
	rdap.ErrNoServer:     "no RDAP server published for this suffix",
	rdap.ErrInvalidQuery: "malformed domain name",
	rdap.ErrRefused:      "server refused the query",
}

// Checker resolves domains against RDAP.
type Checker struct {
	client *rdap.Client
}

// New returns a Checker using the given user agent. The library requires one
// since registries rate limit or block anonymous clients, and an operator
// should be able to reach whoever is querying them.
func New(userAgent string, opts ...rdap.ClientOption) (*Checker, error) {
	// disk cached so each run doesn't refetch the IANA registry
	cache, err := bootstrap.NewDiskCache("")
	if err != nil {
		return nil, fmt.Errorf("bootstrap cache: %w", err)
	}
	resolver, err := bootstrap.NewResolver(bootstrap.WithCache(cache))
	if err != nil {
		return nil, fmt.Errorf("bootstrap resolver: %w", err)
	}

	defaults := []rdap.ClientOption{
		rdap.WithUserAgent(userAgent),
		rdap.WithResolver(resolver),
	}
	client, err := rdap.New(append(defaults, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("rdap client: %w", err)
	}
	return &Checker{client: client}, nil
}

// Source implements check.Checker.
func (c *Checker) Source() string {
	return "rdap"
}

// Check asks the registry about one domain. Failures go in Result.Err, never in
// the status.
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
		// the registry still answered, we just couldn't read the dates
		if events, err := parseEvents(res.Body); err == nil {
			out.Expiration = events.expiration
			out.Registered = events.registration
		}
		return out
	}
}

// classify turns a failed query into a Result and decides whether the failure
// is about the domain.
func classify(out check.Result, res rdap.Result) check.Result {
	out.Status = check.StatusUnknown
	out.Err = res.Err
	out.Retryable = res.Retryable
	out.RetryAfter = res.RetryAfter

	// not an *rdap.Error at all (eg a cancelled context), never about the domain
	kind, ok := rdap.KindOf(out.Err)
	if !ok {
		out.ErrKind = "non-rdap failure"
		return out
	}

	out.ErrKind = kind.String()
	out.BlockReason = blockable[kind]
	return out
}

// events holds the RFC 9083 section 4.5 event dates nomex stores.
type events struct {
	expiration   *time.Time
	registration *time.Time
}

// parseEvents reads the event array out of an RDAP domain object.
//
// NOTE: "last update of RDAP database" is skipped. It's about the answer's
// freshness, not the domain, and easy to mix up.
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
		// Verisign sends whole seconds and Google sends milliseconds, RFC 3339
		// covers both
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

// originOf reduces a full request URL to scheme://host. rdap.Result.Server
// includes the object path, so using it directly would give one entry per
// domain.
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
