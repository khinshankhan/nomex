package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

func checkDomain(domainName string) (int, error) {
	taken, err := DnsCheck(domainName)
	if err != nil {
		return 500, err
	}

	// we can trust that the domain is taken
	if taken {
		return 200, nil
	}

	// domain not found via dns, double-check with rdap
	code, err := RdapCheck(domainName)
	return code, err
}

func verifyDomain(db *sql.DB, domainName string) {
	t := time.Now()
	// TODO: circle back to check errors
	// TODO: check code to avoid getting ratelimited/ other issues
	code, err := checkDomain(domainName)
	if err != nil {
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr):
			if dnsErr.IsNotFound {
				// all good, domain is available
				break
			}
			if dnsErr.IsTemporary || dnsErr.IsTimeout {
				// transient resolver issue -> "ban" (or defer) and move on
				banDomain(db, domainName, "temporary DNS failure", &t)
				return
			}
			// Other DNS errors: record reason and return
			banDomain(db, domainName, "dns error: "+dnsErr.Err, &t)
			return

		case errors.Is(err, context.DeadlineExceeded):
			banDomain(db, domainName, "timeout", &t)
			return

		default:
			fmt.Println("checkDomain unexpected error for", domainName, ":", err)
			panic(err)
		}
	}

	fmt.Println("Domain:", domainName, "Code:", code)
	err = saveCode(db, domainName, code, &t)
	if err != nil {
		panic(err)
	}
}

func verifyDomains(db *sql.DB, domainNames []string) {
	for i, domainName := range domainNames {
		fmt.Printf("(%d/%d) ", i+1, len(domainNames))
		fmt.Println("Checking domain:", domainName)
		verifyDomain(db, domainName)
	}
}

// TODO: make this a flag
const CHECK_DOMAINS = false

func main() {
	db, err := openDB("domains.sqlite")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if CHECK_DOMAINS {
		generatedCandidates := generateCandidates([]string{"net"})
		fmt.Println("Generated", len(generatedCandidates), "candidates")
		err = bulkEnsureRows(db, generatedCandidates)
		if err != nil {
			panic(err)
		}

		// NOTE: this loads any pre existing pending domains from the database
		pendingDomains, err := loadPendingFromDB(db)
		if err != nil {
			panic(err)
		}
		fmt.Println("Loaded", len(pendingDomains), "candidates")

		// list of domain names to check
		candidates := filterBadCandidates(db, pendingDomains)
		fmt.Println("Filtered to", len(candidates), "candidates")
		verifyDomains(db, candidates)
	}

	availableDomains, err := loadAvailableFromDB(db)
	if err != nil {
		panic(err)
	}

	f, err := os.Create("available-domains.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	for _, d := range availableDomains {
		f.WriteString(d + "\n")
	}
}
