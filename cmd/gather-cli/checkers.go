package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/openrdap/rdap"
	"github.com/openrdap/rdap/bootstrap"
	"github.com/openrdap/rdap/bootstrap/cache"
)

var rdapClient *rdap.Client

func init() {
	httpClient := &http.Client{Timeout: 10 * time.Second}

	// bootstrapper with a disk cache to avoid re-downloading IANA files
	b := &bootstrap.Client{
		HTTP: httpClient,
	}
	// uses ~/.openrdap by default https://github.com/openrdap/rdap/blob/master/bootstrap/cache/disk_cache.go
	b.Cache = cache.NewDiskCache()

	rdapClient = &rdap.Client{
		HTTP:      httpClient,
		Bootstrap: b,
		UserAgent: userAgent(),
	}
}

// queryDomainRaw preserves RDAP problem details instead of collapsing them.
func queryDomainRaw(c *rdap.Client, domain string) (*rdap.Response, error) {
	req := &rdap.Request{
		Type:  rdap.DomainRequest,
		Query: domain,
	}

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Second*30)
	defer cancelFunc()

	req = req.WithContext(ctx)
	resp, err := c.Do(req)

	return resp, err
}

/** RdapCheck checks if a domain name is taken using RDAP. Returns 200 if taken, 404 if available, and a different code
 * if any other error occurs.
 *
 * NOTE: This method is more reliable than DNS check as it queries the authoritative source for domain registration
 * data, this method is preferred over DNS check however it may be slower due to network latency and RDAP server
 * response times and it can be rate limited by RDAP servers... it's also bad actor to spam RDAP servers with requests.
 */
func RdapCheck(domainName string) (int, error) {
	_, err := queryDomainRaw(rdapClient, domainName)

	// registered
	if err == nil {
		return 200, nil
	}

	// map typed error to availability
	if err != nil {
		var ce *rdap.ClientError
		if errors.As(err, &ce) {
			fmt.Println("RDAP ClientError:", ce)
			switch ce.Type {
			case rdap.ObjectDoesNotExist:
				// not found
				return 404, nil
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

	// unknown error, treat as upstream error
	return 502, err
}

/** DnsCheck checks if a domain name is taken using DNS lookup. Returns true if taken, false if available, and error if
 * any other error occurs.
 *
 * NOTE: This method may produce false negatives for domains that exist but have no DNS records so it is recommended to
 * use RDAP check or another method as a secondary check.
 */
func DnsCheck(domainName string) (bool, error) {
	_, err := net.LookupHost(domainName)
	if err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.Err == "no such host" {
			// domain is available
			return false, nil
		}
		// some other error occurred
		return false, err
	}
	// domain is taken
	return true, nil
}
