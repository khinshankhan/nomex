package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/controller"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/platform/logx"
	"github.com/khinshankhan/nomex/sweep"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		// usage errors can span lines, which a log field would escape
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(os.Stderr, ue.Error())
		} else {
			logx.Default().Error("exiting", fields.Error(err))
		}
		os.Exit(1)
	}
}

// run dispatches to a subcommand.
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no subcommand given")
	}

	version := platform.Version("nomex")

	// TODO: probably parse flags
	switch cmd := args[0]; cmd {
	case "serve":
		return runServe(ctx)
	case "gather":
		return runGather(ctx, args[1:])
	case "sweep":
		return runSweep(ctx, args[1:])
	case "check":
		return runCheck(ctx, args[1:])
	case "list":
		return runList(ctx, args[1:])
	case "status":
		return runStatus(ctx, args[1:])
	case "version":
		fmt.Fprintln(os.Stdout, version.String())
		return nil
	case "ua":
		fmt.Fprintln(os.Stdout, userAgent(version))
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `nomex -- is this domain available, and how much do we trust that answer.

usage:
  nomex serve           run the HTTP API
  nomex gather          seed candidate domains
  nomex sweep           check domains whose answers have gone stale
  nomex check           check specific names now
  nomex list            report what the database knows
  nomex status          how far the sweep has got
  nomex help            this message
  nomex version         display build info

`)
}

// runServe runs the HTTP server.
func runServe(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return controller.RunHTTPServer(ctx)
	})

	return g.Wait()
}

// userAgent is what registries see, shared with `nomex ua`.
func userAgent(v *platform.VersionInfo) string {
	return platform.UserAgent(v, platform.UserAgentOptions{
		Name:  "nomex",
		URL:   config.Contact(),
		Extra: map[string]string{"service": "rdap"},
	})
}

// limitFlags registers -rate and -burst and returns their per-registry budget
// once parsed. A flag that isn't given stays zero, so the sweep uses the
// registry's measured budget.
func limitFlags(flags *flag.FlagSet) func() (sweep.Limits, error) {
	rate := flags.Float64("rate", 0, fmt.Sprintf("queries per second, per registry (default measured per registry, else %g)",
		float64(time.Second)/float64(sweep.DefaultLimits.Every)))
	burst := flags.Int("burst", 0, fmt.Sprintf("queries allowed back-to-back per registry (default measured per registry, else %d)",
		sweep.DefaultLimits.Burst))

	return func() (sweep.Limits, error) {
		set := make(map[string]bool)
		flags.Visit(func(f *flag.Flag) {
			set[f.Name] = true
		})

		l := sweep.Limits{Burst: *burst}
		if set["burst"] && *burst < 1 {
			return sweep.Limits{}, &usageError{fmt.Sprintf("-burst %d: must be at least 1", *burst)}
		}
		if set["rate"] {
			// NOTE: !(rate > 0) also catches NaN
			if !(*rate > 0) || math.IsInf(*rate, 0) {
				return sweep.Limits{}, &usageError{fmt.Sprintf("-rate %g: must be a positive number of queries per second", *rate)}
			}
			l.Every = time.Duration(float64(time.Second) / *rate)
		}
		return l, nil
	}
}

// atLeastOne fails if any of the named int flags is below one, which would
// otherwise quietly fall back to its default.
func atLeastOne(flags *flag.FlagSet, names ...string) error {
	for _, name := range names {
		if n := flags.Lookup(name).Value.(flag.Getter).Get().(int); n < 1 {
			return &usageError{fmt.Sprintf("-%s %d: must be at least 1", name, n)}
		}
	}
	return nil
}

// describeLimits says what -rate and -burst came to, for the startup line.
func describeLimits(l sweep.Limits) string {
	rate := "measured rate per registry"
	if l.Every > 0 {
		rate = fmt.Sprintf("%g/s per registry", float64(time.Second)/float64(l.Every))
	}
	if l.Burst > 0 {
		rate += fmt.Sprintf(" (burst %d)", l.Burst)
	}
	return rate
}

// usageError is a message for a person, not a failure to log.
type usageError struct{ msg string }

func (e *usageError) Error() string {
	return e.msg
}

// requireContact fails before anything is queried if no contact is set.
func requireContact() error {
	if config.Contact() != "" {
		return nil
	}
	return &usageError{fmt.Sprintf(`%s is not set

Registries see this and use it to reach whoever is querying them, so it
should identify you, not this repository.

  export %s=https://github.com/you
  export %s=mailto:you@example.com`, config.EnvContact, config.EnvContact, config.EnvContact)}
}
