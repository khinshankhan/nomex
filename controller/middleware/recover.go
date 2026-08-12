package middleware

import (
	"io"
	"log"
	"net/http"
	"runtime/debug"
)

// Recover recovers from panics and logs them.
//
// The status is only sent when nothing has been written yet: once a handler has
// flushed a 200 and part of a body, the status line is already on the wire and
// a second WriteHeader would be ignored with a "superfluous response.WriteHeader"
// warning. Truncating the response is the honest failure there.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &recorder{wrapper: wrapper{ResponseWriter: w}}

		defer func() {
			if err := recover(); err != nil {
				// ErrAbortHandler is the documented way for a handler to give
				// up without noise, so it is re-panicked rather than logged.
				if err == http.ErrAbortHandler {
					panic(err)
				}

				log.Printf("[Recover] panic serving %s %q: %v\n%s",
					r.Method, r.URL.Path, err, debug.Stack())

				if !rw.wroteHeader {
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(rw, r)
	})
}

// recorder tracks whether the status line has been sent.
type recorder struct {
	wrapper
	wroteHeader bool
}

func (w *recorder) WriteHeader(code int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// ReadFrom and Flush both put bytes on the wire without going through Write,
// so each has to mark the response as started or a later panic would try to
// replace a status that has already been sent.
func (w *recorder) ReadFrom(r io.Reader) (int64, error) {
	w.wroteHeader = true
	return w.wrapper.ReadFrom(r)
}

func (w *recorder) Flush() {
	w.wroteHeader = true
	w.wrapper.Flush()
}
