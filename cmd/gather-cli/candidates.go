package main

import (
	"fmt"

	"github.com/khinshankhan/nomex/data/domainban"
	"github.com/khinshankhan/nomex/data/domaincheck"
)

// TODO: we need to greatly decrease the candidate space
// conversation on TPH https://discord.com/channels/244230771232079873/244230771232079873/1435352534821765220
func generateCandidates(tlds []string) []string {
	candidates := []string{}

	// TODO: surely there's a more efficient way to do this
	for _, tld := range tlds {
		for c1 := 'a'; c1 <= 'z'; c1++ {
			candidates = append(candidates, fmt.Sprintf("%c.%s", c1, tld), fmt.Sprintf("%c%c.%s", c1, tld))
		}
	}

	return candidates
}

func filterBadCandidates(domainbanRepo domainban.Repository, domains []domaincheck.DomainCheck) []string {
	bannedDomainRecords, err := domainbanRepo.GetAllBannedDomains()
	if err != nil {
		panic(err)
	}

	bannedDomainLookup := make(map[string]struct{})
	for _, record := range bannedDomainRecords {
		bannedDomainLookup[record.Domain] = struct{}{}
	}

	var filtered []string
	for _, d := range domains {
		if _, found := bannedDomainLookup[d.Domain]; found {
			continue
		}

		if len(d.Domain) > 3+1+3 {
			continue
		}

		filtered = append(filtered, d.Domain)
	}

	return filtered
}
