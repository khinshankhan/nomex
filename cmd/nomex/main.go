package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/khinshankhan/nomex/controller"
	"github.com/khinshankhan/nomex/platform"
	"golang.org/x/sync/errgroup"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// defaultDBPath matches the Makefile's DB variable, so the CLI and `make
// migrate` operate on the same database without configuration.
const defaultDBPath = "./var/nomex.db"

func main() {
	// One context every subsystem watches, cancelled on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
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
	case "version":
		fmt.Fprint(
			os.Stderr,
			version.String(),
		)
		return nil
	case "ua":
		fmt.Fprint(
			os.Stderr,
			platform.
				UserAgent(
					version,
					platform.UserAgentOptions{},
				),
		)
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
