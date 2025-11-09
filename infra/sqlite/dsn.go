package sqlite

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Options struct {
	Path string

	MaxOpenConns int
	MaxIdleConns int

	BusyTimeout       time.Duration
	JournalMode       string
	Synchronous       string
	ForeignKeys       bool
	TempStoreMemory   bool
	CacheSizeKiB      int
	MmapSizeBytes     int64
	WALAutoCheckpoint int

	ReadOnly bool
}

func DefaultOptions(path string) Options {
	return Options{
		Path:              path,
		MaxOpenConns:      16,
		MaxIdleConns:      16,
		BusyTimeout:       5 * time.Second,
		JournalMode:       "WAL",
		Synchronous:       "NORMAL",
		ForeignKeys:       true,
		TempStoreMemory:   true,
		CacheSizeKiB:      -8000,
		MmapSizeBytes:     0,
		WALAutoCheckpoint: 0,
	}
}

func buildDSN(o Options) string {
	q := url.Values{}

	if o.ReadOnly {
		q.Set("mode", "ro")
	}

	if o.BusyTimeout > 0 {
		q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", int(o.BusyTimeout/time.Millisecond)))
	}
	if o.JournalMode != "" {
		q.Add("_pragma", fmt.Sprintf("journal_mode(%s)", strings.ToUpper(o.JournalMode)))
	}
	if o.Synchronous != "" {
		q.Add("_pragma", fmt.Sprintf("synchronous(%s)", strings.ToUpper(o.Synchronous)))
	}
	if o.ForeignKeys {
		q.Add("_pragma", "foreign_keys(ON)")
	}
	if o.TempStoreMemory {
		q.Add("_pragma", "temp_store(MEMORY)")
	}
	if o.CacheSizeKiB != 0 {
		q.Add("_pragma", fmt.Sprintf("cache_size(%d)", o.CacheSizeKiB))
	}
	if o.MmapSizeBytes > 0 {
		q.Add("_pragma", fmt.Sprintf("mmap_size(%d)", o.MmapSizeBytes))
	}
	if o.WALAutoCheckpoint > 0 {
		q.Add("_pragma", fmt.Sprintf("wal_autocheckpoint(%d)", o.WALAutoCheckpoint))
	}

	if qs := q.Encode(); qs != "" {
		return o.Path + "?" + qs
	}
	return o.Path
}
