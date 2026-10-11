package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/check/dnschecker"
	"github.com/khinshankhan/nomex/check/rdapchecker"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/sweep"
)

func runSweep(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("sweep", flag.ContinueOnError)
	dbPath := flags.String("db", config.DB(), "path to the database")
	limits := limitFlags(flags)
	attempts := flags.Int("attempts", sweep.DefaultMaxAttempts, "retries per domain within one sweep")
	workers := flags.Int("workers", sweep.DefaultWorkers, "concurrent checks")
	batch := flags.Int("batch", sweep.DefaultBatch, "domains claimed per round")
	limit := flags.Int64("limit", 0, "stop after this many checks (0 = until interrupted)")
	once := flags.Bool("once", false, "run a single round and exit")
	quiet := flags.Bool("quiet", false, "only report the summary")
	useDNS := flags.Bool("dns", false, "check DNS first; skips RDAP for domains that resolve")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex sweep [flags]

Checks domains whose answers have gone stale and writes the results back.
Runs until interrupted.

  nomex sweep -once -limit 10     # a taste, to see it work
  nomex sweep -rate 1             # 1/s even where a registry allows more
  nomex sweep                     # until Ctrl-C

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
	if err := atLeastOne(flags, "workers", "attempts", "batch"); err != nil {
		return err
	}

	if err := requireContact(); err != nil {
		return err
	}

	db, err := data.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	checker, err := rdapchecker.New(userAgent(platform.Version("nomex")))
	if err != nil {
		return err
	}

	var preCheck check.Checker
	if *useDNS {
		preCheck = dnschecker.New()
		fmt.Fprintln(os.Stderr, "dns pre-check enabled: registered domains skip RDAP, but store no expiry")
	}

	fmt.Fprintf(os.Stderr, "sweeping at %s, %d workers\n", describeLimits(lims), *workers)

	var (
		mu                                     sync.Mutex
		registered, available, failed, skipped int64
	)
	done, err := sweep.Run(ctx, db, checker, sweep.Options{
		Limits:      lims,
		MaxAttempts: *attempts,
		Workers:     *workers,
		Batch:       *batch,
		Limit:       *limit,
		Once:        *once,
		Origins:     checker,
		PreCheck:    preCheck,
		Progress: func(s sweep.Stat) {
			// workers report concurrently
			mu.Lock()
			defer mu.Unlock()

			if s.Skipped {
				skipped++
				if !*quiet {
					fmt.Fprintf(os.Stderr, "  %-24s %-12s %s throttled\n", s.Domain, "skipped", s.Origin)
				}
				return
			}

			switch {
			case s.Err != nil:
				failed++
			case s.Status == check.StatusRegistered:
				registered++
			case s.Status == check.StatusNotFound:
				available++
			}

			if *quiet {
				return
			}
			if s.Err != nil {
				fmt.Fprintf(os.Stderr, "  %-24s %-12s %v\n", s.Domain, "failed", s.Err)
				return
			}
			fmt.Fprintf(os.Stderr, "  %-24s %s\n", s.Domain, s.Status)
		},
	})

	// Ctrl-C is the normal way out
	if err != nil && !sweep.Cancelled(err) {
		return fmt.Errorf("after %d checks: %w", done, err)
	}

	fmt.Fprintf(os.Stderr, "\n%d checked: %d registered, %d available, %d failed\n",
		done, registered, available, failed)
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "%d skipped: their registry asked us to wait\n", skipped)
	}
	if done > 0 {
		fmt.Fprintf(os.Stderr, "available: %.1f%%\n", float64(available)/float64(done)*100)
	}
	return nil
}
