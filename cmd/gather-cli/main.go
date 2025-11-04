package main

import (
	"fmt"
	"github.com/openrdap/rdap"
)

var client = &rdap.Client{}

func checkDomainAvailabilityRDAP(domainName string) {
	domain, err := client.QueryDomain(domainName)

	if err == nil {
		fmt.Printf("Handle=%s Domain=%s\n", domain.Handle, domain.LDHName)
	} else {
		fmt.Printf("Error: %s\n", err)
	}
}

func main() {
	// taken
	checkDomainAvailabilityRDAP("khinshankhan.com")
	// taken but reserved
	checkDomainAvailabilityRDAP("example.com")
	// not taken
	checkDomainAvailabilityRDAP("thisdomaindoesntexistcurrentlybecauseichecked.com")
}
