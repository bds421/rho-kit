// Package id consolidates UUID v7 generation for the kit. Before this
// package existed, every data/, infra/, httpx/ package that needed a
// message, task, job, or envelope ID called uuid.NewV7() directly,
// re-implemented the same `if err != nil` branch around it (in code that
// can only fail when crypto/rand itself is broken), and re-invented its
// own test-mode swap pattern. core/id collapses all of that into a
// single primitive:
//
//   - [New] returns a fresh UUID v7 as a string. The (effectively
//     impossible) crypto/rand failure case panics rather than
//     propagating an error — once the OS RNG is gone, a service has
//     nothing useful left to do. Callers that previously returned
//     "generate ID: %w" errors drop those branches.
//   - [NewBytes] is the same generator returning the raw [16]byte form
//     so callers that need a uuid-typed value (Postgres uuid columns,
//     binary wire encodings) do not need to round-trip through a
//     string.
//   - [Parse] turns a string back into a 16-byte UUID and returns a
//     kit validation error on malformed input.
//   - [Generator] is the package-level swap point. Tests assign a
//     deterministic function to it (and restore the previous value on
//     cleanup) so log lines, fixtures, and golden files stay stable
//     across runs.
//
// # When to use
//
// Use [New] for any kit-level identifier whose lifetime is the message
// itself: queue messages, stream entries, outbox rows, task IDs,
// approval-flow IDs, audit-log entries, anything the kit publishes
// under a stable handle. UUID v7's time-ordered prefix is friendly to
// B-tree indexes and human eyes scanning logs in chronological order.
//
// # When NOT to use
//
// - Request/correlation/trace IDs are produced by the
//   observability and httpx middleware (correlationid, requestid,
//   tracing) and follow their respective wire formats — do not
//   substitute id.New() there.
// - Opaque secret tokens (API keys, session tokens, refresh tokens,
//   share-URL nonces) need cryptographic unguessability that UUID v7
//   does not provide; use [core/v2/randstr] instead.
// - User-facing short codes (vouchers, OTPs) likewise belong in
//   [core/v2/randstr] with the no-ambiguous charset.
package id
