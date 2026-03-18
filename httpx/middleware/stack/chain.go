package stack

import "net/http"

// Chain is an explicit, composable middleware chain. Unlike [Default],
// which applies a fixed set of middleware, Chain gives full control over
// ordering and composition.
//
// Design note: Chain and Default intentionally share the middleware/stack
// package. Chain is a zero-dependency utility; Default is an opinionated
// composition of kit middleware. Co-locating them keeps the middleware
// surface small. If you only need Chain, its import cost is negligible
// since Default's dependencies are not initialized until Default is called.
//
//	chain := stack.NewChain(
//	    metrics.Middleware,
//	    requestid.WithRequestID,
//	    logging.Logger(logger, nil),
//	)
//	handler := chain.Then(mux)
type Chain struct {
	middlewares []func(http.Handler) http.Handler
}

// NewChain creates a middleware chain. Middlewares are applied in the order
// given: the first middleware wraps the outermost layer.
func NewChain(middlewares ...func(http.Handler) http.Handler) Chain {
	return Chain{middlewares: middlewares}
}

// Append returns a new Chain with additional middleware appended (applied
// after the existing middleware, closer to the handler).
func (c Chain) Append(middlewares ...func(http.Handler) http.Handler) Chain {
	combined := make([]func(http.Handler) http.Handler, 0, len(c.middlewares)+len(middlewares))
	combined = append(combined, c.middlewares...)
	combined = append(combined, middlewares...)
	return Chain{middlewares: combined}
}

// Then applies the middleware chain to the given handler and returns the
// composed handler. Middleware is applied in reverse order so the first
// middleware in the chain is the outermost wrapper.
//
// Panics if handler is nil to prevent silently falling through to
// http.DefaultServeMux, which masks initialization bugs.
func (c Chain) Then(handler http.Handler) http.Handler {
	if handler == nil {
		panic("middleware: Chain.Then() requires a non-nil handler")
	}
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		handler = c.middlewares[i](handler)
	}
	return handler
}

// ThenFunc is a convenience that wraps an http.HandlerFunc before applying
// the chain. Since http.HandlerFunc implements http.Handler, callers can
// also use Then(fn) directly.
func (c Chain) ThenFunc(fn http.HandlerFunc) http.Handler {
	return c.Then(fn)
}

// Len returns the number of middleware in the chain.
func (c Chain) Len() int {
	return len(c.middlewares)
}
