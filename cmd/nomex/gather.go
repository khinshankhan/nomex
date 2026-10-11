package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/khinshankhan/nomex/check/rdapchecker"
	"github.com/khinshankhan/nomex/data"
	"github.com/khinshankhan/nomex/gather"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/sweep"
)

// runGather seeds candidate domains. Existing rows are left alone, so a space
// can be widened gradually.
func runGather(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("gather", flag.ContinueOnError)
	dbPath := flags.String("db", config.DB(), "path to the database")
	tlds := flags.String("tlds", "com,net", "comma-separated TLDs")
	minLen := flags.Int("min", 1, "shortest label to generate")
	maxLen := flags.Int("max", 3, "longest label to generate")
	priority := flags.Int64("priority", 0, "priority for seeded rows; negative sorts below the sweep")
	alphabet := flags.String("alphabet", gather.Alphabet, "label alphabet")
	batch := flags.Int("batch", gather.DefaultBatch, "rows per transaction")
	dryRun := flags.Bool("n", false, "report the size of the space and exit")
	force := flags.Bool("y", false, "skip the confirmation prompt for large runs")
	namesFrom := flags.String("names", "", "seed names from this file, one per line, instead of generating (- for stdin)")

	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: nomex gather [flags]

Seeds candidate domains. Existing rows are left alone, so widening a space
doesn't reset anything.

  nomex gather -tlds dev -max 2
  nomex gather -tlds dev -max 3          # adds only the 3-character labels
  nomex gather -tlds dev,net -max 4 -n   # size the space first
  nomex gather -tlds com,dev -names words.txt

flags:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}

	if *namesFrom != "" {
		return gatherNames(ctx, *dbPath, *namesFrom, splitList(*tlds), *priority, *dryRun)
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
		// seeding measured about 57k rows/sec
		fmt.Fprintf(os.Stderr, "  seeding:  about %v\n", estimate(total, 57_000))
		printCheckEstimates(ctx, total/int64(len(spec.TLDs)), spec.TLDs)
		return nil
	}

	// a big run writes for minutes and gigabytes, so ask unless -y
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
			// once a second
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
		// earlier batches are still committed
		return fmt.Errorf("seeded %s rows before stopping: %w", commas(inserted), err)
	}

	fmt.Fprintf(os.Stderr, "seeded %s new rows (%s already present)\n",
		commas(inserted), commas(total-inserted))
	return nil
}

// gatherNames seeds a chosen list instead of a generated space.
func gatherNames(ctx context.Context, dbPath, from string, tlds []string, priority int64, dryRun bool) error {
	in := os.Stdin
	if from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}

	names, err := gather.ReadNames(in)
	if err != nil {
		return err
	}
	domains, err := gather.Names(names, tlds)
	if err != nil {
		return &usageError{err.Error()}
	}

	fmt.Fprintf(os.Stderr, "%s names: %s candidates\n", commas(int64(len(names))), commas(int64(len(domains))))
	if dryRun {
		return nil
	}

	db, err := data.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	inserted, err := gather.SeedDomains(ctx, db, domains, priority)
	if err != nil {
		return fmt.Errorf("seeded %s rows before stopping: %w", commas(inserted), err)
	}

	fmt.Fprintf(os.Stderr, "seeded %s new rows (%s already present)\n",
		commas(inserted), commas(int64(len(domains))-inserted))
	return nil
}

// confirmThreshold is where a run stops being cheap.
const confirmThreshold = 1_000_000

// confirm asks on stderr and reads a line from stdin. EOF counts as no.
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

// diskEstimate is the database size for n rows, at a measured 177 bytes/row.
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

// registryEstimate is how long one registry takes to check its TLDs.
type registryEstimate struct {
	origin string
	tlds   []string
	rate   float64
	took   time.Duration
}

// checkEstimates groups tlds by registry, in first-seen order, and times each
// at its measured rate, since .com and .net share Verisign's budget.
func checkEstimates(perTLD int64, tlds []string, originOf func(tld string) string) []registryEstimate {
	var out []registryEstimate
	index := make(map[string]int)
	for _, tld := range tlds {
		origin := originOf(tld)
		i, ok := index[origin]
		if !ok {
			i = len(out)
			index[origin] = i
			out = append(out, registryEstimate{
				origin: origin,
				rate:   float64(time.Second) / float64(sweep.Budget(origin).Every),
			})
		}
		out[i].tlds = append(out[i].tlds, tld)
	}

	for i := range out {
		rows := float64(perTLD * int64(len(out[i].tlds)))
		out[i].took = time.Duration(rows / out[i].rate * float64(time.Second)).Round(time.Second)
	}
	return out
}

// printCheckEstimates reports how long checking takes per registry. Registries
// run in parallel, so the slowest sets the total.
func printCheckEstimates(ctx context.Context, perTLD int64, tlds []string) {
	ests := checkEstimates(perTLD, tlds, bootstrapOrigins(ctx))

	var longest time.Duration
	for _, e := range ests {
		longest = max(longest, e.took)
	}
	fmt.Fprintf(os.Stderr, "  checking: about %v, registries in parallel\n", longest)

	for _, e := range ests {
		origin := e.origin
		if origin == "" {
			origin = "unknown registry"
		}
		rate := fmt.Sprintf("%g/s", e.rate)
		fmt.Fprintf(os.Stderr, "    %-32s %-12s %-6s about %v\n", origin, strings.Join(e.tlds, ","), rate, e.took)
	}
}

// bootstrapOrigins maps a TLD to its registry from the cached IANA bootstrap,
// without querying the registry. Empty when it can't tell.
func bootstrapOrigins(ctx context.Context) func(tld string) string {
	checker, err := rdapchecker.New(userAgent(platform.Version("nomex")))
	if err != nil {
		return func(string) string {
			return ""
		}
	}

	return func(tld string) string {
		// any label will do, only the suffix decides
		origin, err := checker.Origin(ctx, "nomex."+tld)
		if err != nil {
			return ""
		}
		return origin
	}
}

func estimate(rows, perSecond int64) time.Duration {
	if perSecond <= 0 {
		return 0
	}
	return (time.Duration(rows/perSecond) * time.Second).Round(time.Second)
}

// commas formats n with thousands separators.
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
