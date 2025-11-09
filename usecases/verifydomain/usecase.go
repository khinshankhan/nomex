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
	"github.com/khinshankhan/nomex/platform/backoff"
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

		backoffStrategy backoff.Strategy
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

		dnsResolver: dnsResolver,
		rdapClient:  rdapClient,

		backoffStrategy: backoff.NewFullJitter(
			backoff.NewJitterConfig{
				Base: 50 * time.Millisecond,
				Cap:  8 * time.Second,
				RNG:  rand.New(rand.NewSource(time.Now().UnixNano())),
			},
		),
	}
}

func shouldRetryRDAP(code int, err error) bool {
	if code == 429 || (code >= 500 && code <= 599) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && (dnsErr.IsTimeout || dnsErr.IsTemporary) {
		// TODO: we should be retrying in theory but our logic currently has bad support for that since we rely on just
		// a basic dns lookup
		return false
	}
	// respect caller context
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	// transport error without code = allow retry
	return err != nil && code == 0
}

func (u *usecases) rdapWithRetry(ctx context.Context, domain string) (int, error) {
	logger := logx.GetDefaultLogger()

	// TODO: use a global limiter to avoid spamming RDAP servers when doing concurrent checks
	const maxAttempts = 5
	var lastCode int
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		code, err := u.rdapClient.Check(domain)
		lastCode, lastErr = code, err

		if !shouldRetryRDAP(code, err) {
			return code, err
		}

		logger.Warn("rdap check failed, will retry",
			fields.String("domain", domain),
			fields.Int("code", code),
			fields.Error(err),
			fields.Int("attempt", attempt+1),
		)

		// jittered backoff per attempt
		select {
		case <-ctx.Done():
			return lastCode, ctx.Err()
		case <-time.After(u.backoffStrategy.Next(attempt)):
		}
	}
	return lastCode, lastErr
}

func (u *usecases) checkDomain(ctx context.Context, domainName string) (int, error) {
	taken, err := u.dnsResolver.Check(ctx, domainName)
	if err != nil {
		return 500, err
	}

	// we can trust dns if it says domain is taken
	if taken {
		return 200, nil
	}

	// domain is not found in dns, double-check with rdap (with retries)
	code, err := u.rdapWithRetry(ctx, domainName)
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

	// short, per-domain timeout so one slow request doesn't block the batch
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	code, err := u.checkDomain(ctx, domainName)
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

		// Use jittered delay scaled by "failed" (attempt count since last failure).
		// attempt 0 should still wait a tiny bit to avoid stampedes.
		delay := u.backoffStrategy.Next(failed)
		time.Sleep(delay)
	}
	return results
}
