// Package secret provides the [String] type — a wrapper for sensitive
// values that refuses to render through fmt verbs, structured loggers,
// or JSON/text marshallers.
//
// # When to use
//
// Anywhere a credential, API key, signing key, OAuth client secret, or
// other sensitive value travels through application code:
//
//	type Config struct {
//	    DatabasePassword *secret.String
//	    OAuthClientSecret *secret.String
//	}
//
// // safe — slog emits "<redacted>" via String.LogValue
//
//	logger.Info("config loaded", "config", cfg)
//
// // safe — json.Marshal calls String.MarshalJSON
//
//	json.NewEncoder(w).Encode(cfg)
//
// // explicit — code review can grep "Reveal" to audit access points
//
//	conn := sql.Open("postgres", dsnFor(cfg.DatabasePassword.RevealString()))
//
// # When NOT to use
//
//   - For values that need to be embedded in URLs, headers, or other
//     transport bytes — call Reveal at the boundary and let the
//     downstream library handle the value.
//   - As a substitute for proper secret stores (Vault, AWS Secrets
//     Manager, kube Secrets) — String only protects the in-process path.
//
// # Lifecycle
//
// Call [String.Zero] during graceful shutdown to overwrite the underlying
// buffer with zero bytes. Zero is not an [io.Closer] — it wipes the
// in-memory copy and leaves the value safe to reuse. The type is
// otherwise GC-managed; there is no requirement to Zero every secret
// you create.
//
// # Lifetime-bounded reveals (Use)
//
// For cryptographic hot paths that need plaintext only for the duration
// of one HMAC / AEAD operation, prefer [String.Use] over
// [String.Reveal]. Use hands the closure a freshly-allocated copy of
// the bytes and overwrites that copy with zeroes when the closure
// returns — bounding the lifetime of the in-heap plaintext to the call
// site that needs it. This is the recommended pattern for long-lived
// HMAC keys stored inside middleware and audit components:
//
//	var sum []byte
//	key.Use(func(k []byte) {
//	    mac := hmac.New(sha256.New, k)
//	    mac.Write(msg)
//	    sum = mac.Sum(nil)
//	})
package secret
