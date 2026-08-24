package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

func runList(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	dbPath := flags.String("db", defaultDBPath, "path to the database")
	status := flags.String("status", string(check.StatusNotFound), "status to list; empty for all")
	tld := flags.String("tld", "", "restrict to one suffix")
	length := flags.Int64("len", 0, "restrict to labels of this length")
	freshOnly := flags.Bool("fresh", false, "omit answers past their TTL")
	limit := flags.Int64("limit", 0, "maximum rows (0 = no limit)")
	out := flags.String("o", "", "write to this file instead of stdout")
	verbose := flags.Bool("v", false, "show status, age and expiry")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex list [flags]

Reports what the database knows. Defaults to available domains.

  nomex list                          available domains
  nomex list -tld dev -len 4          four-letter .dev
  nomex list -fresh -v                only trustworthy answers, with ages
  nomex list -o available-domains.txt

An answer past its TTL is still shown by default, marked stale: knowing
the answer is old is different from not having one.

flags:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}

	db, err := data.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// The query treats 0 as "no limit"; SQLite wants -1.
	lim := *limit
	if lim <= 0 {
		lim = -1
	}

	rows, err := db.ListChecks(ctx, sqlcgen.ListChecksParams{
		Status:    *status,
		Suffix:    strings.TrimPrefix(*tld, "."),
		LabelLen:  *length,
		FreshOnly: boolArg(*freshOnly),
		Lim:       lim,
	})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}

	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	buf := bufio.NewWriter(w)
	defer buf.Flush()

	var stale int
	now := time.Now()
	for _, r := range rows {
		if r.Stale {
			stale++
		}
		if !*verbose {
			fmt.Fprintln(buf, r.Domain)
			continue
		}
		fmt.Fprintf(buf, "%-24s %-11s %-12s %s\n",
			r.Domain, r.Status, age(now, r.CheckedAt), staleness(r.Stale, r.Expiration))
	}
	buf.Flush()

	// To stderr so it does not land in a redirected list.
	fmt.Fprintf(os.Stderr, "%d rows", len(rows))
	if stale > 0 {
		fmt.Fprintf(os.Stderr, ", %d past their TTL", stale)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

func boolArg(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// age reports how long ago the answer was established, which is the number
// that says how much to trust it.
func age(now time.Time, checked *sqltime.UTC) string {
	if checked == nil {
		return "never"
	}

	d := now.Sub(checked.Time)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func staleness(stale bool, expiration *sqltime.UTC) string {
	var parts []string
	if stale {
		parts = append(parts, "stale")
	}
	if expiration != nil {
		parts = append(parts, "expires "+expiration.Format("2006-01-02"))
	}
	return strings.Join(parts, "  ")
}
