package utils

import (
	"time"
)

// format as RFC3339 in UTC, which SQLite accepts as DATETIME TEXT
func ToSQLiteDT(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
