package verifydomain

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"

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
		Verify(ctx context.Context, domainName string) VerificationResult
		VerifyBatch(ctx context.Context, domainNames []string) []VerificationResult
	}

	// usecases declares the dependencies for the service
	usecases struct {
		domaincheckRepo domaincheck.Repository
		domainbanRepo   domainban.Repository

		dnsResolver *dnsresolver.Resolver
		rdapClient  *rdapclient.Client

		rdapMaxAttempts int
		rdapLimiter     *rate.Limiter
		newBackoff      func() backoff.Strategy

		maxParallel int
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

		rdapMaxAttempts: 5,
		// global RDAP rate limiter: 1 request every 10 seconds
		rdapLimiter: rate.NewLimiter(rate.Every(10*time.Second), 1),
		// per-call jitter: create a new strategy with its own RNG
		newBackoff: func() backoff.Strategy {
			return backoff.NewJitter(backoff.NewJitterConfig{
				Base: 250 * time.Millisecond,
				Cap:  8 * time.Second,
				RNG:  rand.New(rand.NewSource(time.Now().UnixNano())),
			})
		},

		maxParallel: 16,
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

	var lastCode int
	var lastErr error
	backoffStrategy := u.newBackoff()

	for attempt := 0; attempt < u.rdapMaxAttempts; attempt++ {
		// reserve token and check the delay against ctx deadline
		r := u.rdapLimiter.Reserve()
		if !r.OK() {
			return 429, errors.New("limiter burst too small")
		}
		delay := r.DelayFrom(time.Now())
		if deadline, ok := ctx.Deadline(); ok && time.Now().Add(delay).After(deadline) {
			r.Cancel()
			return 408, context.DeadlineExceeded
		}

		// wait for token or ctx cancel
		tokenT := time.NewTimer(delay)
		select {
		case <-tokenT.C:
		case <-ctx.Done():
			tokenT.Stop()
			r.Cancel()
			return 408, ctx.Err()
		}
		// r capacity is consumed here because we proceeded.

		code, err := u.rdapClient.Check(ctx, domain)
		lastCode, lastErr = code, err
		if !shouldRetryRDAP(code, err) {
			return code, err
		}

		logger.Warn("rdap check failed, will retry",
			fields.String("domain", domain),
			fields.Int("attempt", attempt+1),
			fields.Int("code", code),
			fields.Error(err),
		)

		// use jittered delay exponentially scaled by number of failed attempts.
		// attempt 0 should still wait a tiny bit to avoid stampedes.
		sleep := backoffStrategy.Next(attempt)
		sleepT := time.NewTimer(sleep)
		select {
		case <-sleepT.C:
		case <-ctx.Done():
			sleepT.Stop()
			return lastCode, ctx.Err()
		}
	}

	logger.Warn("rdap retries exhausted",
		fields.String("domain", domain),
		fields.Int("attempts", u.rdapMaxAttempts),
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

func (u *usecases) Verify(ctx context.Context, domainName string) VerificationResult {
	logger := logx.GetDefaultLogger()
	t := time.Now()

	// TODO: circle back to check errors
	// TODO: check code to avoid getting ratelimited/ other issues

	// short, per-domain timeout layered on caller ctx
	ctx, cancel := context.WithTimeout(
		ctx,
		// NOTE: this should be greater than the max attempts * rps allowed by the rdap limiter * average rdap response
		// time and account for exponential backoff + n routines running in parallel
		75*time.Second,
	)
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

	type job struct {
		i int
		d string
	}
	jobs := make(chan job)

	total := len(domainNames)
	results := make([]VerificationResult, total)

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for j := range jobs {
			logger.Info("Verifying",
				fields.Int("i", j.i+1),
				fields.Int("n", total),
				fields.String("name", j.d),
			)

			result := u.Verify(ctx, j.d)
			results[j.i] = result

			logger.Info("Verified",
				fields.String("name", j.d),
				fields.Int("code", *result.CheckedDomain.Code),
			)

		}
	}
	wg.Add(u.maxParallel)
	for w := 0; w < u.maxParallel; w++ {
		go worker()
	}

	for i, d := range domainNames {
		jobs <- job{i, d}
	}
	close(jobs)
	wg.Wait()
	return results
}
