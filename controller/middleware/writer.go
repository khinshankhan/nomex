package middleware

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
)

// wrapper is the embedded base for every ResponseWriter this package wraps.
//
// A bare `struct{ http.ResponseWriter }` silently drops the optional interfaces
// the server's own writer implements -- Flusher, Hijacker, ReaderFrom, Pusher.
// Type assertions downstream then fail, and streaming or connection upgrades
// break in a way that only shows up at runtime, on the one endpoint that needs
// them.
//
// Unwrap is the important one: http.ResponseController walks it to reach the
// underlying writer, so without it every SetWriteDeadline/SetReadDeadline call
// returns "feature not supported".
//
// Forwarding unconditionally is safe because each method checks the underlying
// writer for the matching interface and reports http.ErrNotSupported when it is
// absent -- the same error the caller would have seen from a failed assertion.
// What is lost is the assertion itself: a downstream `w.(http.Hijacker)` now
// always succeeds. Handlers should prefer http.ResponseController, which
// reports capability through the returned error.
type wrapper struct {
	http.ResponseWriter
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *wrapper) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *wrapper) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *wrapper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack: %w", http.ErrNotSupported)
}

func (w *wrapper) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return fmt.Errorf("push: %w", http.ErrNotSupported)
}

// ReadFrom keeps io.Copy on the fast path (sendfile, where available) instead
// of falling back to a buffered loop through Write.
func (w *wrapper) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
}
