package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/check"
	"github.com/khinshankhan/nomex/check/dnschecker"
	"github.com/khinshankhan/nomex/check/rdapchecker"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/platform/logx"
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

	log := logx.Default().Named("sweep")

	var preCheck check.Checker
	if *useDNS {
		preCheck = dnschecker.New()
		log.Info("dns pre-check enabled, registered domains skip RDAP but store no expiry")
	}

	log.Info("sweeping",
		fields.String("limits", describeLimits(lims)),
		fields.Int("workers", *workers))

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
					log.Info("skipped, registry throttled",
						fields.String("domain", s.Domain),
						fields.String("origin", s.Origin))
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
				log.Warn("check failed",
					fields.String("domain", s.Domain),
					fields.String("origin", s.Origin),
					fields.Error(s.Err))
				return
			}
			log.Info("checked",
				fields.String("domain", s.Domain),
				fields.String("status", string(s.Status)))
		},
	})

	// Ctrl-C is the normal way out
	if err != nil && !sweep.Cancelled(err) {
		return fmt.Errorf("after %d checks: %w", done, err)
	}

	summary := []logx.Field{
		fields.Int64("checked", done),
		fields.Int64("registered", registered),
		fields.Int64("available", available),
		fields.Int64("failed", failed),
		// their registry asked us to wait
		fields.Int64("skipped", skipped),
	}
	if done > 0 {
		summary = append(summary, fields.Float64("available_pct", float64(available)/float64(done)*100))
	}
	log.Info("sweep finished", summary...)
	return nil
}
