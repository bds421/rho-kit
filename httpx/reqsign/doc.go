// Package reqsign provides HTTP request signing and verification for
// inter-service authentication. It delegates all HMAC-SHA256 operations to
// the [github.com/bds421/rho-kit/crypto/v2/signing] package, adding an
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
// Four headers carry the signature:
//   - X-Signature: the HMAC-SHA256 signature (sha256=hex)
//   - X-Signature-Timestamp: Unix timestamp (decimal string)
//   - X-Signature-KeyID: which key was used (supports rotation)
//   - X-Signature-Nonce: per-request random token used for replay
//     protection (audit FR-025); see [WithNonceStore]
//
// # Replay Protection
//
// Verification requires a [NonceStore] (see [WithNonceStore]) so that
// a captured signed request cannot be replayed inside the maxAge
// window — the prior behaviour. The nonce store records every accepted
// nonce until the window closes; duplicates are rejected with
// [ErrReplay]. Multi-instance deployments must use a shared backend
// (Redis, etc.) so a replay caught by one replica is rejected by all.
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
