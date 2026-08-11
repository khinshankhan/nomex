package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/khinshankhan/nomex/version"
	"golang.org/x/sync/errgroup"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// NotifyContext replaces the usual signal-channel plumbing: one context
	// that every subsystem watches, cancelled on the first SIGINT or SIGTERM.
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

	// TODO: probably parse flags
	switch cmd := args[0]; cmd {
	case "serve":
		return runServe(ctx)
	case "version":
		fmt.Fprint(
			os.Stderr,
			version.
				Version("nomex").
				String(),
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
	fmt.Fprint(os.Stderr, `nomex -- mini experiment to index domain names.

usage:
  nomex serve           run the HTTP API
  nomex help            this message
  nomex version         display build info

`)
}

// runServe runs the HTTP server + background tasks.
func runServe(ctx context.Context) error {
	// WithContext gives every subsystem a context that is cancelled when any
	// one of them returns an error, so a dead background task takes the server
	// down with it rather than leaving a half-running process.
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return runHTTPServer(ctx)
	})

	return g.Wait()
}
