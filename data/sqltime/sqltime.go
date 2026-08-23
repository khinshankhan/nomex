// Package sqltime carries timestamps to and from SQLite in UTC.
package sqltime

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// UTC is a time.Time that always stores as UTC.
//
// SQLite has no date type: DATETIME is text, and comparison is lexicographic.
// A stored "-04:00" offset is therefore not interpreted, so a timestamp an hour
// in the future sorts as though it were hours in the past. Normalising in the
// driver value means a caller cannot get it wrong by passing a local time.
//
// Queries still wrap stored timestamps in datetime() when comparing, which
// covers rows written by anything that is not this type -- SQLite's own
// CURRENT_TIMESTAMP default, or a hand-run UPDATE.
type UTC struct {
	time.Time
}

// Now returns the current time, ready to store.
func Now() UTC {
	return UTC{time.Now().UTC()}
}

// At wraps t.
func At(t time.Time) UTC {
	return UTC{t.UTC()}
}

// Ptr wraps t, preserving nil.
func Ptr(t *time.Time) *UTC {
	if t == nil {
		return nil
	}
	return &UTC{t.UTC()}
}

// Time unwraps u, or returns nil.
func Time(u *UTC) *time.Time {
	if u == nil {
		return nil
	}
	t := u.Time
	return &t
}

// Value implements driver.Valuer.
func (u UTC) Value() (driver.Value, error) {
	return u.Time.UTC(), nil
}

// Scan implements sql.Scanner.
func (u *UTC) Scan(v any) error {
	switch t := v.(type) {
	case time.Time:
		u.Time = t.UTC()
		return nil
	case nil:
		u.Time = time.Time{}
		return nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			// SQLite's own CURRENT_TIMESTAMP has no zone and is UTC.
			parsed, err = time.ParseInLocation("2006-01-02 15:04:05", t, time.UTC)
			if err != nil {
				return fmt.Errorf("sqltime: parse %q: %w", t, err)
			}
		}
		u.Time = parsed.UTC()
		return nil
	default:
		return fmt.Errorf("sqltime: cannot scan %T", v)
	}
}
