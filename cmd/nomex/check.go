package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/check/dnschecker"
	"github.com/khinshankhan/nomex/check/rdapchecker"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/gather"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/sweep"
)

// runCheck checks specific names now, reusing fresh stored answers.
func runCheck(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	dbPath := flags.String("db", config.DB(), "path to the database")
	tlds := flags.String("tlds", "com", "comma-separated TLDs for names given without one")
	force := flags.Bool("force", false, "query even when a fresh answer is stored")
	useDNS := flags.Bool("dns", false, "check DNS first; skips RDAP for domains that resolve")
	limits := limitFlags(flags)
	attempts := flags.Int("attempts", sweep.DefaultMaxAttempts, "retries per domain")
	workers := flags.Int("workers", sweep.DefaultWorkers, "concurrent checks")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex check [flags] name...

Checks specific names. A name without a suffix is tried against each of
-tlds. With no names, or "-", reads one per line from stdin.

  nomex check matterwave standingwave.dev
  nomex check -tlds com,dev,io matterwave
  namingway | nomex check -dns -

Fresh stored answers are reported without querying; -force re-asks.
Exits non-zero if any name could not be checked.

flags:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}

	lims, err := limits()
	if err != nil {
		return err
	}

	names := flags.Args()
	if len(names) == 0 || (len(names) == 1 && names[0] == "-") {
		var err error
		if names, err = gather.ReadNames(os.Stdin); err != nil {
			return err
		}
	}

	domains, err := gather.Names(names, splitList(*tlds))
	if err != nil {
		return &usageError{err.Error()}
	}
	if len(domains) == 0 {
		return &usageError{"no names given; try: nomex check matterwave"}
	}

	db, err := data.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := gather.SeedDomains(ctx, db, domains, 0); err != nil {
		return err
	}

	now := time.Now()
	blocked := make(map[string]bool)
	var due []string
	for _, d := range domains {
		if b, err := db.IsBlocked(ctx, d); err != nil {
			return err
		} else if b {
			blocked[d] = true
			continue
		}

		row, err := db.GetCheck(ctx, d)
		if err != nil {
			return fmt.Errorf("read %s: %w", d, err)
		}
		answered := row.Status == string(check.StatusRegistered) || row.Status == string(check.StatusNotFound)
		if *force || !answered || !row.FreshUntil.After(now) {
			due = append(due, d)
		}
	}

	var (
		mu       sync.Mutex
		failures = make(map[string]string)
	)
	if len(due) > 0 {
		// only needed when something is queried
		if err := requireContact(); err != nil {
			return err
		}

		checker, err := rdapchecker.New(userAgent(platform.Version("nomex")))
		if err != nil {
			return err
		}

		var preCheck check.Checker
		if *useDNS {
			preCheck = dnschecker.New()
		}

		fmt.Fprintf(os.Stderr, "checking %d of %d (%d answered from the database)\n",
			len(due), len(domains), len(domains)-len(due)-len(blocked))

		_, err = sweep.CheckDomains(ctx, db, checker, due, sweep.Options{
			Limits:      lims,
			MaxAttempts: *attempts,
			Workers:     *workers,
			Origins:     checker,
			PreCheck:    preCheck,
			Progress: func(s sweep.Stat) {
				// workers report concurrently
				mu.Lock()
				defer mu.Unlock()

				switch {
				case s.Skipped:
					failures[s.Domain] = fmt.Sprintf("skipped: %s asked us to wait", s.Origin)
				case s.Err != nil:
					failures[s.Domain] = s.Err.Error()
				}
			},
		})
		if err != nil && !sweep.Cancelled(err) {
			return err
		}
	}

	// report from the database, so fresh and stored answers look the same
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DOMAIN\tSTATUS\tCHECKED\tDETAIL")
	for _, d := range domains {
		if blocked[d] {
			fmt.Fprintf(w, "%s\t%s\t\t%s\n", d, "blocked", "registry will not answer for this name")
			continue
		}
		if msg, failed := failures[d]; failed {
			fmt.Fprintf(w, "%s\t%s\t\t%s\n", d, "failed", msg)
			continue
		}

		row, err := db.GetCheck(ctx, d)
		if err != nil {
			return fmt.Errorf("read %s: %w", d, err)
		}

		detail := staleness(row.Stale, row.Expiration)
		if row.Source != nil && *row.Source == "dns" {
			detail = "via dns, no expiry known"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d, row.Status, age(now, row.CheckedAt), detail)
	}
	w.Flush()

	if err := ctx.Err(); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d of %d could not be checked", len(failures), len(domains))
	}
	return nil
}
