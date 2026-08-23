package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/khinshankhan/nomex/controller/handler"
	"github.com/khinshankhan/nomex/controller/middleware"
)

const (
	// defaultAddr is where serve listens.
	defaultAddr = 8080

	// shutdownGrace bounds the drain. Unbounded, one hung request holds the
	// process open forever.
	shutdownGrace = 10 * time.Second
)

// RunHTTPServer runs the HTTP API until the context is cancelled.
func RunHTTPServer(ctx context.Context) error {
	// TODO: allow flags to customize
	addr := fmt.Sprintf(":%d", defaultAddr)
	return newHTTPServer(addr).run(ctx)
}

// httpServer owns the listener's lifecycle.
type httpServer struct {
	srv *http.Server
}

func newHTTPServer(addr string) *httpServer {
	// First listed runs outermost, so Recover sees panics from BlackHole too.
	// TODO: add CORS here
	handler := middleware.Chain(
		middleware.Recover,
		middleware.BlackHole("/api"),
	)(routes())

	return &httpServer{
		srv: &http.Server{
			Addr:    addr,
			Handler: handler,
		},
	}
}

// run serves until ctx is cancelled, then shuts down gracefully.
func (s *httpServer) run(ctx context.Context) error {
	listening := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", s.srv.Addr)
		listening <- s.srv.ListenAndServe()
	}()

	// ListenAndServe blocks, so a failure arrives on a channel while
	// cancellation arrives on the context. Returning the listener error rather
	// than logging it matters: a swallowed "address already in use" would leave
	// the process up with nothing serving.
	select {
	case err := <-listening:
		// Only reachable when the server stopped on its own.
		return err

	case <-ctx.Done():
		log.Println("shutting down")

		// Fresh context: ctx is already cancelled and would abort the drain.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()

		if err := s.srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutting down: %w", err)
		}

		// ErrServerClosed means Shutdown was called: the success case.
		if err := <-listening; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Health endpoint
	mux.HandleFunc("GET /api/healthz", handler.HealthHandler)

	return mux
}
