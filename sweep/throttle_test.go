package sweep

import (
	"testing"
	"time"
)

func TestThrottlesActive(t *testing.T) {
	now := time.Now()
	th := newThrottles(map[string]time.Time{
		"https://open":   now.Add(-time.Minute),
		"https://closed": now.Add(time.Minute),
	})

	if _, ok := th.active("https://open", now); ok {
		t.Error("lapsed throttle still active")
	}
	if _, ok := th.active("https://unknown", now); ok {
		t.Error("unknown origin throttled")
	}
	if _, ok := th.active("", now); ok {
		t.Error("empty origin throttled")
	}
	if until, ok := th.active("https://closed", now); !ok || !until.Equal(now.Add(time.Minute)) {
		t.Errorf("active = %v, %v, want %v, true", until, ok, now.Add(time.Minute))
	}
}

// A wait can grow but never shrink, so a short Retry-After can't cancel a long
// one.
func TestThrottlesExtend(t *testing.T) {
	now := time.Now()
	th := newThrottles(nil)

	th.extend("https://a", now.Add(time.Hour))
	th.extend("https://a", now.Add(time.Minute))
	if until, _ := th.active("https://a", now); !until.Equal(now.Add(time.Hour)) {
		t.Errorf("after a shorter wait: until = %v, want %v", until, now.Add(time.Hour))
	}

	th.extend("https://a", now.Add(2*time.Hour))
	if until, _ := th.active("https://a", now); !until.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("after a longer wait: until = %v, want %v", until, now.Add(2*time.Hour))
	}
}
