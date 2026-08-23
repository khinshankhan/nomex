package middleware

import (
	"log"
	"net/http"
	"strings"
)

// BlackHole replaces the mux's default 404 body under the given path prefixes.
//
// Not a catchall route: "/api/{path...}" matches any method, so it beats a
// method mismatch and the mux never returns its 405. Wrapping the response
// leaves routing alone -- 405s and their Allow header survive.
//
// A prefix covers itself and everything under it: "/api" matches "/api",
// "/api/" and "/api/x", but not "/apifoo".
func BlackHole(prefixes ...string) Middleware {
	cleaned := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		if p = strings.TrimSuffix(p, "/"); p != "" {
			cleaned = append(cleaned, p)
		}
	}

	return func(next http.Handler) http.Handler {
		// Only a ServeMux can be asked whether a route exists before dispatch,
		// which is what separates an unrouted path from a handler's own 404.
		mux, ok := next.(*http.ServeMux)
		if !ok {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !covered(cleaned, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Empty pattern means nothing is registered -- but it also covers
			// a method mismatch, so dispatch decides between 404 and 405.
			if _, pattern := mux.Handler(r); pattern != "" {
				next.ServeHTTP(w, r)
				return
			}

			bw := &blackHoleWriter{wrapper: wrapper{ResponseWriter: w}}
			next.ServeHTTP(bw, r)

			if bw.swallowed {
				// %q: the path is caller-controlled and can contain newlines.
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

// blackHoleWriter swallows the mux's 404 so a replacement can be written.
type blackHoleWriter struct {
	wrapper
	swallowed   bool
	wroteHeader bool
}

func (w *blackHoleWriter) WriteHeader(code int) {
	w.wroteHeader = true

	// A 405 carries Allow; a 404 never does. Only the latter is ours.
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
		// Discard "404 page not found", reporting success to avoid a short write.
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
