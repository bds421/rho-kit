//go:build authtest

package interceptor

import "context"

// WithTrustedS2S returns ctx marked as a trusted service-to-service
// caller.
//
// Available only under the `authtest` build tag — production code
// MUST rely on MTLSAuthUnary/MTLSAuthStream's mTLS branch to set the
// marker after a verified client certificate. The build tag exists
// so a typo during a refactor cannot promote a test-only bypass into
// a binary shipping to production.
func WithTrustedS2S(ctx context.Context) context.Context {
	return trustedS2SKey.Set(ctx, grpcTrustedS2SMarker{})
}
