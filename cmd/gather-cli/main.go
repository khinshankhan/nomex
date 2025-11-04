package main

import (
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
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

func verifyDomains(db *badger.DB, domainNames []string) {
	for _, domainName := range domainNames {
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
		saveCode(db, domainName, code, &t)
	}
}

// TODO: we need to greatly decrease the candidate space
// conversation on TPH https://discord.com/channels/244230771232079873/244230771232079873/1435352534821765220
func generateCandidates(tlds []string) []string {
	candidates := []string{}

	// TODO: surely there's a more efficient way to do this
	for _, tld := range tlds {
		for c1 := 'a'; c1 <= 'z'; c1++ {
			for c2 := 'a'; c2 <= 'z'; c2++ {
				for c3 := 'a'; c3 <= 'z'; c3++ {
					for c4 := 'a'; c4 <= 'z'; c4++ {
						domain := fmt.Sprintf("%c%c%c%c.%s", c1, c2, c3, c4, tld)
						candidates = append(candidates, domain)
					}
				}
			}
		}
	}

	return candidates
}

func filterBadCandidates(domains []string) []string {
	// known bad domains to exclude, they have bad records that break our simple checks
	badDomains := map[string]struct{}{
		"aaad.net": {},
		"aaax.net": {},
		"aaem.net": {},
		"aafl.net": {},
		"jmpb.net": {},
	}

	var filtered []string
	for _, d := range domains {
		if _, found := badDomains[d]; found {
			continue
		}

		filtered = append(filtered, d)
	}

	return filtered
}

// TODO: make this a flag
const CHECK_DOMAINS = false

func main() {
	db, err := openBadger("db")
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
		verifyDomains(db, candidates)
	}

	availableDomains, err := loadAvailableFromDB(db)
	if err != nil {
		panic(err)
	}
	fmt.Println("Available domains:")
	for _, d := range availableDomains {
		fmt.Println("-", d)
	}
}
