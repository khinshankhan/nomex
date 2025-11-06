package main

import (
	"fmt"
	"os"
	"strings"
)

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
					domain := fmt.Sprintf("%c%c%c.%s", c1, c2, c3, tld)
					candidates = append(candidates, domain)
				}
				domain := fmt.Sprintf("%c%c.%s", c1, c2, tld)
				candidates = append(candidates, domain)
			}
			domain := fmt.Sprintf("%c.%s", c1, tld)
			candidates = append(candidates, domain)
		}
	}

	return candidates
}

func filterBadCandidates(domains []string) []string {
	// known bad domains to exclude, they have bad records that break our simple checks
	badDomains := map[string]struct{}{}

	// read file on disk "bad-domains.txt" for more list, one per line
	filename := "bad-domains.txt"
	data, err := os.ReadFile(filename)
	if err != nil {
		fmt.Println("Error reading bad domains file:", err)
		panic(err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			badDomains[line] = struct{}{}
		}
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
