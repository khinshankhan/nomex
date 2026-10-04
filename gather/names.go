package gather

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// Names turns chosen names into domains.
//
// A bare label ("matterwave") is joined to each TLD; a name that already has a
// suffix ("matterwave.dev") is taken as written. Input is lowercased, and
// duplicates are dropped keeping the first occurrence, so output order follows
// input order.
//
// Every label is validated here rather than left to the registry. A malformed
// name fails RDAP as ErrInvalidQuery, which is an allowlisted reason to write
// to blocked, so a typo would otherwise be blocklisted permanently.
func Names(names []string, tlds []string) ([]string, error) {
	for _, tld := range tlds {
		if err := validLabel(tld); err != nil {
			return nil, fmt.Errorf("TLD %q: %w", tld, err)
		}
	}

	seen := make(map[string]bool)
	var out []string
	add := func(domain string) {
		if !seen[domain] {
			seen[domain] = true
			out = append(out, domain)
		}
	}

	for _, raw := range names {
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if name == "" {
			continue
		}

		for label := range strings.SplitSeq(name, ".") {
			if err := validLabel(label); err != nil {
				return nil, fmt.Errorf("name %q: %w", raw, err)
			}
		}

		if strings.Contains(name, ".") {
			add(name)
			continue
		}
		if len(tlds) == 0 {
			return nil, fmt.Errorf("name %q has no suffix and no TLDs were given", raw)
		}
		for _, tld := range tlds {
			add(name + "." + tld)
		}
	}
	return out, nil
}

// ReadNames reads one name per line, skipping blank lines and # comments, so a
// word list can be kept annotated.
func ReadNames(r io.Reader) ([]string, error) {
	var out []string

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read names: %w", err)
	}
	return out, nil
}

// SeedDomains writes the given domains that are not already present, due
// immediately. Existing rows are left alone, as with Seed.
//
// Returns the number of rows actually inserted.
func SeedDomains(ctx context.Context, db *data.DB, domains []string, priority int64) (int64, error) {
	due := sqltime.At(time.Now().Add(-time.Minute))

	var inserted int64
	for start := 0; start < len(domains); start += DefaultBatch {
		end := min(start+DefaultBatch, len(domains))

		n, err := insertBatch(ctx, db, domains[start:end], due, priority)
		if err != nil {
			return inserted, err
		}
		inserted += n
	}
	return inserted, nil
}

// validLabel applies the hostname rules: 1-63 characters of a-z, 0-9 and '-',
// not starting or ending with a hyphen.
func validLabel(label string) error {
	switch {
	case label == "":
		return fmt.Errorf("empty label")
	case len(label) > 63:
		return fmt.Errorf("label %q is over 63 characters", label)
	case label[0] == '-' || label[len(label)-1] == '-':
		return fmt.Errorf("label %q starts or ends with a hyphen", label)
	}

	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			// Internationalized names would need punycode first; refusing is
			// safer than sending something the registry will reject.
			return fmt.Errorf("label %q contains %q: only a-z, 0-9 and '-' are allowed", label, r)
		}
	}
	return nil
}
