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

// TTL policy. Mostly guesses for now, tune once there is real sweep data.
const (
	// NotFoundTTL is short since an available domain can be registered at any time.
	NotFoundTTL = 6 * time.Hour

	// NoExpiryTTL applies when a registry reports a domain as registered but
	// does not say when the registration ends.
	NoExpiryTTL = 30 * 24 * time.Hour

	// MaxTTL caps everything so a far off expiry still gets checked for
	// transfers, deletions and status changes.
	MaxTTL = 90 * 24 * time.Hour

	// PostExpiryDelay is roughly how long a gTLD takes to drop after expiring
	// (grace, redemption, pending delete). ICANN puts it at 30-75 days.
	PostExpiryDelay = 75 * 24 * time.Hour

	// ExpiredRecheck is how often to check an expired domain that has not
	// dropped yet. Registrars vary, so this polls through the whole window.
	ExpiredRecheck = 7 * 24 * time.Hour

	// UnknownTTL applies when a check completed but the answer could not be
	// classified.
	UnknownTTL = time.Hour
)

// FreshUntil returns when a result stops being trustworthy.
//
// Expiry is handled here instead of in the scheduler so the sweep query stays a
// single indexed scan. A registered domain is worth checking just after expiry
// (renewed?) and again about 75 days later (dropped?).
func FreshUntil(now time.Time, status Status, expiration *time.Time) time.Time {
	switch {
	case status == StatusNotFound:
		return now.Add(NotFoundTTL)

	case status == StatusUnknown, status == StatusUnchecked:
		return now.Add(UnknownTTL)

	case expiration == nil:
		return now.Add(NoExpiryTTL)

	case expiration.After(now):
		// just after expiry, to see if it was renewed
		return earliest(expiration.Add(24*time.Hour), now.Add(MaxTTL))

	case now.Before(expiration.Add(PostExpiryDelay)):
		// expired but not dropped yet, it could free up any day
		return now.Add(ExpiredRecheck)

	default:
		// past the drop window and still registered, so it was renewed or
		// transferred and the stored expiry is stale
		// NOTE: deriving from the old expiry gives a time in the past, which
		// leaves the row due forever
		return now.Add(NoExpiryTTL)
	}
}

// Backoff returns when to look again after a failed attempt. A server's
// Retry-After wins, otherwise the delay doubles per consecutive failure, up to
// a day.
//
// Pushing fresh_until forward stands in for checking attempts.retry_after in the
// sweep query, which keeps that query on its index.
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
