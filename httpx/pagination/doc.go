// Package pagination provides cursor-based and offset-based pagination
// helpers.
//
// # When to use which
//
// Use cursor pagination ([CursorParams], [BuildResult], [HandleCursorList])
// for hot, high-cardinality lists where the underlying ordering is stable
// and clients consume sequentially. Cursors avoid the O(offset) cost of
// classical offset pagination and are stable under concurrent writes.
//
// Use offset pagination ([ParseOffset], [WriteLinkHeader]) for admin and UI
// tables where the user expects "page 5 of 12" semantics, total counts, and
// jump-to-last. RFC 5988 Link headers (first/prev/next/last) plug straight
// into every front-end paginator and kubectl-style CLI.
//
// # Cursor signer memory hygiene
//
// [CursorSigner] holds its HMAC secret in [secret.String] and reveals
// it inside [secret.String.Use] closures so plaintext key bytes have
// per-call lifetime. Call [CursorSigner.Close] at shutdown to zero
// the wrapped secret; subsequent Encode calls return "" and Decode
// returns [ErrCursorInvalid].
package pagination
