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
// A bare struct{ http.ResponseWriter } drops the optional interfaces the
// server's writer implements, breaking streaming and upgrades at runtime.
// Unwrap matters most: http.ResponseController walks it, and without it every
// SetWriteDeadline returns "feature not supported".
//
// Tradeoff: a downstream w.(http.Hijacker) now always succeeds and fails at
// call time with ErrNotSupported instead. Prefer http.ResponseController.
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
