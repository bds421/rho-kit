package app

import (
	"net/http"

	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

// signedRequestSpec captures everything WithSignedRequests passes
// through to the middleware constructor. Stored on the Builder so
// the public-mux assembly can wrap inbound requests.
type signedRequestSpec struct {
	resolver signedrequest.KeyResolver
	store    signedrequest.NonceStore
	opts     []signedrequest.Option
}

// SignedRequestMiddleware returns the middleware factory for the
// configured WithSignedRequests options, or nil when no signed
// requests are configured. The kit-supplied router glue inserts it
// in front of the standard stack so unsigned requests get rejected
// before any handler-side work.
func (b *Builder) signedRequestMiddleware() func(http.Handler) http.Handler {
	if b.signedSpec == nil {
		return nil
	}
	return signedrequest.Middleware(b.signedSpec.resolver, b.signedSpec.store, b.signedSpec.opts...)
}
