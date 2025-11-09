package main

import (
	"math/rand"
	"os"
	"time"

	"github.com/khinshankhan/nomex/adapters/dnsresolver"
	"github.com/khinshankhan/nomex/adapters/rdapclient"
	"github.com/khinshankhan/nomex/data/domainban"
	"github.com/khinshankhan/nomex/data/domaincheck"
	"github.com/khinshankhan/nomex/infra/sqlite"
	"github.com/khinshankhan/nomex/services/logx"
	"github.com/khinshankhan/nomex/services/logx/fields"
	"github.com/khinshankhan/nomex/usecases/verifydomain"

	// side-effect import to autoload .env files, should run before anything else
	_ "github.com/joho/godotenv/autoload"
)

func smallBackoff(attempt int) {
	// cap duration to 500ms + jitter
	d := time.Duration(50*attempt) * time.Millisecond
	if d > 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	time.Sleep(d + time.Duration(rand.Intn(100))*time.Millisecond)
}

func verifyDomains(
	verifydomainUsecase verifydomain.Usecases,
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

		result := verifydomainUsecase.VerifyOne(domainName)
		logger.Info("Finished domain verify",
			fields.String("name", domainName),
			fields.Int("code", *result.CheckedDomain.Code),
		)

		if result.Err != nil {
			failed += 1
		} else {
			failed = 0
		}
		smallBackoff(failed * 15)
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

		verifydomainUsecase := verifydomain.New(
			domaincheckRepo,
			domainbanRepo,
			dnsResolver,
			rdapClient,
		)

		verifyDomains(
			verifydomainUsecase,
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
