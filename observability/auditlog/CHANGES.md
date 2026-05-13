## Unreleased (v2.0.0)

### Breaking

- `auditlog.New` now **requires** both `WithChainKey(...)` and
  `WithCursorKey(...)` (each ≥32 bytes). Construction panics fast if
  either key is missing or too short, per the AGENTS.md fail-fast rule.
  This closes two `docs/audit/THREAT_MODEL.md` §5.4 gaps that the
  previous release advertised but did not implement: tamper-evident
  HMAC chains and forgery-resistant pagination cursors. Callers
  (`app.Builder.WithAuditLog`, `httpx/middleware/auditlog` test
  fixtures, custom wiring) must pass both keys; treat the new
  Option-required check as a v2.0.0 source-incompatible change.
- The `Store` interface gained `LastHMAC(ctx) ([]byte, error)`.
  Bundled `MemoryStore` and any downstream Store implementations must
  return the tail HMAC of the most recently appended event (or nil /
  empty for an empty store). `Logger.LogE` calls `LastHMAC` under the
  append mutex to compute each new event's `PrevHMAC`.
- `Event` gained `PrevHMAC []byte` and `HMAC []byte` fields. Stores
  must persist both fields verbatim; `Logger.LogE` discards any
  caller-supplied values and recomputes them from the chain tail.
  Stored JSON payloads marshal the byte slices as base64.

### Added

- HMAC chain over every appended event. Each record's HMAC is
  HMAC-SHA256 of `canonical(prevHMAC, eventWithoutHMAC)` keyed by the
  per-Logger chain key. The canonical encoding length-prefixes every
  field so adjacent-field confusion cannot produce HMAC collisions.
- `auditlog.VerifyChain(events, chainKey)` — validates a slice of
  events in chain order. Returns wrapped `ErrChainBroken` with the
  offending index on mismatch; HMAC comparison is constant-time.
- `Logger.VerifyChain(ctx)` — streams every event from the underlying
  store (paged in batches of 500) and runs `VerifyChain` on the
  reassembled chain. Useful for periodic compliance verification and
  on-call investigation.
- `Logger.Query` now signs pagination cursors with `WithCursorKey`.
  The wire format mirrors `httpx/pagination.CursorSigner`:
  `base64url(payload) "." base64url(HMAC-SHA256(cursorKey, payload))`.
  Malformed / foreign-signed cursors return a wrapped
  `ErrInvalidCursor` that callers can `errors.Is` and map to 400 Bad
  Request.
- New typed error sentinels: `ErrChainBroken`, `ErrInvalidCursor`,
  along with constants `HMACSize`, `MinChainKeyLen`, `MinCursorKeyLen`.

### Internal

- `Logger.LogE` serialises the read-prev-HMAC / compute / Append
  window through `appendMu` so two concurrent appenders cannot
  observe the same `PrevHMAC` and fork the chain.

## 1.1.0 (2026-04-06)

This was a version bump only for observability/auditlog to align it with other projects, there were no code changes.
