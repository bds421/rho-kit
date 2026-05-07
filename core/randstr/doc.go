// Package randstr generates cryptographically random strings.
//
// All public functions are backed by [crypto/rand] and use rejection sampling
// against the charset length so the output is uniformly distributed. They are
// safe for tokens, OTPs, share-URL nonces, anti-forgery values, and any other
// secret that needs to be unguessable.
//
// Use [MustString] in startup paths and tests where a [crypto/rand] failure
// is genuinely fatal. Use [RuneSequence] when the caller wants to surface the
// error.
//
// The pre-defined charsets cover the common cases: alphanumeric (mixed,
// lower-only, upper-only), digits, and the no-ambiguous variant that excludes
// visually-confusable characters (0/O/I/l/1) for human-readable codes.
package randstr
