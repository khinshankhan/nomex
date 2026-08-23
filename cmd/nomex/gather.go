package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/gather"
)

// runGather seeds candidate domains into the database.
//
// Re-running over a space that overlaps an earlier run is cheap and lossless:
// existing rows keep their status and priority, so the usual workflow is to
// widen gradually -- two characters, then three -- rather than committing to
// the whole space up front.
func runGather(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("gather", flag.ContinueOnError)
	dbPath := flags.String("db", defaultDBPath, "path to the database")
	tlds := flags.String("tlds", "com,net", "comma-separated TLDs")
	minLen := flags.Int("min", 1, "shortest label to generate")
	maxLen := flags.Int("max", 3, "longest label to generate")
	priority := flags.Int64("priority", 0, "priority for seeded rows; negative sorts below the sweep")
	alphabet := flags.String("alphabet", gather.Alphabet, "label alphabet")
	batch := flags.Int("batch", gather.DefaultBatch, "rows per transaction")
	dryRun := flags.Bool("n", false, "report the size of the space and exit")
	force := flags.Bool("y", false, "skip the confirmation prompt for large runs")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex gather [flags]

Seeds candidate domains. Existing rows are left untouched, so widening a
space re-walks the earlier candidates without resetting them.

  nomex gather -tlds dev -max 2
  nomex gather -tlds dev -max 3          # adds only the 3-character labels
  nomex gather -tlds dev,net -max 4 -n   # size the space first

flags:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}

	spec := gather.Spec{
		TLDs:     splitList(*tlds),
		MinLen:   *minLen,
		MaxLen:   *maxLen,
		Alphabet: *alphabet,
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	total, err := spec.Count()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "%s: %s candidates (%d-%d chars, %d TLDs)\n",
		strings.Join(spec.TLDs, ","), commas(total), spec.MinLen, spec.MaxLen, len(spec.TLDs))

	if *dryRun {
		// Seeding measured ~57k rows/sec; checking is bounded by registry rate
		// limits, so the two estimates differ by orders of magnitude and the
		// second one is the one that matters.
		fmt.Fprintf(os.Stderr, "  seeding:  about %v\n", estimate(total, 57_000))
		fmt.Fprintf(os.Stderr, "  checking: about %v at 3 queries/sec\n", estimate(total, 3))
		return nil
	}

	// A large run writes for minutes and gigabytes. Ask first unless told not
	// to, since the flags make it easy to add a character without noticing the
	// space grew 26-fold.
	if total >= confirmThreshold && !*force {
		if !confirm(fmt.Sprintf("seed %s candidates (about %s on disk)?", commas(total), diskEstimate(total))) {
			return fmt.Errorf("cancelled")
		}
	}

	db, err := data.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var lastReport time.Time
	inserted, err := gather.Seed(ctx, db, spec, gather.SeedOptions{
		Priority: *priority,
		Batch:    *batch,
		Progress: func(p gather.Progress) {
			// Throttled: one line a second, not one per batch.
			if time.Since(lastReport) < time.Second && p.Seen < p.Total {
				return
			}
			lastReport = time.Now()
			fmt.Fprintf(os.Stderr, "\r  %s/%s seen, %s new, %.0f/s, %v left    ",
				commas(p.Seen), commas(p.Total), commas(p.Inserted), p.Rate(), p.Remaining().Round(time.Second))
		},
	})
	fmt.Fprintln(os.Stderr)

	if err != nil {
		// A cancelled run has still committed everything up to the last batch.
		return fmt.Errorf("seeded %s rows before stopping: %w", commas(inserted), err)
	}

	fmt.Fprintf(os.Stderr, "seeded %s new rows (%s already present)\n",
		commas(inserted), commas(total-inserted))
	return nil
}

// confirmThreshold is where a run stops being obviously cheap. Below it,
// seeding finishes in seconds and costs a few hundred megabytes.
const confirmThreshold = 1_000_000

// confirm asks on stderr and reads a line from stdin. A non-interactive stdin
// reads EOF, which answers no -- so a scripted run needs -y and cannot be
// silently blocked waiting for input.
func confirm(question string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// diskEstimate reports the approximate database size for n rows, from a
// measured 177 bytes/row including both indexes.
func diskEstimate(n int64) string {
	const bytesPerRow = 177
	b := float64(n) * bytesPerRow
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", b/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.0f MB", b/1e6)
	default:
		return fmt.Sprintf("%.0f KB", b/1e3)
	}
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, strings.TrimPrefix(part, "."))
		}
	}
	return out
}

func estimate(rows, perSecond int64) time.Duration {
	if perSecond <= 0 {
		return 0
	}
	return (time.Duration(rows/perSecond) * time.Second).Round(time.Second)
}

// commas formats n with thousands separators, because the difference between
// 1.4M and 37M candidates is the whole decision and is easy to misread.
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return "-" + commas(-n)
	}

	var sb strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
