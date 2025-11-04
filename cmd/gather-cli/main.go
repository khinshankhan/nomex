package main

import (
	"fmt"
)

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

		dnsTaken, dnsErr := DnsCheck(domainName)
		if dnsErr != nil || dnsTaken {
			taken = dnsTaken
			err = dnsErr
		} else {
			fmt.Println("DNS says available, checking RDAP...")
			taken, err = RdapCheck(domainName)
		}

		fmt.Printf("Domain=%s Taken=%t Error=%v\n", domainName, taken, err)
	}
}
