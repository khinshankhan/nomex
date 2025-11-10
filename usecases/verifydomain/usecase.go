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
		VerifyOne(ctx context.Context, domainName string) VerificationResult
		VerifyBatch(ctx context.Context, domainNames []string) []VerificationResult
	}

	// usecases declares the dependencies for the service
	usecases struct {
		domaincheckRepo domaincheck.Repository
		domainbanRepo   domainban.Repository

		dnsResolver *dnsresolver.Resolver
		rdapClient  *rdapclient.Client

		newBackoff func() backoff.Strategy
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

		// per-call jitter: create a new strategy with its own RNG
		newBackoff: func() backoff.Strategy {
			return backoff.NewJitter(backoff.NewJitterConfig{
				Base: 250 * time.Millisecond,
				Cap:  8 * time.Second,
				RNG:  rand.New(rand.NewSource(time.Now().UnixNano())),
			})
		},
	}
}

func shouldRetryRDAP(code int, err error) bool {
	// 429 are rate limited and 502/503/504 are upstream/server/transient conditions.
	if code == 429 || code == 502 || code == 503 || code == 504 {
		return true
	}

	// respect caller context: if the context is done, don't keep retrying locally.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	// net.Error covers common transient network failures.
	// transport layer hiccups (dns lookup timeout, tcp reset, etc) are generally retryable.
	// NOTE: temporary errors are not well-defined so they've been deprecated, but we lose nothing by checking it here.
	var ne net.Error
	if errors.As(err, &ne) && (ne.Timeout() || ne.Temporary()) {
		return true
	}

	// transport error without code = retryable
	return err != nil && code == 0
}

func (u *usecases) rdapWithRetry(ctx context.Context, domain string) (int, error) {
	logger := logx.GetDefaultLogger()

	// TODO: use a global limiter to avoid spamming RDAP servers when doing concurrent checks
	const maxAttempts = 5
	var lastCode int
	var lastErr error
	backoffStrategy := u.newBackoff()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		code, err := u.rdapClient.Check(ctx, domain)
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

		select {
		case <-ctx.Done():
			return lastCode, ctx.Err()
		// use jittered delay exponentially scaled by number of failed attempts.
		// attempt 0 should still wait a tiny bit to avoid stampedes.
		case <-time.After(backoffStrategy.Next(attempt)):
		}
	}

	logger.Warn("rdap retries exhausted",
		fields.String("domain", domain),
		fields.Int("attempts", maxAttempts),
		fields.Int("last_code", lastCode),
		fields.Error(lastErr),
	)
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

func (u *usecases) VerifyOne(ctx context.Context, domainName string) VerificationResult {
	logger := logx.GetDefaultLogger()
	t := time.Now()

	// TODO: circle back to check errors
	// TODO: check code to avoid getting ratelimited/ other issues

	// short, per-domain timeout layered on caller ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
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

func (u *usecases) VerifyBatch(ctx context.Context, domainNames []string) []VerificationResult {
	logger := logx.GetDefaultLogger()

	total := len(domainNames)
	results := make([]VerificationResult, 0, total)
	for i, domainName := range domainNames {
		logger.Info("Verifying",
			fields.Int("i", i+1),
			fields.Int("n", total),
			fields.String("name", domainName),
		)

		result := u.VerifyOne(ctx, domainName)
		results = append(results, result)

		logger.Info("Verified",
			fields.String("name", domainName),
			fields.Int("code", *result.CheckedDomain.Code),
		)

		// optional tiny nap, rdap already does polite retry/backoff
		time.Sleep(25 * time.Millisecond)
	}
	return results
}
