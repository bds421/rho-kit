// Package actionlog is the kit's append-only, signed record of
// agent-attributed actions.
//
// # Why this exists
//
// The kit already ships [observability/auditlog] for HTTP request audit:
// per-request visibility, "who hit which path with which status." That's a
// *transport*-level concern.
//
// actionlog is a different concern. It records the application-level fact
// that "agent X performed action Y at time T against tenant Z," with a
// freeform metadata bag the caller decides the shape of. Forensics teams,
// compliance reviewers, and incident responders read it; an HTTP log is
// the wrong shape for those readers because it doesn't know the
// application's verbs (e.g. "user.delete" vs "POST /v1/users/123") and
// doesn't carry tenant or actor as first-class fields.
//
// Two properties make this log audit-grade:
//
//  1. Append-only at the API: there is no Update, no Delete. Stores that
//     can support deletion (memory, GORM) intentionally do not expose it
//     here. Retention sweeps belong to a separate, explicitly-named tool.
//
//  2. HMAC-signed entries: every persisted entry carries a signature
//     computed over its canonical form. Reads verify and reject any entry
//     whose signature doesn't match. A DBA who manually rewrites a row
//     produces an unverifiable entry, and forensics will see it as
//     [ErrSignatureInvalid] rather than as a fact.
//
// # Canonicalisation rule
//
// The signed payload is the entry's fields joined by '\n' in this exact
// order:
//
//	id
//	tenant_id
//	actor (convention: "<kind>:<id>" via httpx/middleware/auth.FormatActorFromContext)
//	action
//	resource
//	outcome
//	reason
//	occurred_at (RFC3339Nano, UTC)
//	metadata (canonical JSON: keys sorted lexicographically; nested maps
//	          recursively sorted; no insignificant whitespace)
//
// The signature is HMAC-SHA256(secret, canonical) hex-encoded. Use
// [SignEntry] / [VerifyEntry] if you need to verify off-band — both are
// stateless free functions that take only a [SecretSource], so chain-
// inspection tools do not need to construct a Logger / Store pair. The
// canonical form is deterministic across processes that share the secret.
//
// # Secret rotation
//
// [New] accepts a [SecretSource] (not a single key) so deployments can
// rotate without rewriting the historical log. New entries sign with the
// current key id; verification accepts any key id the source still
// resolves. Drop a key id from the source only when its entries have aged
// out of the retention window.
package actionlog
