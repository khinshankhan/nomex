package main

import (
	"fmt"
	"os"
	"time"
)

func checkDomain(domainName string) (int, error) {
	taken, err := DnsCheck(domainName)
	if err != nil {
		// some error occurred during check
		fmt.Println("DNS check error for", domainName, ":", err)
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

func verifyDomains(saveDomainCode func(string, int, *time.Time), domainNames []string) {
	for i, domainName := range domainNames {
		fmt.Printf("(%d/%d) ", i+1, len(domainNames))
		fmt.Println("Checking domain:", domainName)

		// TODO: circle back to check errors
		// TODO: check code to avoid getting ratelimited/ other issues
		code, err := checkDomain(domainName)
		if err != nil {
			fmt.Println("Error checking domain", domainName, ":", err)
			panic(err)
		}
		fmt.Println("Domain:", domainName, "Code:", code)
		t := time.Now()
		saveDomainCode(domainName, code, &t)
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
		err = bulkEnsureRows(db, generateCandidates([]string{"net"}))
		if err != nil {
			panic(err)
		}

		// NOTE: this loads any pre existing pending domains from the database
		pendingDomains, err := loadPendingFromDB(db)
		if err != nil {
			panic(err)
		}

		// list of domain names to check
		candidates := filterBadCandidates(pendingDomains)
		verifyDomains(func(domainName string, code int, t *time.Time) {
			err := saveCode(db, domainName, code, t)
			if err != nil {
				panic(err)
			}
		}, candidates)
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
