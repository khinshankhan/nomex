package check

import (
	"testing"
	"time"
)

var now = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time {
	t := now.Add(d)
	return &t
}

func TestFreshUntil(t *testing.T) {
	tests := []struct {
		name       string
		status     Status
		expiration *time.Time
		want       time.Duration // from now
	}{
		{"available is short-lived", StatusNotFound, nil, NotFoundTTL},
		{"available ignores any expiry", StatusNotFound, at(365 * 24 * time.Hour), NotFoundTTL},
		{"unknown is not cached", StatusUnknown, nil, UnknownTTL},
		{"registered without expiry", StatusRegistered, nil, NoExpiryTTL},
		{"expiry soon: look just after", StatusRegistered, at(4 * 24 * time.Hour), 5 * 24 * time.Hour},
		{"expiry far: capped", StatusRegistered, at(365 * 24 * time.Hour), MaxTTL},
		{"just expired: weekly", StatusRegistered, at(-24 * time.Hour), ExpiredRecheck},
		{"inside drop window", StatusRegistered, at(-74 * 24 * time.Hour), ExpiredRecheck},
		{"past drop window: renewed", StatusRegistered, at(-76 * 24 * time.Hour), NoExpiryTTL},
		{"expired years ago", StatusRegistered, at(-10 * 365 * 24 * time.Hour), NoExpiryTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FreshUntil(now, tt.status, tt.expiration)
			if want := now.Add(tt.want); !got.Equal(want) {
				t.Errorf("FreshUntil = %v, want %v (%v from now)", got, want, tt.want)
			}
		})
	}
}

// The cap is load-bearing: without it a far-future expiry means the domain is
// never looked at again, and a transfer or deletion goes unnoticed.
func TestFreshUntilAlwaysCapped(t *testing.T) {
	for _, d := range []time.Duration{
		91 * 24 * time.Hour,
		365 * 24 * time.Hour,
		10 * 365 * 24 * time.Hour,
	} {
		got := FreshUntil(now, StatusRegistered, at(d))
		if max := now.Add(MaxTTL); got.After(max) {
			t.Errorf("expiry %v out: FreshUntil = %v, past the %v cap", d, got, MaxTTL)
		}
	}
}

// Nothing may return a time in the past, or the row would be immediately due
// and the sweep would spin on it.
func TestFreshUntilIsAlwaysFuture(t *testing.T) {
	offsets := []time.Duration{
		-10 * 365 * 24 * time.Hour, -76 * 24 * time.Hour, -time.Second,
		0, time.Second, 365 * 24 * time.Hour,
	}
	statuses := []Status{StatusNotFound, StatusRegistered, StatusUnknown, StatusUnchecked}

	for _, s := range statuses {
		for _, d := range offsets {
			if got := FreshUntil(now, s, at(d)); !got.After(now) {
				t.Errorf("status=%s expiry=%v: FreshUntil = %v, not after now", s, d, got)
			}
		}
		if got := FreshUntil(now, s, nil); !got.After(now) {
			t.Errorf("status=%s expiry=nil: FreshUntil = %v, not after now", s, got)
		}
	}
}

func TestBackoff(t *testing.T) {
	// A server-supplied Retry-After is the one number that is not a guess.
	if got, want := Backoff(now, 0, 5*time.Minute), now.Add(5*time.Minute); !got.Equal(want) {
		t.Errorf("with Retry-After: %v, want %v", got, want)
	}
	if got, want := Backoff(now, 99, 30*time.Second), now.Add(30*time.Second); !got.Equal(want) {
		t.Errorf("Retry-After must win over failure count: %v, want %v", got, want)
	}

	// Otherwise it doubles, and is capped.
	var last time.Time
	for i := range 20 {
		got := Backoff(now, i, 0)
		if !got.After(now) {
			t.Fatalf("failures=%d: %v is not in the future", i, got)
		}
		if i > 0 && got.Before(last) {
			t.Errorf("failures=%d: backoff went backwards (%v < %v)", i, got, last)
		}
		if max := now.Add(24 * time.Hour); got.After(max) {
			t.Errorf("failures=%d: %v exceeds the 24h cap", i, got)
		}
		last = got
	}
}
