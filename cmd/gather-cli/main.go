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

func main() {
	db, err := openBadger("db")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// list of domain names to check
	domainNames := []string{
		// taken
		"khinshankhan.com",
		// taken but reserved
		"example.com",
		// not taken
		"thisdomaindoesntexistcurrentlybecauseichecked.com",
	}

	err = bulkEnsureRows(db, domainNames)
	if err != nil {
		panic(err)
	}

	pendingDomainNames, err := loadPendingFromDB(db)
	if err != nil {
		panic(err)
	}

	verifyDomains(db, pendingDomainNames)

	availableDomains, err := loadAvailableFromDB(db)
	if err != nil {
		panic(err)
	}
	fmt.Println("Available domains:")
	for _, d := range availableDomains {
		fmt.Println("-", d)
	}
}
