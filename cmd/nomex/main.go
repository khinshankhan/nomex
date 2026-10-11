package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/controller"
	"github.com/khinshankhan/nomex/platform"
	"github.com/khinshankhan/nomex/platform/config"
	"github.com/khinshankhan/nomex/platform/logx"
	"golang.org/x/sync/errgroup"
)

func main() {
	// one context for everything, cancelled on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		// usage errors are for a person and can span lines, which a log field
		// would escape, so print those plainly
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
		// stdout with a newline, this is output not a log
		fmt.Fprintln(os.Stdout, version.String())
		return nil
	case "ua":
		// the same string the sweep sends
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

// runServe runs the HTTP server + background tasks.
func runServe(ctx context.Context) error {
	// one subsystem failing takes the rest down with it
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return controller.RunHTTPServer(ctx)
	})

	return g.Wait()
}

// userAgent is what registries see, shared so `nomex ua` reports the real
// string. The contact comes from the environment with no default (see
// config.EnvContact).
func userAgent(v *platform.VersionInfo) string {
	return platform.UserAgent(v, platform.UserAgentOptions{
		Name:  "nomex",
		URL:   config.Contact(),
		Extra: map[string]string{"service": "rdap"},
	})
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
