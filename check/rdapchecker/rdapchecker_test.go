package rdapchecker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/rdap/bootstrap"
)

// registry is a fake RDAP server, and its bootstrap registry, for the "test"
// suffix. answers maps a domain to its response.
func registry(t *testing.T, answers map[string]http.HandlerFunc) (*Checker, string) {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("GET /bootstrap/dns.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":"1.0","publication":"2026-01-01T00:00:00Z","services":[[["test"],["%s/rdap/"]]]}`, srv.URL)
	})
	mux.HandleFunc("GET /rdap/domain/{name}", func(w http.ResponseWriter, r *http.Request) {
		answer, ok := answers[r.PathValue("name")]
		if !ok {
			t.Errorf("unexpected query for %s", r.PathValue("name"))
			http.NotFound(w, r)
			return
		}
		answer(w, r)
	})

	cache, err := bootstrap.NewDiskCache(t.TempDir())
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	c, err := New("nomex-test (+https://example.com)", WithBootstrap(
		bootstrap.WithBaseURL(srv.URL+"/bootstrap/"),
		bootstrap.WithCache(cache),
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv.URL
}

func domainObject(events string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rdap+json")
		fmt.Fprintf(w, `{"objectClassName":"domain","ldhName":%q,"events":[%s]}`, r.PathValue("name"), events)
	}
}

func status(code int, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
	}
}

func date(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

// only failures about the domain itself block it
func TestCheck(t *testing.T) {
	tests := []struct {
		name           string
		domain         string
		answer         http.HandlerFunc
		wantStatus     check.Status
		wantExpiration *time.Time
		wantRegistered *time.Time
		wantErr        bool
		wantRetryable  bool
		wantRetryAfter time.Duration
		wantThrottled  bool
		wantBlock      bool
	}{
		{
			// Verisign sends whole seconds
			name:   "registered with dates",
			domain: "taken.test",
			answer: domainObject(`{"eventAction":"registration","eventDate":"1995-08-14T04:00:00Z"},
				{"eventAction":"expiration","eventDate":"2027-08-13T04:00:00Z"}`),
			wantStatus:     check.StatusRegistered,
			wantRegistered: date("1995-08-14T04:00:00Z"),
			wantExpiration: date("2027-08-13T04:00:00Z"),
		},
		{
			// Google sends milliseconds
			name:           "millisecond dates",
			domain:         "google.test",
			answer:         domainObject(`{"eventAction":"expiration","eventDate":"2027-07-02T03:23:42.407Z"}`),
			wantStatus:     check.StatusRegistered,
			wantExpiration: date("2027-07-02T03:23:42.407Z"),
		},
		{
			name:       "database update isn't an expiry",
			domain:     "fresh.test",
			answer:     domainObject(`{"eventAction":"last update of RDAP database","eventDate":"2026-08-23T15:05:42Z"}`),
			wantStatus: check.StatusRegistered,
		},
		{
			name:       "unreadable date is still registered",
			domain:     "odd.test",
			answer:     domainObject(`{"eventAction":"expiration","eventDate":"whenever"}`),
			wantStatus: check.StatusRegistered,
		},
		{
			name:       "not found is available",
			domain:     "free.test",
			answer:     status(http.StatusNotFound),
			wantStatus: check.StatusNotFound,
		},
		{
			name:           "rate limited",
			domain:         "busy.test",
			answer:         status(http.StatusTooManyRequests, "Retry-After", "120"),
			wantStatus:     check.StatusUnknown,
			wantErr:        true,
			wantRetryable:  true,
			wantRetryAfter: 2 * time.Minute,
			wantThrottled:  true,
		},
		{
			// an overloaded server often stops answering instead of sending a 429
			name:          "timeout throttles",
			domain:        "slow.test",
			answer:        status(http.StatusRequestTimeout),
			wantStatus:    check.StatusUnknown,
			wantErr:       true,
			wantRetryable: true,
			wantThrottled: true,
		},
		{
			name:          "server failure",
			domain:        "down.test",
			answer:        status(http.StatusServiceUnavailable),
			wantStatus:    check.StatusUnknown,
			wantErr:       true,
			wantRetryable: true,
		},
		{
			name:       "refused blocks",
			domain:     "nope.test",
			answer:     status(http.StatusForbidden),
			wantStatus: check.StatusUnknown,
			wantErr:    true,
			wantBlock:  true,
		},
		{
			// not retryable, but about the server
			name:       "unsupported doesn't block",
			domain:     "todo.test",
			answer:     status(http.StatusNotImplemented),
			wantStatus: check.StatusUnknown,
			wantErr:    true,
		},
		{
			name:   "redirect loop doesn't block",
			domain: "loop.test",
			answer: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
			},
			wantStatus: check.StatusUnknown,
			wantErr:    true,
		},
		{
			name:       "no server for the suffix blocks",
			domain:     "nowhere.zz",
			wantStatus: check.StatusUnknown,
			wantErr:    true,
			wantBlock:  true,
		},
		{
			name:       "malformed name blocks",
			domain:     "-bad-.test",
			wantStatus: check.StatusUnknown,
			wantErr:    true,
			wantBlock:  true,
		},
	}

	answers := make(map[string]http.HandlerFunc)
	for _, tt := range tests {
		if tt.answer != nil {
			answers[tt.domain] = tt.answer
		}
	}
	c, origin := registry(t, answers)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Check(t.Context(), tt.domain)

			if got.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s", got.Status, tt.wantStatus)
			}
			if got.Source != "rdap" {
				t.Errorf("source = %q, want rdap", got.Source)
			}
			if !sameTime(got.Expiration, tt.wantExpiration) {
				t.Errorf("expiration = %v, want %v", got.Expiration, tt.wantExpiration)
			}
			if !sameTime(got.Registered, tt.wantRegistered) {
				t.Errorf("registered = %v, want %v", got.Registered, tt.wantRegistered)
			}
			if got.Failed() != tt.wantErr {
				t.Errorf("Err = %v, want error=%v", got.Err, tt.wantErr)
			}
			if got.Retryable != tt.wantRetryable {
				t.Errorf("retryable = %v, want %v", got.Retryable, tt.wantRetryable)
			}
			if got.RetryAfter != tt.wantRetryAfter {
				t.Errorf("retry after = %v, want %v", got.RetryAfter, tt.wantRetryAfter)
			}
			if got.Throttled != tt.wantThrottled {
				t.Errorf("throttled = %v, want %v", got.Throttled, tt.wantThrottled)
			}
			if got.Blockable() != tt.wantBlock {
				t.Errorf("block reason = %q, want blocked=%v", got.BlockReason, tt.wantBlock)
			}
			if tt.wantErr && got.ErrKind == "" {
				t.Error("a failure has no error kind to record")
			}

			// anything that reached the server is attributed to its origin
			if tt.answer != nil {
				if got.Origin != origin {
					t.Errorf("origin = %q, want %q", got.Origin, origin)
				}
				if !strings.HasPrefix(got.Server, origin+"/rdap/domain/") {
					t.Errorf("server = %q, want the answering URL", got.Server)
				}
			}
		})
	}
}

func TestOrigin(t *testing.T) {
	c, origin := registry(t, nil)

	got, err := c.Origin(t.Context(), "anything.test")
	if err != nil || got != origin {
		t.Errorf("Origin(.test) = %q, %v, want %q", got, err, origin)
	}

	// no server isn't an error, the query itself fails
	got, err = c.Origin(t.Context(), "anything.zz")
	if err != nil || got != "" {
		t.Errorf("Origin(.zz) = %q, %v, want no origin", got, err)
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
