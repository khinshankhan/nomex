package main

import (
	"database/sql"
	"fmt"
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

func filterBadCandidates(db *sql.DB, domains []string) []string {
	bannedDomains, err := loadBannedFromDB(db)
	if err != nil {
		panic(err)
	}

	var filtered []string
	for _, d := range domains {
		if _, found := bannedDomains[d]; found {
			continue
		}

		if len(d) > 3+1+3 {
			continue
		}

		filtered = append(filtered, d)
	}

	return filtered
}
