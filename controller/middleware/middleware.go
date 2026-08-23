package middleware

import "net/http"

// Middleware wraps a handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain composes mw into one Middleware, first listed running outermost.
// Returning a Middleware rather than a Handler lets a stack be named and
// extended:
//
//	base := Chain(Recover, Logging)
//	protected := Chain(base, RequireAuth)
//
// Wrapping happens once, when applied -- doing it per request costs an
// allocation per middleware for an identical result.
func Chain(mw ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			next = mw[i](next)
		}
		return next
	}
}
