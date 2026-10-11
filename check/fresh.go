// Package check defines what a registry check returns and how long it holds.
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

// TODO: mostly guesses, tune once there's real sweep data
const (
	// NotFoundTTL is short, an available domain can be taken any time.
	NotFoundTTL = 6 * time.Hour

	// NoExpiryTTL is for a registered domain with no expiry date.
	NoExpiryTTL = 30 * 24 * time.Hour

	// MaxTTL caps everything, so a far off expiry still gets rechecked.
	MaxTTL = 90 * 24 * time.Hour

	// PostExpiryDelay is about how long a gTLD takes to drop after expiring.
	// ICANN puts it at 30-75 days.
	PostExpiryDelay = 75 * 24 * time.Hour

	// ExpiredRecheck is how often an expired domain is checked until it drops.
	ExpiredRecheck = 7 * 24 * time.Hour

	// UnknownTTL is for an answer that couldn't be classified.
	UnknownTTL = time.Hour
)

// FreshUntil returns when a result stops being trustworthy. A registered domain
// is rechecked just after expiry (renewed?) and again once it could have
// dropped.
func FreshUntil(now time.Time, status Status, expiration *time.Time) time.Time {
	switch {
	case status == StatusNotFound:
		return now.Add(NotFoundTTL)

	case status == StatusUnknown, status == StatusUnchecked:
		return now.Add(UnknownTTL)

	case expiration == nil:
		return now.Add(NoExpiryTTL)

	case expiration.After(now):
		// just after expiry, renewed?
		return earliest(expiration.Add(24*time.Hour), now.Add(MaxTTL))

	case now.Before(expiration.Add(PostExpiryDelay)):
		// expired, could drop any day
		return now.Add(ExpiredRecheck)

	default:
		// past the drop window, so the stored expiry is stale
		// NOTE: deriving from it gives a time in the past, due forever
		return now.Add(NoExpiryTTL)
	}
}

// Backoff returns when to look again after a failure. A server's Retry-After
// wins, otherwise the delay doubles per failure, up to a day.
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
