package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/controller"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/platform/logx"
	"golang.org/x/sync/errgroup"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// One context every subsystem watches, cancelled on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		// A usage error is guidance for a person and often spans lines, which
		// a structured log field renders as escaped \n. Print those plainly
		// and log only genuine failures.
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
	case "list":
		return runList(ctx, args[1:])
	case "status":
		return runStatus(ctx, args[1:])
	case "version":
		// stdout, with a newline: this is output a caller pipes, not a log.
		fmt.Fprintln(os.Stdout, version.String())
		return nil
	case "ua":
		// The same string the sweeper sends, so this reports what registries
		// actually see rather than a bare default.
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
  nomex list            report what the database knows
  nomex status          how far the sweep has got
  nomex help            this message
  nomex version         display build info

`)
}

// runServe runs the HTTP server + background tasks.
func runServe(ctx context.Context) error {
	// Cancels every subsystem when any one errors, so a dead background task
	// takes the server with it instead of leaving a half-running process.
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return controller.RunHTTPServer(ctx)
	})

	return g.Wait()
}

// userAgent is what registries see. One definition, so `nomex ua` reports the
// string actually sent rather than a plausible-looking approximation.
//
// The contact comes from the environment and has no default. Shipping one would
// put a stranger's traffic under someone else's name: an operator receiving
// abuse would reach the author of this repository rather than whoever is
// actually querying them.
func userAgent(v *platform.VersionInfo) string {
	return platform.UserAgent(v, platform.UserAgentOptions{
		Name:  "nomex",
		URL:   config.Contact(),
		Extra: map[string]string{"service": "rdap"},
	})
}

// usageError is a message for a person rather than a failure to log.
type usageError struct{ msg string }

func (e *usageError) Error() string {
	return e.msg
}

// requireContact fails before any query is sent when nobody can be reached.
func requireContact() error {
	if config.Contact() != "" {
		return nil
	}
	return &usageError{fmt.Sprintf(`%s is not set

Registries see this and use it to reach whoever is querying them. It has to
identify you rather than this repository, because a shared default would
attribute your traffic to someone else.

  export %s=https://github.com/you
  export %s=mailto:you@example.com`, config.EnvContact, config.EnvContact, config.EnvContact)}
}
