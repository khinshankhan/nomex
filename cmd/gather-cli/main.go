package main

import (
	"fmt"
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

func checkDomainAvailabilityRDAP(domainName string) {
	domain, err := client.QueryDomain(domainName)

	if err == nil {
		fmt.Printf("Handle=%s Domain=%s\n", domain.Handle, domain.LDHName)
	} else {
		fmt.Printf("Error: %s\n", err)
	}
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
		checkDomainAvailabilityRDAP(domainName)
	}
}
