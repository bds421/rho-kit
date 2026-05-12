//go:build !authtest

package interceptor

import "context"

// WithTrustedS2S panics in non-authtest builds. The function exists
// so a misuse fails loudly at runtime rather than letting an
// accidental import compile and silently bypass RBAC. Production code
// MUST NOT call this; the marker is meant to be set only by
// MTLSAuthUnary/MTLSAuthStream's mTLS branch after a verified client
// certificate.
//
// To use this helper in tests, build with `-tags authtest`.
func WithTrustedS2S(_ context.Context) context.Context {
	panic("interceptor.WithTrustedS2S is only available under build tag authtest")
}
