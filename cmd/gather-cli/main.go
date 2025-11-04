package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/openrdap/rdap"
	"github.com/openrdap/rdap/bootstrap"
	"github.com/openrdap/rdap/bootstrap/cache"
)

var client *rdap.Client
var AppName = ""

func init() {
	httpClient := &http.Client{Timeout: 10 * time.Second}

	// bootstrapper with a disk cache to avoid re-downloading IANA files
	b := &bootstrap.Client{
		HTTP: httpClient,
	}
	// uses ~/.openrdap by default https://github.com/openrdap/rdap/blob/master/bootstrap/cache/disk_cache.go
	b.Cache = cache.NewDiskCache()

	client = &rdap.Client{
		HTTP:      httpClient,
		Bootstrap: b,
		UserAgent: userAgent(),
	}
}

func checkDomainAvailabilityRDAP(domainName string) (bool, error) {
	_, err := client.QueryDomain(domainName)

	if err == nil {
		return true, nil
	} else {
		return false, err
	}
}

func checkDomainAvailabilityDnsLookup(domainName string) (bool, error) {
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

func main() {

	// list of domain names to check
	domainNames := []string{
		// taken
		"khinshankhan.com",
		// taken but reserved
		"example.com",
		// not taken
		"thisdomaindoesntexistcurrentlybecauseichecked.com",
	}

	for _, domainName := range domainNames {
		var taken bool = true
		var err error = nil

		dnsTaken, dnsErr := checkDomainAvailabilityDnsLookup(domainName)
		if dnsErr != nil || dnsTaken {
			taken = dnsTaken
			err = dnsErr
		} else {
			fmt.Println("DNS says available, checking RDAP...")
			taken, err = checkDomainAvailabilityRDAP(domainName)
		}

		fmt.Printf("Domain=%s Taken=%t Error=%v\n", domainName, taken, err)
	}
}
