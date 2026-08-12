package middleware

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRecover(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	t.Run("panic becomes 500", func(t *testing.T) {
		h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		}))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("no panic passes through", func(t *testing.T) {
		h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("fine"))
		}))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "fine" {
			t.Errorf("got %d %q, want 200 \"fine\"", rec.Code, rec.Body.String())
		}
	})

	t.Run("status is not rewritten after a partial write", func(t *testing.T) {
		// Once 200 and part of a body are on the wire the status cannot be
		// taken back; the response is truncated instead.
		h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("partial"))
			panic("boom")
		}))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		if rec.Code != 200 {
			t.Errorf("status = %d, want 200 (already sent)", rec.Code)
		}
		if got := rec.Body.String(); strings.Contains(got, "Internal Server Error") {
			t.Errorf("appended an error to an already-started body: %q", got)
		}
	})

	t.Run("ErrAbortHandler is re-panicked", func(t *testing.T) {
		defer func() {
			if err := recover(); err != http.ErrAbortHandler {
				t.Errorf("recovered %v, want ErrAbortHandler to propagate", err)
			}
		}()

		h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic(http.ErrAbortHandler)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	})
}
