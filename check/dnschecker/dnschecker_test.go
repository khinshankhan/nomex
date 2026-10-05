package dnschecker

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/khinshankhan/nomex/check"
)

// A resolver failure must never look like an answer. The old code returned a
// 500 here, stored it with the real results, and blocked domains on timeout.
func TestCheckSeparatesFailureFromAnswer(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  check.Status
		wantErr     bool
		wantRetry   bool
		wantErrKind string
	}{
		{
			// authoritative "does not resolve", a registered domain doesn't
			// have to be delegated
			name:       "not found is not available",
			err:        &net.DNSError{Err: "no such host", IsNotFound: true},
			wantStatus: check.StatusUnknown,
			wantErr:    false,
		},
		{
			name:        "timeout established nothing",
			err:         &net.DNSError{Err: "i/o timeout", IsTimeout: true},
			wantStatus:  check.StatusUnknown,
			wantErr:     true,
			wantRetry:   true,
			wantErrKind: "dns timeout",
		},
		{
			name:        "temporary failure established nothing",
			err:         &net.DNSError{Err: "server misbehaving", IsTemporary: true},
			wantStatus:  check.StatusUnknown,
			wantErr:     true,
			wantRetry:   true,
			wantErrKind: "dns temporary failure",
		},
		{
			name:        "unknown failure established nothing",
			err:         errors.New("resolver exploded"),
			wantStatus:  check.StatusUnknown,
			wantErr:     true,
			wantRetry:   true,
			wantErrKind: "dns failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(fakeLookup(nil, tt.err))
			got := c.Check(context.Background(), "x.dev")

			if got.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s", got.Status, tt.wantStatus)
			}
			if (got.Err != nil) != tt.wantErr {
				t.Errorf("Err = %v, want error=%v", got.Err, tt.wantErr)
			}
			if got.Retryable != tt.wantRetry {
				t.Errorf("Retryable = %v, want %v", got.Retryable, tt.wantRetry)
			}
			if tt.wantErrKind != "" && got.ErrKind != tt.wantErrKind {
				t.Errorf("ErrKind = %q, want %q", got.ErrKind, tt.wantErrKind)
			}
			// nothing from DNS should ever block a domain
			if got.BlockReason != "" {
				t.Errorf("BlockReason = %q; DNS cannot establish a permanent fact", got.BlockReason)
			}
		})
	}
}

// DNS can only prove the positive.
func TestCheckRegisteredWhenDelegated(t *testing.T) {
	c := New(fakeLookup([]*net.NS{{Host: "ns1.example."}}, nil))

	got := c.Check(context.Background(), "x.dev")

	if got.Status != check.StatusRegistered {
		t.Errorf("status = %s, want registered", got.Status)
	}
	if got.Failed() {
		t.Errorf("reported an error: %v", got.Err)
	}
	// no expiry from DNS, so the row gets the default TTL
	if got.Expiration != nil {
		t.Error("DNS produced an expiration date, which it cannot know")
	}
	if got.Source != "dns" {
		t.Errorf("source = %q, want dns", got.Source)
	}
}

// DNS can never report StatusNotFound.
func TestCheckNeverReportsNotFound(t *testing.T) {
	cases := []struct {
		ns  []*net.NS
		err error
	}{
		{nil, &net.DNSError{Err: "no such host", IsNotFound: true}},
		{nil, nil},
		{[]*net.NS{}, nil},
		{nil, errors.New("boom")},
	}

	for _, tc := range cases {
		got := New(fakeLookup(tc.ns, tc.err)).Check(context.Background(), "x.dev")
		if got.Status == check.StatusNotFound {
			t.Errorf("ns=%v err=%v produced StatusNotFound; DNS cannot establish availability", tc.ns, tc.err)
		}
	}
}

func TestCheckRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := New(fakeLookup(nil, context.Canceled)).Check(ctx, "x.dev")

	if got.Status != check.StatusUnknown {
		t.Errorf("status = %s, want unknown", got.Status)
	}
	if !got.Failed() {
		t.Error("a cancelled lookup reported success")
	}
}

// fakeLookup always returns ns and err, so tests don't need a network.
func fakeLookup(ns []*net.NS, err error) Option {
	return withLookup(func(context.Context, string) ([]*net.NS, error) {
		return ns, err
	})
}
