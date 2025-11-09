package main

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/khinshankhan/nomex/adapters/dnsresolver"
	"github.com/khinshankhan/nomex/adapters/rdapclient"
	"github.com/khinshankhan/nomex/data/domainban"
	"github.com/khinshankhan/nomex/data/domaincheck"
	"github.com/khinshankhan/nomex/infra/sqlite"
	"github.com/khinshankhan/nomex/services/logx"
	"github.com/khinshankhan/nomex/services/logx/fields"

	// side-effect import to autoload .env files, should run before anything else
	_ "github.com/joho/godotenv/autoload"
)

func verifyDomain(
	domaincheckRepo domaincheck.Repository,
	domainbanRepo domainban.Repository,
	checkDomain func(domainName string) (int, error),
	domainName string,
) int {
	logger := logx.GetDefaultLogger()
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
			logger.Warn("checkDomain unexpected error",
				fields.String("domain", domainName),
				fields.Error(err),
			)
			return 15
		}
	}

	logger.Info("Domain checked",
		fields.String("domain", domainName),
		fields.Int("code", code),
	)
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

func verifyDomains(
	domaincheckRepo domaincheck.Repository,
	domainbanRepo domainban.Repository,
	checkDomain func(domainName string) (int, error),
	domainNames []string,
) {
	logger := logx.GetDefaultLogger()

	// seed rand
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(domainNames), func(i, j int) {
		domainNames[i], domainNames[j] = domainNames[j], domainNames[i]
	})

	total := len(domainNames)
	failed := 0
	for i, domainName := range domainNames {
		logger.Info("Starting domain verify",
			fields.Int("i", i+1),
			fields.Int("n", total),
			fields.String("name", domainName),
		)
		backoffAddition := verifyDomain(
			domaincheckRepo,
			domainbanRepo,
			checkDomain,
			domainName,
		)
		if backoffAddition > 0 {
			failed += backoffAddition
		} else {
			failed = 0
		}
		smallBackoff(failed)
	}
}

// TODO: make this a flag
const CHECK_DOMAINS = true

// Version and BuildData get replaced during build with the commit hash and time of build
var (
	CommitHash = ""
	BuildDate  = ""
)

func main() {
	logger := logx.GetDefaultLogger()
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
		logger.Info(
			"Generated candidates",
			fields.Int("n", len(generatedCandidates)),
		)

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
		logger.Info(
			"Loaded candidates",
			fields.Int("n", len(pendingDomains)),
		)

		// list of domain names to check
		candidates := filterBadCandidates(domainbanRepo, pendingDomains)
		logger.Info(
			"Filtered candidates",
			fields.Int("n", len(candidates)),
		)

		// verify domains
		ua := getUserAgent()
		rdapClient, err := rdapclient.New(rdapclient.Config{
			UserAgent:  ua,
			HTTPClient: nil, // use default 10s
		})
		if err != nil {
			panic(err)
		}

		dnsResolver := dnsresolver.New(dnsresolver.Config{
			Timeout: 30 * time.Second,
		})

		checkDomain := func(domainName string) (int, error) {
			taken, err := dnsResolver.Check(context.Background(), domainName)
			if err != nil {
				return 500, err
			}

			// we can trust that the domain is taken
			if taken {
				return 200, nil
			}

			// domain not found via dns, double-check with rdap
			code, err := rdapClient.Check(domainName)
			return code, err
		}

		verifyDomains(
			domaincheckRepo,
			domainbanRepo,
			checkDomain,
			candidates,
		)
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
