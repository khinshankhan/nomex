package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/check/rdapchecker"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/sweep"
)

// contactURL is sent to every registry we query, so an operator who wants to
// complain can find someone rather than just blocking the address.
const contactURL = "https://github.com/khinshankhan/nomex"

func runSweep(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("sweep", flag.ContinueOnError)
	dbPath := flags.String("db", defaultDBPath, "path to the database")
	rate := flags.Float64("rate", sweep.DefaultRate, "queries per second across all workers")
	workers := flags.Int("workers", sweep.DefaultWorkers, "concurrent checks")
	batch := flags.Int("batch", sweep.DefaultBatch, "domains claimed per round")
	limit := flags.Int64("limit", 0, "stop after this many checks (0 = until interrupted)")
	once := flags.Bool("once", false, "run a single round and exit")
	quiet := flags.Bool("quiet", false, "only report the summary")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex sweep [flags]

Checks domains whose answers have gone stale and writes the results back.
Runs until interrupted; there is no finish line, because the candidate space
is far larger than the rate limit can drain.

  nomex sweep -once -limit 10     # a taste, to see it work
  nomex sweep -rate 1             # gentler
  nomex sweep                     # until Ctrl-C

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

	ua := platform.UserAgent(platform.Version("nomex"), platform.UserAgentOptions{
		Name:  "nomex",
		URL:   contactURL,
		Extra: map[string]string{"service": "rdap"},
	})
	checker, err := rdapchecker.New(ua)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "sweeping at %g/s with %d workers\n", *rate, *workers)

	var registered, available, failed int64
	done, err := sweep.Run(ctx, db, checker, sweep.Options{
		Rate:    *rate,
		Workers: *workers,
		Batch:   *batch,
		Limit:   *limit,
		Once:    *once,
		Progress: func(s sweep.Stat) {
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

	// Cancellation is how this command normally ends, not a failure.
	if err != nil && !sweep.Cancelled(err) {
		return fmt.Errorf("after %d checks: %w", done, err)
	}

	fmt.Fprintf(os.Stderr, "\n%d checked: %d registered, %d available, %d failed\n",
		done, registered, available, failed)
	if done > 0 {
		fmt.Fprintf(os.Stderr, "available: %.1f%%\n", float64(available)/float64(done)*100)
	}
	return nil
}
