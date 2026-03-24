// Package reqsign provides HTTP request signing and verification for
// inter-service authentication. It delegates all HMAC-SHA256 operations to
// the [github.com/bds421/rho-kit/crypto/signing] package, adding an
// HTTP-aware layer on top.
//
// # Canonical String
//
// Each request is reduced to a canonical byte sequence before signing:
//
//	METHOD + "\n" + PATH + "\n" + hex(sha256(body))
//
// For requests without a body the SHA-256 of empty bytes is used. This
// ensures that the method, path, and body are all covered by the signature.
//
// # Headers
//
// Three headers carry the signature:
//   - X-Signature: the HMAC-SHA256 signature (sha256=hex)
//   - X-Signature-Timestamp: Unix timestamp (decimal string)
//   - X-Signature-KeyID: which key was used (supports rotation)
//
// # Key Rotation
//
// [StaticKeyStore] supports multiple keys: sign with the current key, verify
// against any known key. This allows zero-downtime key rotation by adding
// the new key, switching the current ID, and eventually removing the old key.
//
// # Transport & Middleware
//
// [SigningTransport] wraps an [http.RoundTripper] to sign all outbound
// requests automatically. [RequireSignedRequest] returns middleware that
// verifies inbound requests and returns 401 on failure.
package reqsign
