package main

import (
	"fmt"
	"time"
)

func checkDomain(domainName string) (bool, error) {
	var taken bool = true
	var err error = nil

	dnsTaken, dnsErr := DnsCheck(domainName)
	if dnsErr != nil || dnsTaken {
		taken = dnsTaken
		err = dnsErr
	} else {
		taken, err = RdapCheck(domainName)
	}

	return taken, err
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

	for _, domainName := range pendingDomainNames {
		fmt.Println("Checking domain:", domainName)
		taken, err := checkDomain(domainName)

		// TODO: temporary hardcoded status 418 for testing, unsure what a good status code would be here
		code := 418
		if err == nil {
			if taken {
				// taken
				code = 200
			} else {
				// available
				code = 404
			}
		} else {
			fmt.Println("Error checking domain", domainName, ":", err)
		}

		t := time.Now()
		saveCode(db, domainName, code, &t)
	}
}
