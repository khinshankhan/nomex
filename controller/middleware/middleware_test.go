package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tag records entry and exit around next, so the assertion covers nesting
// order rather than merely which middleware ran.
func tag(log *[]string, name string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*log = append(*log, "->"+name)
			next.ServeHTTP(w, r)
			*log = append(*log, "<-"+name)
		})
	}
}

func TestChainOrder(t *testing.T) {
	var log []string

	h := Chain(tag(&log, "a"), tag(&log, "b"), tag(&log, "c"))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log = append(log, "handler")
		}),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	// First listed is outermost: it is entered first and exited last.
	want := "->a ->b ->c handler <-c <-b <-a"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// The reason Chain returns a Middleware: a chain has to be reusable as a unit.
func TestChainComposes(t *testing.T) {
	var log []string

	base := Chain(tag(&log, "recover"), tag(&log, "logging"))
	protected := Chain(base, tag(&log, "auth"))

	h := protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log = append(log, "handler")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	want := "->recover ->logging ->auth handler <-auth <-logging <-recover"
	if got := strings.Join(log, " "); got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestChainEmpty(t *testing.T) {
	h := Chain()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("reached"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Body.String() != "reached" {
		t.Errorf("empty chain did not pass through: %q", rec.Body.String())
	}
}

// Wrapping must happen when the chain is applied, not per request, so the
// handler a request traverses is built exactly once.
func TestChainWrapsOnce(t *testing.T) {
	wraps := 0
	counting := func(next http.Handler) http.Handler {
		wraps++
		return next
	}

	h := Chain(counting, counting)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if wraps != 2 {
		t.Fatalf("wraps after apply = %d, want 2", wraps)
	}

	for range 5 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}
	if wraps != 2 {
		t.Errorf("wraps after 5 requests = %d, want 2 (chain rebuilt per request)", wraps)
	}
}
