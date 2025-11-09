package rdapclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/openrdap/rdap"
	"github.com/openrdap/rdap/bootstrap"
	"github.com/openrdap/rdap/bootstrap/cache"
)

type Client struct {
	rc *rdap.Client
}

type Config struct {
	UserAgent  string
	HTTPClient *http.Client // optional, default with timeout if nil
}

func New(cfg Config) (*Client, error) {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	// bootstrapper with a disk cache to avoid re-downloading IANA files
	b := &bootstrap.Client{
		HTTP: httpClient,
	}

	// uses ~/.openrdap by default, they say default but it's not configurable :/
	// https://github.com/openrdap/rdap/blob/master/bootstrap/cache/disk_cache.go
	b.Cache = cache.NewDiskCache()

	rc := &rdap.Client{
		HTTP:      httpClient,
		Bootstrap: b,
		UserAgent: cfg.UserAgent,
	}
	return &Client{rc: rc}, nil
}

// QueryDomainRaw preserves RDAP problem details instead of collapsing them.
func (c *Client) QueryDomainRaw(domainName string) (*rdap.Response, error) {
	req := &rdap.Request{
		Type:  rdap.DomainRequest,
		Query: domainName,
	}

	// TODO: allow caller to pass context?
	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Second*30)
	defer cancelFunc()

	req = req.WithContext(ctx)
	resp, err := c.rc.Do(req)

	return resp, err
}

/** Check checks if a domain name is taken using RDAP. Returns 200 if taken, 404 if available, and a different code if
 * any other error occurs.
 *
 * NOTE: This method is more reliable than DNS check as it queries the authoritative source for domain registration
 * data, this method is preferred over DNS check however it may be slower due to network latency and RDAP server
 * response times and it can be rate limited by RDAP servers... it's also bad actor to spam RDAP servers with requests.
 */
func (c *Client) Check(domainName string) (int, error) {
	_, err := c.QueryDomainRaw(domainName)

	// registered
	if err == nil {
		return 200, nil
	}

	// map typed error to codes which indicate availability
	var ce *rdap.ClientError
	if errors.As(err, &ce) {
		switch ce.Type {
		case rdap.ObjectDoesNotExist:
			// not found
			return 404, nil

		// lowkey I don't really know what all these codes mean in practice, but this is my best guess
		case rdap.InputError:
			return 400, err
		case rdap.BootstrapNotSupported:
			return 501, err
		case rdap.BootstrapNoMatch, rdap.WrongResponseType, rdap.RDAPServerError:
			return 502, err
		case rdap.NoWorkingServers:
			return 503, err
		default:
			return 502, err
		}
	}

	// context classification first (to avoid being masked by *url.Error -> net.Error)
	if errors.Is(err, context.DeadlineExceeded) {
		return 504, err
	}
	if errors.Is(err, context.Canceled) {
		return 499, err
	}

	// network errors = service unavailable / gateway timeout
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return 504, err
		}
		// temporary connect/ reset/ dns hiccup etc
		return 503, err
	}

	// unknown error, treat as upstream error because clearly we didn't cause it (probably)
	return 502, err
}
