package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/khinshankhan/nomex/data/domainban"
	"github.com/khinshankhan/nomex/data/domaincheck"
	"github.com/khinshankhan/nomex/infra/sqlite"
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

func verifyDomain(domaincheckRepo domaincheck.Repository, domainbanRepo domainban.Repository, domainName string) int {
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

				reason := "temporary DNS failure"
				domainbanRepo.BanDomain(
					domainban.DomainBan{
						Domain: domainName,
						Reason: &reason,
						At:     &t,
					},
				)
				return 0
			}
			// Other DNS errors: record reason and return
			reason := "DNS error: " + dnsErr.Err
			domainbanRepo.BanDomain(
				domainban.DomainBan{
					Domain: domainName,
					Reason: &reason,
					At:     &t,
				},
			)
			return 5

		case errors.Is(err, context.DeadlineExceeded):
			reason := "timeout"
			domainbanRepo.BanDomain(
				domainban.DomainBan{
					Domain: domainName,
					Reason: &reason,
					At:     &t,
				},
			)
			return 0

		default:
			fmt.Println("checkDomain unexpected error for", domainName, ":", err)
			return 15
		}
	}

	fmt.Println("Domain:", domainName, "Code:", code)
	err = domaincheckRepo.SaveDomainCheck(
		domaincheck.DomainCheck{
			Domain: domainName,
			Code:   &code,
			At:     &t,
		},
	)

	if err != nil {
		panic(err)
	}
	return 0
}

func smallBackoff(attempt int) {
	// cap duration to 500ms + jitter
	d := time.Duration(50*attempt) * time.Millisecond
	if d > 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	time.Sleep(d + time.Duration(rand.Intn(100))*time.Millisecond)
}

func verifyDomains(domaincheckRepo domaincheck.Repository, domainbanRepo domainban.Repository, domainNames []string) {
	// seed rand
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(domainNames), func(i, j int) {
		domainNames[i], domainNames[j] = domainNames[j], domainNames[i]
	})

	failed := 0
	for i, domainName := range domainNames {
		fmt.Printf("(%d/%d) ", i+1, len(domainNames))
		fmt.Println("Checking domain:", domainName)
		backoffAddition := verifyDomain(domaincheckRepo, domainbanRepo, domainName)
		if backoffAddition > 0 {
			failed += backoffAddition
		} else {
			failed = 0
		}
		smallBackoff(failed)
	}
}

// TODO: make this a flag
const CHECK_DOMAINS = false

func main() {
	conn, err := sqlite.GetConnection(
		sqlite.DefaultOptions("db/domains.sqlite"),
	)
	if err != nil {
		panic(err)
	}
	defer sqlite.CloseConnection(conn)

	domaincheckRepo := domaincheck.NewRepository(conn)
	domainbanRepo := domainban.NewRepository(conn)

	if CHECK_DOMAINS {
		generatedCandidates := generateCandidates([]string{"net"})
		fmt.Println("Generated", len(generatedCandidates), "candidates")

		// ensure all candidates are in the database so they're "queued" for checking
		err = domaincheckRepo.BulkEnsureDomainChecks(generatedCandidates)
		if err != nil {
			panic(err)
		}

		// NOTE: this loads any pre existing pending domains from the database
		pendingDomains, err := domaincheckRepo.GetPendingDomains()
		if err != nil {
			panic(err)
		}
		fmt.Println("Loaded", len(pendingDomains), "candidates")

		// list of domain names to check
		candidates := filterBadCandidates(domainbanRepo, pendingDomains)
		fmt.Println("Filtered to", len(candidates), "candidates")
		verifyDomains(domaincheckRepo, domainbanRepo, candidates)
	}

	availableDomains, err := domaincheckRepo.GetAvailableDomains()
	if err != nil {
		panic(err)
	}

	f, err := os.Create("available-domains.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	for _, d := range availableDomains {
		f.WriteString(d.Domain + "\n")
	}
}
