package dnschecker

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/khinshankhan/nomex/check"
)

// DNS can prove a domain is registered, never that it's available, and a
// resolver failure must never look like an answer.
func TestCheck(t *testing.T) {
	tests := []struct {
		name        string
		ns          []*net.NS
		err         error
		wantStatus  check.Status
		wantErr     bool
		wantErrKind string
	}{
		{
			name:       "delegated is registered",
			ns:         []*net.NS{{Host: "ns1.example."}},
			wantStatus: check.StatusRegistered,
		},
		{
			// a registered domain doesn't have to be delegated
			name:       "not found is not available",
			err:        &net.DNSError{Err: "no such host", IsNotFound: true},
			wantStatus: check.StatusUnknown,
		},
		{
			name:       "no records is not available",
			wantStatus: check.StatusUnknown,
		},
		{
			name:       "empty records is not available",
			ns:         []*net.NS{},
			wantStatus: check.StatusUnknown,
		},
		{
			name:        "timeout established nothing",
			err:         &net.DNSError{Err: "i/o timeout", IsTimeout: true},
			wantStatus:  check.StatusUnknown,
			wantErr:     true,
			wantErrKind: "dns timeout",
		},
		{
			name:        "temporary failure established nothing",
			err:         &net.DNSError{Err: "server misbehaving", IsTemporary: true},
			wantStatus:  check.StatusUnknown,
			wantErr:     true,
			wantErrKind: "dns temporary failure",
		},
		{
			name:        "unknown failure established nothing",
			err:         errors.New("resolver exploded"),
			wantStatus:  check.StatusUnknown,
			wantErr:     true,
			wantErrKind: "dns failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(fakeLookup(tt.ns, tt.err)).Check(context.Background(), "x.dev")

			if got.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s", got.Status, tt.wantStatus)
			}
			if got.Source != "dns" {
				t.Errorf("source = %q, want dns", got.Source)
			}
			if (got.Err != nil) != tt.wantErr {
				t.Errorf("Err = %v, want error=%v", got.Err, tt.wantErr)
			}
			// every failure is worth retrying, nothing about DNS is permanent
			if got.Retryable != tt.wantErr {
				t.Errorf("Retryable = %v, want %v", got.Retryable, tt.wantErr)
			}
			if got.ErrKind != tt.wantErrKind {
				t.Errorf("ErrKind = %q, want %q", got.ErrKind, tt.wantErrKind)
			}
			if got.BlockReason != "" {
				t.Errorf("BlockReason = %q, DNS can't block a domain", got.BlockReason)
			}
			if got.Expiration != nil {
				t.Errorf("expiration = %v, DNS can't know one", got.Expiration)
			}
		})
	}
}

// fakeLookup always returns ns and err, so tests don't need a network.
func fakeLookup(ns []*net.NS, err error) Option {
	return withLookup(func(context.Context, string) ([]*net.NS, error) {
		return ns, err
	})
}
