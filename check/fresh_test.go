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

const day = 24 * time.Hour

func TestFreshUntil(t *testing.T) {
	tests := []struct {
		name       string
		status     Status
		expiration *time.Time
		want       time.Duration // from now
	}{
		{"available is short-lived", StatusNotFound, nil, 6 * time.Hour},
		{"available ignores any expiry", StatusNotFound, at(365 * day), 6 * time.Hour},
		{"unknown is not cached", StatusUnknown, nil, time.Hour},
		{"registered without expiry", StatusRegistered, nil, 30 * day},
		{"expiry soon: look just after", StatusRegistered, at(4 * day), 5 * day},
		{"expiry far: capped", StatusRegistered, at(365 * day), 90 * day},
		{"just expired: weekly", StatusRegistered, at(-day), 7 * day},
		{"inside drop window", StatusRegistered, at(-74 * day), 7 * day},
		{"past drop window: renewed", StatusRegistered, at(-76 * day), 30 * day},
		{"expired years ago", StatusRegistered, at(-10 * 365 * day), 30 * day},
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

// Nothing should return a time in the past, or the row stays due forever.
func TestFreshUntilIsAlwaysFuture(t *testing.T) {
	offsets := []time.Duration{
		-10 * 365 * day, -76 * day, -time.Second,
		0, time.Second, 365 * day,
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
	tests := []struct {
		name       string
		failures   int
		retryAfter time.Duration
		want       time.Duration
	}{
		{"first failure", 0, 0, 5 * time.Minute},
		{"doubles", 1, 0, 10 * time.Minute},
		{"doubles again", 3, 0, 40 * time.Minute},
		{"last step under the cap", 8, 0, 21*time.Hour + 20*time.Minute},
		{"capped at a day", 9, 0, day},
		{"stays capped", 50, 0, day},
		{"retry-after wins", 0, 30 * time.Second, 30 * time.Second},
		{"retry-after wins over a long streak", 50, 30 * time.Second, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := Backoff(now, tt.failures, tt.retryAfter), now.Add(tt.want); !got.Equal(want) {
				t.Errorf("Backoff = %v, want %v", got.Sub(now), tt.want)
			}
		})
	}
}
