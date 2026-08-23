// Package check turns a registry answer into a stored row.
package check

import "time"

// Status is what a check concluded about a domain.
type Status string

const (
	StatusUnchecked  Status = "unchecked"
	StatusRegistered Status = "registered"
	StatusNotFound   Status = "not_found"
	StatusUnknown    Status = "unknown"
)

// TTL policy. These are guesses until there is real data to tune against; they
// are the first thing to revisit once the sweep has run.
const (
	// NotFoundTTL is short because an available domain can be taken at any
	// moment, and a stale "available" is the answer most likely to embarrass.
	NotFoundTTL = 6 * time.Hour

	// NoExpiryTTL applies when a registry reports a domain as registered but
	// does not say when the registration ends.
	NoExpiryTTL = 30 * 24 * time.Hour

	// MaxTTL caps everything. Without it a 2027 expiry would go unlooked-at
	// for eleven months, and a transfer, deletion or status change in the
	// meantime would go unnoticed.
	MaxTTL = 90 * 24 * time.Hour

	// PostExpiryDelay is roughly how long a gTLD takes to actually drop after
	// expiring: auto-renew grace, then redemption, then pending delete. ICANN
	// puts the whole lifecycle at 30-75 days, so expiry is not availability.
	PostExpiryDelay = 75 * 24 * time.Hour

	// ExpiredRecheck is how often to look at a domain that is past its expiry
	// but not yet dropped. Weekly rather than sleeping for the full window,
	// because registrars vary and it is a small set.
	ExpiredRecheck = 7 * 24 * time.Hour

	// UnknownTTL applies when a check completed but could not classify the
	// answer. Short, because "we do not know" is not a result worth caching.
	UnknownTTL = time.Hour
)

// FreshUntil returns when a result stops being trustworthy.
//
// Expiry is folded in here rather than consulted by the scheduler, which keeps
// the sweep read a single indexed scan and puts all the policy in one place.
//
// A registered domain has two interesting moments, not one: around expiry (did
// they renew?) and about 75 days later (did it drop?). Between them nothing
// changes, and after a renewal nothing changes for another year.
func FreshUntil(now time.Time, status Status, expiration *time.Time) time.Time {
	switch {
	case status == StatusNotFound:
		return now.Add(NotFoundTTL)

	case status == StatusUnknown, status == StatusUnchecked:
		return now.Add(UnknownTTL)

	case expiration == nil:
		return now.Add(NoExpiryTTL)

	case expiration.After(now):
		// Look again just after expiry to see whether it was renewed, capped
		// so a far-future expiry still gets an occasional look.
		return earliest(expiration.Add(24*time.Hour), now.Add(MaxTTL))

	case now.Before(expiration.Add(PostExpiryDelay)):
		// Expired but inside the drop window: grace, redemption and pending
		// delete run about 75 days, and the name could become available on any
		// day of it. Weekly, rather than sleeping through the whole window,
		// because registrars vary and this is a small set.
		return now.Add(ExpiredRecheck)

	default:
		// Past the drop window and still registered, so it was renewed or
		// transferred and the expiry we hold is simply stale. Ordinary cadence.
		//
		// Reached by anything expired long ago. Deriving the answer from that
		// old expiry would return a time in the PAST, leaving the row
		// permanently due and the sweep spinning on it.
		return now.Add(NoExpiryTTL)
	}
}

// Backoff returns when to look again after a failed attempt.
//
// This is what stands in for consulting attempts.retry_after in the scheduler
// query: pushing fresh_until forward keeps the sweep read on its index.
//
// A server-supplied Retry-After wins outright -- it is the one number that is
// not a guess. Otherwise the delay doubles per consecutive failure, capped, so
// a server that is down stops being hammered without dropping its domains.
func Backoff(now time.Time, failures int, retryAfter time.Duration) time.Time {
	const (
		base = 5 * time.Minute
		max  = 24 * time.Hour
	)

	if retryAfter > 0 {
		return now.Add(retryAfter)
	}

	delay := base
	for range failures {
		if delay >= max/2 {
			return now.Add(max)
		}
		delay *= 2
	}
	return now.Add(delay)
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
