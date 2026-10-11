// Package sqltime carries timestamps to and from SQLite in UTC.
package sqltime

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// UTC is a time.Time that always stores as UTC.
//
// NOTE: SQLite compares DATETIME as text and ignores a stored offset, so an
// hour from now in -04:00 sorts as hours ago.
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

// Text formats t the way the driver writes it, for text comparisons.
func Text(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
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
			// CURRENT_TIMESTAMP has no zone and is UTC
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
