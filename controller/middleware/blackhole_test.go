package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testMux() *http.ServeMux {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /api/healthz", ok)
	// A real route whose path merely starts with the guarded prefix.
	mux.HandleFunc("GET /apidocs", ok)
	return mux
}

func TestBlackHole(t *testing.T) {
	h := BlackHole("/api", "/images")(testMux())

	const sector = "ERROR 404: DATA NOT FOUND IN THIS SECTOR."

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantAllow  string
	}{
		{
			name: "registered route is untouched", method: "GET", path: "/api/healthz",
			wantStatus: 200, wantBody: "ok",
		},
		{
			// The reason this middleware wraps the response instead of
			// registering a catchall: a catchall would answer 404 here.
			name: "method mismatch still gets 405", method: "POST", path: "/api/healthz",
			wantStatus: 405, wantAllow: "GET, HEAD",
		},
		{
			name: "unknown path under prefix", method: "GET", path: "/api/nope",
			wantStatus: 404, wantBody: sector,
		},
		{
			name: "prefix root with slash", method: "GET", path: "/api/",
			wantStatus: 404, wantBody: sector,
		},
		{
			name: "bare prefix", method: "GET", path: "/api",
			wantStatus: 404, wantBody: sector,
		},
		{
			name: "second prefix", method: "GET", path: "/images/x.png",
			wantStatus: 404, wantBody: sector,
		},
		{
			name: "route sharing the prefix string still resolves", method: "GET", path: "/apidocs",
			wantStatus: 200, wantBody: "ok",
		},
		{
			name: "prefix is a path segment, not a substring", method: "GET", path: "/apifoo",
			wantStatus: 404, wantBody: "404 page not found",
		},
		{
			name: "outside every prefix", method: "GET", path: "/other",
			wantStatus: 404, wantBody: "404 page not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); tt.wantBody != "" && got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
		})
	}
}

// A handler's own 404 is its answer, not an unrouted path, so it must survive.
func TestBlackHoleKeepsHandler404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/domains/{domain}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no such domain"}`, http.StatusNotFound)
	})

	rec := httptest.NewRecorder()
	BlackHole("/api")(mux).ServeHTTP(rec, httptest.NewRequest("GET", "/api/domains/nope.test", nil))

	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"no such domain"}` {
		t.Errorf("handler's own 404 was replaced: body = %q", got)
	}
}
