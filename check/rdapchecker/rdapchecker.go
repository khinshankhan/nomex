// Package rdapchecker checks domains against RDAP (RFC 7480, 9082, 9083).
//
// A thin adapter over github.com/khinshankhan/rdap: query, map the result,
// read two event dates. The protocol itself lives in that library.
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

// blockable is the allowlist of error kinds that describe the DOMAIN rather
// than a server or our own configuration.
//
// Deliberately not !rdap.IsRetryable(err): Retryable() is a five-kind
// allowlist, so "not retryable" also covers ErrUnknown (an unclassified
// failure), ErrTooManyRedirects, ErrRedirectRefused, ErrRedirectLoop and
// ErrNotSupported. Blocking on those would write a domain off permanently
// because of a redirect loop on someone else's server.
var blockable = map[rdap.ErrorKind]string{
	rdap.ErrNoServer:     "no RDAP server published for this suffix",
	rdap.ErrInvalidQuery: "malformed domain name",
	rdap.ErrRefused:      "server refused the query",
}

// Checker resolves domains against RDAP.
type Checker struct {
	client *rdap.Client
}

// NewChecker returns a Checker using the given user agent, which the library
// requires: registries rate-limit or block anonymous clients, and an operator
// who wants to complain should be able to find us.
func New(userAgent string, opts ...rdap.ClientOption) (*Checker, error) {
	// A disk-cached resolver, so repeated invocations do not refetch the IANA
	// registry. Without it every process start costs a round trip to IANA.
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

// Check asks the registry about one domain.
//
// A failure is reported in Result.Err rather than as a status, so the caller
// cannot accidentally store "we could not find out" as "the answer is no".
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
		// A body that will not parse is not a reason to discard the status:
		// the registry answered, we just could not read the dates.
		if events, err := parseEvents(res.Body); err == nil {
			out.Expiration = events.expiration
			out.Registered = events.registration
		}
		return out
	}
}

// classify turns a failed query into a Result, deciding whether the failure is
// a fact about the domain.
func classify(out check.Result, res rdap.Result) check.Result {
	out.Status = check.StatusUnknown
	out.Err = res.Err
	out.Retryable = res.Retryable
	out.RetryAfter = res.RetryAfter

	// ok is false when the error is not an *rdap.Error at all -- a cancelled
	// context, say -- which is never a fact about the domain.
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
// "last update of RDAP database" is deliberately ignored: it describes the
// freshness of the answer, not the domain, and is easy to mistake for the
// latter.
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
		// Registries differ on precision -- Verisign sends whole seconds,
		// Google's registry sends milliseconds -- and RFC 3339 covers both.
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

// originOf reduces a full request URL to scheme://host.
//
// rdap.Result.Server is the whole URL including the object path, so using it
// directly as a server identity would produce one entry per domain.
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

// Compile-time proof that Checker satisfies the interface, so a signature
// change in check.Checker breaks here rather than at the call site.
var _ check.Checker = (*Checker)(nil)
