// Package jwtutil provides JWKS-based JWT verification with automatic
// key rotation and caching, plus a [SigningProvider] for the issuance
// side. It wraps lestrrat-go/jwx for token parsing, signature
// verification, and signing.
//
// The verification surface ([Provider], [KeySet], [Verify]) was the
// kit's historical posture — services consumed tokens minted by
// external IdPs and the kit's mandate stopped at JWKS-backed
// verification. v2.0 keeps that posture by default and adds
// [SigningProvider] so services that need to issue short-lived
// service tokens, on-behalf-of tokens, or session JWTs can do so
// without rolling a parallel issuance path against the same jwx/v3
// dependency the verifier already pulls in. See [SigningProvider] for
// rotation, jti, and audience semantics.
//
// asvs: V2.1.5, V2.3.1, V3.2.1
package jwtutil
