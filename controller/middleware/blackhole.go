package middleware

import (
	"log"
	"net/http"
	"strings"
)

// BlackHole replaces the mux's default 404 body under the given path prefixes,
// so an unknown API path answers in the API's own voice rather than with Go's
// plain "404 page not found".
//
// It deliberately does not register a catchall route. A pattern like
// "/api/{path...}" matches any method, so it wins against a method mismatch and
// the mux never reaches its own 405 handling -- a client that used the wrong
// verb gets told the endpoint does not exist. Wrapping the response instead
// leaves routing untouched: 405s and their Allow header survive, and only a
// genuine 404 is rewritten.
//
// A prefix covers itself and everything beneath it: "/api" matches "/api",
// "/api/" and "/api/x", but not "/apifoo".
func BlackHole(prefixes ...string) Middleware {
	cleaned := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		if p = strings.TrimSuffix(p, "/"); p != "" {
			cleaned = append(cleaned, p)
		}
	}

	return func(next http.Handler) http.Handler {
		// Only a ServeMux can be asked whether a route exists ahead of time.
		// Without one there is nothing to distinguish an unrouted path from a
		// handler's own 404, so the wrapper stays out of the way entirely.
		mux, ok := next.(*http.ServeMux)
		if !ok {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !covered(cleaned, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Handler resolves the route without running it. An empty pattern
			// means nothing is registered -- but it also covers a method
			// mismatch, so dispatch still has to decide between 404 and 405.
			if _, pattern := mux.Handler(r); pattern != "" {
				next.ServeHTTP(w, r)
				return
			}

			bw := &blackHoleWriter{wrapper: wrapper{ResponseWriter: w}}
			next.ServeHTTP(bw, r)

			if bw.swallowed {
				// Quoted: a raw path can contain spaces or newlines, which
				// would otherwise forge extra fields or extra lines in the log.
				log.Printf("[BlackHole] no route for %s %q", r.Method, r.URL.Path)
				http.Error(w, "ERROR 404: DATA NOT FOUND IN THIS SECTOR.", http.StatusNotFound)
			}
		})
	}
}

func covered(prefixes []string, path string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// blackHoleWriter swallows the mux's own 404 so a replacement can be written in
// its place. Anything else passes through untouched.
type blackHoleWriter struct {
	wrapper
	swallowed   bool
	wroteHeader bool
}

func (w *blackHoleWriter) WriteHeader(code int) {
	w.wroteHeader = true

	// A 405 carries Allow; a 404 never does. That header is what separates
	// "nothing is registered here" from "wrong method for a route that exists",
	// and only the former belongs to the black hole.
	if code == http.StatusNotFound && w.Header().Get("Allow") == "" {
		w.swallowed = true
		return
	}

	w.ResponseWriter.WriteHeader(code)
}

func (w *blackHoleWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.swallowed {
		// Discard "404 page not found" while reporting success, so the handler
		// that wrote it does not see a short write.
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
