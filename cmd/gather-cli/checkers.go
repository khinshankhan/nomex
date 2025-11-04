package main

import (
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

/** RdapCheck checks if a domain name is taken using RDAP. Returns true if taken, false if available, and error if any
 * other error occurs.
 *
 * NOTE: This method is more reliable than DNS check as it queries the authoritative source for domain registration
 * data, this method is preferred over DNS check however it may be slower due to network latency and RDAP server
 * response times and it can be rate limited by RDAP servers... it's also bad actor to spam RDAP servers with requests.
 */
func RdapCheck(domainName string) (bool, error) {
	_, err := rdapClient.QueryDomain(domainName)

	if err == nil {
		return true, nil
	} else {
		return false, err
	}
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
