package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

type Row struct {
	Domain string
	Code   *int // nil until set (represented as -1 in badger)
	At     *time.Time
}

type checkVal struct {
	Code      int   `json:"code"`       // -1 = unset/pending
	CheckedAt int64 `json:"checked_at"` // unix seconds -- 0 if never
}

/** Final codes are 200 (taken) and 404 (available).
 * NOTE: code will be used to indicate available domains at the time of fetch
 * which is not necessarily the same as still untaken at the time of inquiry
 */
func (v *checkVal) isFinal() bool {
	return v.Code == 200 || v.Code == 404
}

func keyFor(domain string) []byte {
	return []byte("checks/" + domain)
}

func openBadger(dir string) (*badger.DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	opts := badger.DefaultOptions(dir)
	// store values on disk (good for large sets?)
	opts = opts.WithValueDir(dir)
	opts = opts.WithLoggingLevel(badger.WARNING)
	return badger.Open(opts)
}

func bulkEnsureRows(db *badger.DB, domains []string) error {
	wb := db.NewWriteBatch()
	defer wb.Cancel()

	ts := time.Now().Unix()
	val := checkVal{Code: -1, CheckedAt: ts}

	for _, d := range domains {
		k := keyFor(d)
		// only insert if missing
		err := db.View(func(txn *badger.Txn) error {
			_, err := txn.Get(k)
			return err
		})
		if err == nil {
			// exists
			continue
		}
		if err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		b, _ := json.Marshal(val)
		if err := wb.Set(k, b); err != nil {
			return err
		}
	}
	return wb.Flush()
}

func saveCode(db *badger.DB, domain string, code int, at *time.Time) error {
	return db.Update(func(txn *badger.Txn) error {
		k := keyFor(domain)

		// read existing (to preserve if previously final)
		var cur checkVal
		item, err := txn.Get(k)
		if err == nil {
			_ = item.Value(func(v []byte) error { return json.Unmarshal(v, &cur) })
		} else if err != badger.ErrKeyNotFound {
			return err
		}

		cur.Code = code
		if at != nil {
			cur.CheckedAt = at.Unix()
		}
		b, _ := json.Marshal(cur)
		return txn.Set(k, b)
	})
}

func loadPendingFromDB(db *badger.DB) ([]string, error) {
	var out []string
	err := db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte("checks/")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			var v checkVal
			err := item.Value(func(b []byte) error { return json.Unmarshal(b, &v) })
			if err != nil {
				return err
			}
			// is pending?
			if !v.isFinal() {
				d := strings.TrimPrefix(string(item.Key()), "checks/")
				out = append(out, d)
			}
		}
		return nil
	})
	return out, err
}
