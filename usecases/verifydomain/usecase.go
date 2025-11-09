package verifydomain

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"time"

	"github.com/khinshankhan/nomex/adapters/dnsresolver"
	"github.com/khinshankhan/nomex/adapters/rdapclient"
	"github.com/khinshankhan/nomex/data/domainban"
	"github.com/khinshankhan/nomex/data/domaincheck"
	"github.com/khinshankhan/nomex/services/logx"
	"github.com/khinshankhan/nomex/services/logx/fields"
)

type (
	// Usecases declares available services
	Usecases interface {
		VerifyOne(domainName string) VerificationResult
		VerifyBatch(domainNames []string) []VerificationResult
	}

	// usecases declares the dependencies for the service
	usecases struct {
		domaincheckRepo domaincheck.Repository
		domainbanRepo   domainban.Repository

		dnsResolver *dnsresolver.Resolver
		rdapClient  *rdapclient.Client
	}
)

// New returns Usecases
func New(
	domaincheckRepo domaincheck.Repository,
	domainbanRepo domainban.Repository,

	dnsResolver *dnsresolver.Resolver,
	rdapClient *rdapclient.Client,

) Usecases {
	return &usecases{
		domaincheckRepo: domaincheckRepo,
		domainbanRepo:   domainbanRepo,
		dnsResolver:     dnsResolver,
		rdapClient:      rdapClient,
	}
}

func (u *usecases) checkDomain(domainName string) (int, error) {
	taken, err := u.dnsResolver.Check(context.Background(), domainName)
	if err != nil {
		return 500, err
	}

	// we can trust DNS if it says domain is taken
	if taken {
		return 200, nil
	}

	// domain is not found in DNS, double-check with rdap
	code, err := u.rdapClient.Check(domainName)
	return code, err
}

type VerificationResult struct {
	CheckedDomain domaincheck.DomainCheck
	Err           error
}

func (u *usecases) VerifyOne(domainName string) VerificationResult {
	logger := logx.GetDefaultLogger()
	t := time.Now()

	// TODO: circle back to check errors
	// TODO: check code to avoid getting ratelimited/ other issues
	code, err := u.checkDomain(domainName)
	checkedDomain := domaincheck.DomainCheck{
		Domain: domainName,
		Code:   &code,
		At:     &t,
	}

	if err != nil {
		var dnsErr *net.DNSError
		switch {
		// TODO: circle back to this, this may be insufficient...
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			// all good, domain is available but we can't trust dns results alone
			// domain check should've verified via RDAP as well so we can move on
			break
		case errors.As(err, &dnsErr) && (dnsErr.IsTemporary || dnsErr.IsTimeout):
			// transient resolver issue -> "ban" (or defer) and move on
			reason := "temporary DNS failure"
			_ = u.domainbanRepo.BanDomain(
				domainban.DomainBan{
					Domain: domainName,
					Reason: &reason,
					At:     &t,
				},
			)
			break
		case errors.Is(err, context.DeadlineExceeded):
			reason := "timeout"
			_ = u.domainbanRepo.BanDomain(
				domainban.DomainBan{
					Domain: domainName,
					Reason: &reason,
					At:     &t,
				},
			)
			break
		default:
			logger.Warn("checkDomain unexpected error",
				fields.String("domain", domainName),
				fields.Error(err),
			)
			return VerificationResult{
				CheckedDomain: checkedDomain,
				Err:           err,
			}
		}
	}

	err = u.domaincheckRepo.SaveDomainCheck(checkedDomain)
	if err != nil {
		logger.Error("failed to save domain check",
			fields.String("domain", domainName),
			fields.Error(err),
		)
		return VerificationResult{
			CheckedDomain: checkedDomain,
			Err:           err,
		}
	}

	return VerificationResult{
		CheckedDomain: checkedDomain,
		Err:           nil,
	}
}

func (u *usecases) VerifyBatch(domainNames []string) []VerificationResult {
	logger := logx.GetDefaultLogger()

	total := len(domainNames)
	results := make([]VerificationResult, 0, total)
	failed := 0
	for i, domainName := range domainNames {
		logger.Info("Verifying",
			fields.Int("i", i+1),
			fields.Int("n", total),
			fields.String("name", domainName),
		)

		result := u.VerifyOne(domainName)
		results = append(results, result)

		logger.Info("Verified",
			fields.String("name", domainName),
			fields.Int("code", *result.CheckedDomain.Code),
		)

		if result.Err != nil {
			failed += 1
		} else {
			failed = 0
		}

		// ensure tiny politeness delay of at least 1
		weight := (failed * 15) + 1
		smallBackoff(weight)
	}
	return results
}

func smallBackoff(weight int) {
	// cap duration to 1000ms + jitter
	d := time.Duration(50*weight) * time.Millisecond
	if d > 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	time.Sleep(d + time.Duration(rand.Intn(100))*time.Millisecond)
}
