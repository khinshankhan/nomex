package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/khinshankhan/logstox/fields"
	"github.com/khinshankhan/nomex/controller/handler"
	"github.com/khinshankhan/nomex/controller/middleware"
	"github.com/khinshankhan/nomex/platform/logx"
)

const (
	defaultAddr = 8080

	// so a hung request can't hold shutdown forever
	shutdownGrace = 10 * time.Second
)

// RunHTTPServer runs the HTTP API until the context is cancelled.
func RunHTTPServer(ctx context.Context) error {
	// TODO: allow flags to customize
	addr := fmt.Sprintf(":%d", defaultAddr)
	return newHTTPServer(addr).run(ctx)
}

type httpServer struct {
	srv *http.Server
}

func newHTTPServer(addr string) *httpServer {
	// first listed is outermost
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
		logx.Default().Named("http").Info("listening", fields.String("addr", s.srv.Addr))
		listening <- s.srv.ListenAndServe()
	}()

	// NOTE: return the listener error, a swallowed "address already in use"
	// leaves the process up with nothing serving
	select {
	case err := <-listening:
		return err

	case <-ctx.Done():
		logx.Default().Named("http").Info("shutting down")

		// ctx is already cancelled
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()

		if err := s.srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutting down: %w", err)
		}

		if err := <-listening; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/healthz", handler.HealthHandler)

	return mux
}
