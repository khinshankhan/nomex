package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/khinshankhan/nomex/data"
)

func runStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	dbPath := flags.String("db", defaultDBPath, "path to the database")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex status [flags]

Reports how far the sweep has got, per suffix.

The registered ratio is worth reading: DNS-in-front only pays where most
candidates are taken, and this is that number from real data.

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

	rows, err := db.Progress(ctx)
	if err != nil {
		return fmt.Errorf("progress: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "nothing seeded yet; try: nomex gather -tlds com,net -max 3")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SUFFIX\tTOTAL\tCHECKED\tREGISTERED\tAVAILABLE\tUNKNOWN\tDUE")

	var total, checked, due int64
	for _, r := range rows {
		done := r.Total - r.Unchecked
		total += r.Total
		checked += done
		due += r.Due

		registeredPct := ""
		if done > 0 {
			registeredPct = fmt.Sprintf(" (%.0f%%)", float64(r.Registered)/float64(done)*100)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\t%s\t%s\t%s\n",
			r.Suffix, commas(r.Total), commas(done),
			commas(r.Registered), registeredPct,
			commas(r.Available), commas(r.Unknown), commas(r.Due))
	}
	w.Flush()

	fmt.Fprintf(os.Stderr, "\n%s of %s checked", commas(checked), commas(total))
	if due > 0 {
		fmt.Fprintf(os.Stderr, ", %s due now", commas(due))
	}
	fmt.Fprintln(os.Stderr)

	// Attempts is the table that grows without bound, so surface it before it
	// becomes a surprise.
	attempts, err := db.CountAttempts(ctx)
	if err == nil && attempts > 0 {
		fmt.Fprintf(os.Stderr, "%s recorded failures\n", commas(attempts))
	}
	return nil
}
