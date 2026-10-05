package rdapchecker

import (
	"testing"
	"time"

	"github.com/khinshankhan/nomex/check"

	"github.com/khinshankhan/rdap"
)

// Only errors about the domain itself can block it. Using !rdap.IsRetryable(err)
// instead would block on server and config errors too, a quieter version of
// the November bug.
func TestBlockableIsAnAllowlist(t *testing.T) {
	tests := []struct {
		kind      rdap.ErrorKind
		wantBlock bool
	}{
		{rdap.ErrNoServer, true},
		{rdap.ErrInvalidQuery, true},
		{rdap.ErrRefused, true},

		// not retryable, but not about the domain either
		{rdap.ErrUnknown, false},
		{rdap.ErrTooManyRedirects, false},
		{rdap.ErrRedirectRefused, false},
		{rdap.ErrRedirectLoop, false},
		{rdap.ErrNotSupported, false},

		// retryable
		{rdap.ErrTimeout, false},
		{rdap.ErrRateLimited, false},
		{rdap.ErrServerFailure, false},
		{rdap.ErrTransport, false},
		{rdap.ErrBootstrap, false},
	}

	for _, tt := range tests {
		t.Run(tt.kind.String(), func(t *testing.T) {
			// go through classify, checking the map alone would pass even if
			// Check ignored it
			got := classify(check.Result{}, rdap.Result{
				Err:       &rdap.Error{Kind: tt.kind},
				Retryable: rdap.IsRetryable(&rdap.Error{Kind: tt.kind}),
			})

			if blocked := got.BlockReason != ""; blocked != tt.wantBlock {
				t.Errorf("BlockReason = %q (blocked=%v), want blocked=%v",
					got.BlockReason, blocked, tt.wantBlock)
			}
			if got.ErrKind == "" {
				t.Error("ErrKind was not set")
			}
		})
	}
}

// No retryable kind should ever be in the allowlist, even if the library adds
// more later.
func TestRetryableIsNeverBlockable(t *testing.T) {
	for kind := range blockable {
		err := &rdap.Error{Kind: kind}
		if rdap.IsRetryable(err) {
			t.Errorf("%s is both retryable and blockable", kind)
		}
	}
}

func TestParseEvents(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantExp string
		wantReg string
	}{
		{
			// Verisign sends whole seconds
			name: "second precision",
			body: `{"events":[
				{"eventAction":"registration","eventDate":"1995-08-14T04:00:00Z"},
				{"eventAction":"expiration","eventDate":"2027-08-13T04:00:00Z"}]}`,
			wantReg: "1995-08-14", wantExp: "2027-08-13",
		},
		{
			// Google sends milliseconds
			name: "millisecond precision",
			body: `{"events":[
				{"eventAction":"expiration","eventDate":"2027-07-02T03:23:42.407Z"}]}`,
			wantExp: "2027-07-02",
		},
		{
			// about the answer's freshness, not the domain
			name: "last update of rdap database is ignored",
			body: `{"events":[
				{"eventAction":"last update of RDAP database","eventDate":"2026-08-23T15:05:42Z"}]}`,
		},
		{
			name: "no events",
			body: `{"objectClassName":"domain"}`,
		},
		{
			name: "unparseable date is skipped, not fatal",
			body: `{"events":[{"eventAction":"expiration","eventDate":"whenever"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEvents([]byte(tt.body))
			if err != nil {
				t.Fatalf("parseEvents: %v", err)
			}
			if fmtDate(got.expiration) != tt.wantExp {
				t.Errorf("expiration = %q, want %q", fmtDate(got.expiration), tt.wantExp)
			}
			if fmtDate(got.registration) != tt.wantReg {
				t.Errorf("registration = %q, want %q", fmtDate(got.registration), tt.wantReg)
			}
		})
	}
}

func TestParseEventsRejectsGarbage(t *testing.T) {
	if _, err := parseEvents([]byte("not json")); err == nil {
		t.Error("parseEvents accepted invalid JSON")
	}
}

func TestOriginOf(t *testing.T) {
	tests := []struct{ in, want string }{
		// Result.Server has the object path, using it as is would give one
		// entry per domain
		{"https://rdap.verisign.com/com/v1/domain/example.com", "https://rdap.verisign.com"},
		{"https://pubapi.registry.google/rdap/domain/uptogood.dev", "https://pubapi.registry.google"},
		{"https://rdap.example:8443/domain/x.dev", "https://rdap.example:8443"},
		{"", ""},
		{"://nonsense", ""},
	}

	for _, tt := range tests {
		if got := originOf(tt.in); got != tt.want {
			t.Errorf("originOf(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func fmtDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}
