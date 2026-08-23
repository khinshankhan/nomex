package sqltime

import (
	"testing"
	"time"
)

// The point of the type: a local time must not reach the database carrying its
// offset, because SQLite compares DATETIME text and never interprets one.
func TestValueIsAlwaysUTC(t *testing.T) {
	east := time.FixedZone("UTC+9", 9*3600)
	west := time.FixedZone("UTC-4", -4*3600)

	for _, loc := range []*time.Location{east, west, time.Local, time.UTC} {
		local := time.Date(2026, 8, 23, 12, 0, 0, 0, loc)

		v, err := At(local).Value()
		if err != nil {
			t.Fatalf("Value: %v", err)
		}
		got, ok := v.(time.Time)
		if !ok {
			t.Fatalf("Value returned %T, want time.Time", v)
		}
		if got.Location() != time.UTC {
			t.Errorf("%v: stored in %v, want UTC", loc, got.Location())
		}
		if !got.Equal(local) {
			t.Errorf("%v: %v is not the same instant as %v", loc, got, local)
		}
	}
}

func TestScanNormalises(t *testing.T) {
	west := time.FixedZone("UTC-4", -4*3600)
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"time.Time in another zone", time.Date(2026, 8, 23, 12, 0, 0, 0, west), "2026-08-23T16:00:00Z"},
		{"rfc3339 text", "2026-08-23T16:00:00Z", "2026-08-23T16:00:00Z"},
		{"rfc3339 with offset", "2026-08-23T12:00:00-04:00", "2026-08-23T16:00:00Z"},
		// SQLite's CURRENT_TIMESTAMP default: no zone, and UTC by definition.
		{"sqlite CURRENT_TIMESTAMP", "2026-08-23 16:00:00", "2026-08-23T16:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u UTC
			if err := u.Scan(tt.in); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if got := u.Time.Format(time.RFC3339); got != tt.want {
				t.Errorf("Scan(%v) = %s, want %s", tt.in, got, tt.want)
			}
			if u.Time.Location() != time.UTC {
				t.Errorf("scanned into %v, want UTC", u.Time.Location())
			}
		})
	}
}

func TestPtrPreservesNil(t *testing.T) {
	if Ptr(nil) != nil {
		t.Error("Ptr(nil) was not nil")
	}
	now := time.Now()
	if got := Ptr(&now); got == nil || got.Location() != time.UTC {
		t.Error("Ptr did not normalise")
	}
	if Time(nil) != nil {
		t.Error("Time(nil) was not nil")
	}
}
