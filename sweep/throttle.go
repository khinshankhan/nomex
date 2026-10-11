package sweep

import (
	"sync"
	"time"
)

// throttles tracks which origins asked us to wait, and until when.
type throttles struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newThrottles(until map[string]time.Time) *throttles {
	if until == nil {
		until = make(map[string]time.Time)
	}
	return &throttles{until: until}
}

// active returns when origin reopens, if it's throttled at now.
func (t *throttles) active(origin string, now time.Time) (time.Time, bool) {
	if origin == "" {
		return time.Time{}, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	until, ok := t.until[origin]
	if !ok || !now.Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// extend throttles origin until the given time. A wait never gets shorter.
func (t *throttles) extend(origin string, until time.Time) {
	if origin == "" || until.IsZero() {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if cur, ok := t.until[origin]; !ok || until.After(cur) {
		t.until[origin] = until
	}
}
