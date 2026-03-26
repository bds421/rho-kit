// Package reqsign provides HTTP request signing and verification for
// inter-service authentication. It delegates all HMAC-SHA256 operations to
// the [github.com/bds421/rho-kit/crypto/signing] package, adding an
// HTTP-aware layer on top.
//
// # Canonical String
//
// Each request is reduced to a canonical byte sequence before signing:
//
//	METHOD + "\n" + REQUEST_URI + "\n" + hex(sha256(body))
//
// REQUEST_URI is the full request URI including the path and query string
// (e.g. "/api/deploy?env=prod"). This ensures that the method, path, query
// parameters, and body are all covered by the signature.
//
// WARNING: The canonical string does not include the Host header. If signing
// keys are shared across multiple services, a signature for one service's
// endpoint can be replayed against another service at the same path. Use
// unique per-service-pair keys to prevent cross-service replay.
//
// The Content-Type header is also not included in the canonical string. An
// intercepted request could have its Content-Type changed without invalidating
// the signature.
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
// [signing.StaticKeyStore] supports multiple keys: sign with the current key, verify
// against any known key. This allows zero-downtime key rotation by adding
// the new key, switching the current ID, and eventually removing the old key.
//
// # Transport & Middleware
//
// [SigningTransport] wraps an [http.RoundTripper] to sign all outbound
// requests automatically. [RequireSignedRequest] returns middleware that
// verifies inbound requests and returns 401 on failure.
package reqsign
