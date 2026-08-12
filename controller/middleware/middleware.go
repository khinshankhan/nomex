package middleware

import "net/http"

// Middleware wraps a handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain composes mw into a single Middleware, applied so that the first listed
// runs outermost -- matching the order they are written at the call site.
//
// Returning a Middleware rather than an http.Handler is what lets a chain be
// reused as a unit, so a stack can be named once and extended per route group:
//
//	base := Chain(Recover, Logging)
//	protected := Chain(base, RequireAuth)
//
// The wrapping happens once, when the returned Middleware is applied. Doing it
// inside the request instead costs an allocation per middleware per request,
// for a result that is identical every time.
func Chain(mw ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			next = mw[i](next)
		}
		return next
	}
}
