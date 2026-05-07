# v2.0.0 — independent security review

**Reviewer**: security-reviewer agent
**Branch**: main @ d43b47590f81dbf9059064738be08ef7f9d54214
**Scope**: 7 themes shipped in v2.0.0 (actionlog, approval, budget/redis, mcp, signedrequest, secret, tenant) plus the example/builder/grpcx/messaging surface.

## Verdict

**Ship-with-followups.** No CRITICAL forgery / cross-tenant / secret-leak defect was found in the load-bearing primitives. HMAC compare paths use `hmac.Equal`; canonicalisation sorts map keys at every level; the postgres approval store wraps `Decide` and `MarkExecuted` in `SELECT … FOR UPDATE` transactions; the Redis budget Lua is genuinely atomic and refunds floor at zero; the tenant middleware composes correctly outside-in (signedrequest → tenant → budget → handler). Two HIGH defects warrant follow-up before this surface is exposed to untrusted callers: (1) `secret.String` only redacts via pointer-receiver methods, so a value-typed copy or unsafe-reflect path leaks plaintext, and (2) the MCP server's "skip audit log when no tenant" branch is a fail-open audit gap — the tool ran but no signed entry exists. Both are fixable without API breakage. Several MEDIUM/LOW items round out the v2.1 list.

## Critical findings

None.

## High findings

### H-1. `secret.String` redaction methods are pointer-receivers; value-typed usage leaks plaintext
**Severity**: HIGH
**File**: `core/secret/secret.go:116-144`

`String()`, `GoString()`, `MarshalJSON()`, `MarshalText()`, `LogValue()`, and `Format()` are all defined on `*String`. The pointer method set is a strict superset of the value method set. If a developer ever passes `*s` (deref), embeds `secret.String` by value into another struct (which then gets formatted), or copies the struct, the redaction methods are not in the value method set and `fmt.Printf("%+v", s)` prints the underlying `buf []byte` as a decimal byte slice — i.e. the plaintext, decoded. Reproducer:

```go
s := secret.String{} // ← unexported buf empty; populated via reflect or by mistake
// or, more realistically: a struct that embeds `secret.String` by value
fmt.Printf("%+v", s) // → {mu:{...} buf:[115 117 112 101 114 ...]}
```

The kit's own constructors (`New`, `NewFromString`) return `*String`, and `sync.RWMutex` makes by-value copies a `go vet` warning, so the natural usage is safe. But the type is exported and so are downstream consumers that may not run vet, and a single `var s secret.String` declaration in a config struct breaks the redaction contract.

**Fix**: Add value-receiver versions of `String`, `GoString`, `MarshalJSON`, `MarshalText`, `LogValue`, `Format` (each can simply forward to the pointer method or return the redacted literal directly; concurrent safety isn't a concern for the redaction methods because they don't read `buf`). Alternatively rename the type to a non-exported `stringImpl` and export only `*String` via a type alias / opaque interface so by-value declaration is impossible.

### H-2. MCP `recordActionLog` silently skips audit on missing tenant — fail-open audit trail
**Severity**: HIGH
**File**: `httpx/mcp/actionlog.go:38-49`, called from `httpx/mcp/server.go:206`

When `tenantExtractor(ctx)` returns false the server logs `slog.Warn("mcp: skipping action log entry; no tenant on context")` and returns. The tool itself has already executed (line 202 of `server.go` runs `entry.dispatch` before `recordActionLog`). The contract is "every MCP tool call writes an attributed entry" (per `mcp.go:120-127` doc) but the actual implementation is "every tool call writes an entry, except when we couldn't, in which case the call still succeeded." That is the wrong direction for an audit log.

Concrete attack: a service deployed with `WithMultiTenant(extractor, /*required=*/false)` accepts MCP POSTs without `X-Tenant-Id`, the tenant middleware passes the request through, the MCP handler runs the tool, and the action log writes nothing. An operator running the SQL forensic query "what did agent X do this hour against tenant Y" gets a partial answer.

The skip path is also asymmetric with the `actionLogger == nil` early return on line 39: when no logger is configured the doc explicitly says audit moves to transport-layer logging — a deliberate opt-out. The "no tenant" path has no such opt-out semantics; it's a configuration error that silently elides audit.

**Fix**: When an action logger is configured but the tenant resolves to empty, fail closed — return a `-32603 internal error` to the JSON-RPC caller before invoking the tool. (Equivalent: invoke `recordActionLog` *before* `entry.dispatch`, with a "denied" outcome on the missing-tenant branch, refusing the call.) The signed-store invariant that rejects empty `TenantID` is correct; the kit's middleware should match it rather than work around it.

A weaker mitigation: keep the behaviour but elevate the slog level to `Error`, document that production deployments must require tenant middleware, and add a `Validate()`-time check in `app.Builder` that refuses `WithActionLogger + WithMultiTenant(required=false)` together.

### H-3. `WithMaxDeliver` not configured on the JetStream consumer — poison-pill DoS
**Severity**: HIGH (operational, not data-confidentiality)
**File**: `infra/messaging/natsbackend/natsbackend.go:262-268, 294-302`

`Consumer.Consume` creates the durable consumer with no `MaxDeliver` field set. The JetStream default for `MaxDeliver` is `-1` (unlimited). The dispatch loop on line 294 catches handler panics, calls `Nak()`, and resumes consumption. A message that reliably triggers a panic in the handler (malformed payload that survived `json.Unmarshal` but trips downstream type assertions, or a tenant-id that triggers an integer overflow in some accounting code) is redelivered indefinitely. The malformed-message branch on line 308-315 does the right thing (`Term()` — no redelivery) but only catches `json.Unmarshal` failures; anything that unmarshals and later panics goes into an infinite Nak loop.

Note also the doc-vs-code mismatch on line 293: "the panic is re-raised so process-level recovery can react" — the implementation only logs and Naks, no `panic(r)`.

**Fix**: Add `MaxDeliver` to `ConsumerConfig` (default `5`) and set it in the `CreateOrUpdateConsumer` call. After `MaxDeliver` is exhausted JetStream sends the message to the configured DLQ if any, else drops; either is preferable to forever-replay. Also reconcile the doc: either re-`panic(r)` after the Nak or rewrite the comment.

## Medium findings

### M-1. `mapErrorToRPC` default and conflict branches surface raw error text
**Severity**: MEDIUM
**File**: `httpx/mcp/server.go:280-284`

The `default` case returns `err.Error()` verbatim to the JSON-RPC client. Wrapped errors from infrastructure (GORM "pq: relation \"x\" does not exist", "context deadline exceeded: connection refused: 10.0.0.1:5432") leak internal topology to whoever can call a tool. The `apperror.IsConflict` branch (line 281) has the same shape. The validation / not-found / auth-required branches are intentional (callers benefit from field-level details); the default is not.

**Fix**: In the default branch, log the full error server-side and return a generic "internal error" to the client. Match the pattern in `grpcx/interceptor/recovery.go` which already does this for gRPC.

### M-2. Postgres migrations use `TIMESTAMP` (without time zone) for signed/decided times
**Severity**: MEDIUM
**Files**:
- `data/actionlog/postgres/migrations/20260507000001_create_action_log_entries.sql:11`
- `data/approval/postgres/migrations/20260507000001_create_approval_requests.sql:13-14`

The action log columns `occurred_at` and the approval columns `created_at` / `expires_at` / `decided_at` are declared `TIMESTAMP`, which in Postgres is `TIMESTAMP WITHOUT TIME ZONE`. The Go side stores `.UTC()` and re-applies `.UTC()` after reading (`store.go:160`), but `TIMESTAMP WITHOUT TIME ZONE` columns return values whose interpretation depends on the database session's `TimeZone` setting. Combined with the action log's HMAC re-verification, which formats `OccurredAt.UTC().Format(time.RFC3339Nano)` into the canonical signing input (`canonical.go:50`), a session timezone of anything other than UTC can cause every signature verification to fail after a round trip on some drivers. With pgx the issue is muted (it normalises to UTC); with `database/sql` + `lib/pq` it's not guaranteed.

**Fix**: Change the column type to `TIMESTAMPTZ` in both migrations. Existing tables can migrate via `ALTER TABLE … ALTER COLUMN … TYPE TIMESTAMPTZ USING … AT TIME ZONE 'UTC'`.

### M-3. Example HMAC secret is not labelled "demo-only"
**Severity**: MEDIUM (documentation)
**File**: `examples/agentic-service/internal/app/app.go:39-41`, `examples/agentic-service/README.md`

The example hard-codes `"at-least-32-bytes-of-secret-bytes!"` as the v1 HMAC key. The README's "What's NOT in this example" section mentions auth and persistence but does not call out that the secret is a placeholder. Anyone copying the example into their service starter has a working signing key with zero entropy. The string itself even passes the `>= 32 byte` check in `NewStaticSecrets`, so the panic guardrail does not fire.

**Fix**: Add an explicit `// SECURITY: demo value, do not reuse — load from env/secret manager in production` comment beside the literal, and a one-line warning in the README's "What's NOT in this example" list. Optionally, make `NewStaticSecrets` reject keys that match a small known list of demo strings.

### M-4. `signedrequest`'s `MemoryNonceStore` is not safe across multi-replica deployments
**Severity**: MEDIUM (documentation gap, not a defect)
**File**: `httpx/middleware/signedrequest/noncestore.go:8-15`

Already correctly documented in the package comment. Flagging only because the kit ships exactly one `NonceStore` implementation and a multi-replica deployment that drops in `MemoryNonceStore` (the obvious choice) silently loses replay protection across replicas. A second-shipped Redis implementation would close the gap; alternatively, the `Middleware` constructor could refuse `MemoryNonceStore` when a "production" hint is set (parallel to `app.Builder.WithProductionDefaults`).

## Low findings

### L-1. `actionlog.canonicalForm` uses `\n` join with no length-prefix or escape
**Severity**: LOW
**File**: `data/actionlog/canonical.go:42-53`

A field value containing a literal newline can shift the field boundary in the canonical bytes. Two distinct entries (different field assignments, same total bytes after join) can produce identical canonical strings and therefore identical signatures. Forgery is not possible without the secret, and entry IDs disambiguate at the row level, but two entries with identical signatures is a forensic nuisance. Defence-in-depth: length-prefix each field (`fmt.Fprintf(&buf, "%d:%s\n", len(field), field)`) or hex-encode field values that may contain whitespace.

### L-2. `signedrequest.MemoryNonceStore` sweeps every 256 calls — pathological-traffic concern
**Severity**: LOW
**File**: `httpx/middleware/signedrequest/noncestore.go:36, 47-49`

Under sustained, low-replay traffic (every nonce unique, sweep never triggers a meaningful cleanup) the map grows linearly. The sweep at every 256th call is an O(n) walk under the lock. For a high-RPS service this is a latency spike every 256 requests proportional to map size. Not a correctness issue; consider a time-based sweep goroutine or a heap-ordered expiry index.

### L-3. `mcp.invoke` writes JSON-RPC body before action log returns — log-after-respond ordering
**Severity**: LOW
**File**: `httpx/mcp/server.go:202-220`, `httpx/mcp/actionlog.go:79`

`recordActionLog` is called between `dispatch` and `writeJSONRPCRaw`, which is correct in the sense that the audit append happens before the response is written. However, the audit logger uses `context.WithoutCancel(ctx)` (`actionlog.go:79`) so a request-context cancel after the response is queued cannot interrupt it. That part is fine; the LOW issue is that `recordActionLog` runs synchronously on the request hot path. A slow Postgres can extend MCP latency. Consider a buffered async append for the production path (already documented as a future via "weaker posture" in the package doc, just confirming the trade-off is intentional).

### L-4. `httpx/mcp` schema validation: `DisallowUnknownFields` rejects requests but not via -32602 with field name
**Severity**: LOW
**File**: `httpx/mcp/server.go:233`

`buildDispatch` calls `dec.DisallowUnknownFields()`, which is correct hardening (don't silently accept extra fields), but the resulting error message ("json: unknown field \"foo\"") is opaque. Combined with M-1's leak, this prints a JSON shape detail back to the agent. Fine for trusted agents; consider sanitising for untrusted callers.

## Items checked — no findings

- `data/actionlog/actionlog.go:395` — `hmac.Equal` is constant-time. Verified.
- `data/actionlog/actionlog.go:299-329` (Append) and `:332-356` (Get/List) — signing-secret never appears in any error string; the only secret-adjacent error wraps the *key id* (`actionlog: current key id %q not resolvable`), not the secret bytes. Good.
- `data/actionlog/canonical.go:63-115` — `canonicalJSON` recursively sorts map keys at every depth and disables HTML escape; canonical bytes are deterministic.
- `data/actionlog/actionlog.go:228-242` — `NewStaticSecrets` deep-copies the secret map and panics on `< 32` byte keys; rotation race is bounded because the map is captured by value at construction and is read-only thereafter.
- `data/approval/postgres/store.go:148-223` — `Decide` opens a `tx.Transaction`, takes `clause.Locking{Strength: "UPDATE"}` on the row, and commits the auto-expire write before returning the late-approval error. Two concurrent approvers serialise on the row lock; idempotency on same-decision is correct; flip-after-decision is rejected.
- `data/approval/postgres/store.go:227-260` — `MarkExecuted` similarly uses `FOR UPDATE` and the transition `Approved → Executed` is atomic.
- `data/approval/postgres/store.go:84-130` (Get/List) and `:65-81` (Create) — payload column is `jsonb` (matching the migration), all queries use parameter substitution via GORM, no raw SQL or string concatenation.
- `data/budget/redis/redis.go:58-80` — the optimistic-INCR-then-DECR-on-overcap Lua is genuinely atomic; the EXPIRE happens only on the success path so a rejected attempt cannot inadvertently extend a stale window's TTL.
- `data/budget/redis/redis.go:96-116` — the refund script floors `newUsed` at zero before recomputing remaining, so an over-refund cannot inflate future Consume headroom past the cap. Verified against the audit's "under-flow when refund > consumed" concern.
- `data/budget/redis/redis.go:135-138, 218-219` — key prefix is configurable per-Budget instance; bucket key includes the period id; collisions across services with distinct prefixes are impossible.
- `httpx/middleware/signedrequest/signedrequest.go:189-207` — verification order is correct: timestamp / signature / body / MAC compare / nonce. Nonce is recorded only after MAC validates, so attacker traffic cannot poison the nonce store. `hmac.Equal` is the comparator. Body-tampering window is closed because `readBody` rewinds the body to the bytes that were MAC'd.
- `httpx/middleware/signedrequest/signedrequest.go:166-174` — clock-skew comparison uses absolute delta, both directions of skew rejected.
- `httpx/middleware/signedrequest/signedrequest.go:209-221` — error mapping converts internal sentinels to opaque 400/401/413, no internal state leak.
- `httpx/middleware/tenant/tenant.go:77-98` — when an extractor returns ok=false on a state-changing method, `WithRequired(true)` returns 400 before invoking next; safe methods (GET/HEAD/OPTIONS) pass through unchanged. Confirmed there is no path where a state-changing request lands without a tenant ctx and proceeds (the only such path is `WithRequired(false)` which is an explicit operator opt-in).
- `app/builder.go:922-937` — middleware composition order is correct: the chain wraps inside-out so the executed order at request time is signedrequest → tenant → budget → handler. Tenant middleware sees a request whose body has already been MAC-verified; budget middleware reads the tenant ctx populated by the tenant middleware.
- `grpcx/interceptor/deadline.go:60-68` — `withDefaultDeadline` correctly returns a no-op cancel when the inbound deadline is already tighter, so the `defer cancel()` pattern is safe in either branch. Recovery interceptor (`recovery.go:29-40`) wraps the handler with its own defer; deferred cancels execute on panic unwind in LIFO order, so the deadline's cancel runs after recovery has captured the panic value. No race.
- `core/secret/secret.go:63-87` (Reveal/RevealString) — returns a copy; underlying `buf` remains owned by the String. The buffer is zeroed in `Close()` (line 108-110). No leak to GC distinct from the standard "the returned copy will be GC'd" — that's the caller's responsibility.
- `core/secret/secret.go:122-144` — JSON, text, slog, and fmt paths all emit the redacted literal when called via `*String`. Confirmed via `core/secret/secret_test.go:55-126`.
- `httpx/mcp/server.go:60-70` — handler refuses non-POST methods; non-POST cannot reach `dispatch`.
- `httpx/mcp/server.go:79-83` — JSON-array (batch) requests rejected before parse; single-call semantics preserved.
- `httpx/mcp/server.go:269-277` — auth-required and forbidden errors map to `rpcErrMethodNotFound` on purpose so an unauthenticated probe cannot enumerate registered tools.
- `httpx/mcp/mcp.go:217-227` — default `maxRequestBytes` is 1 MiB; `WithMaxRequestBytes(0)` panics. Body cap is enforced in `readBody` before parse.
- `data/actionlog/postgres/store.go:79-116` — all queries parameterised; tenant-scoped composite index `idx_action_log_entries_tenant_occurred` covers the common `(TenantID, OccurredAt DESC)` query path.
- `data/approval/postgres/migrations/…sql:17-22` — composite index covers `(tenant_id, state)` and `(state, expires_at)`; sufficient for the tenant-scoped dashboard query and the expiry sweep.
- All v2.0.0 module `go.mod` files reviewed (`actionlog`, `approval`, `budget/redis`, `signedrequest`, `secret`, `mcp`); no obviously stale or known-vulnerable pins. `redis/go-redis/v9 v9.18.0`, `gorm.io/gorm` (latest line), `google/uuid v1.6.0`, `nats-io/nats.go` — all current.

## Recommendations for v2.1

- Add a `Validate()`-level check in `app.Builder` that refuses `WithActionLogger` + `WithMultiTenant(required=false)` together (or any other path where the action log can be configured but tenant-less requests can reach a tool).
- Provide a `signedrequest.RedisNonceStore` so multi-replica deployments don't have to roll their own. Pair with a Builder-level "production posture" check that warns on `MemoryNonceStore` when KIT_ENV=production.
- Fold the `secret.String` value-receiver redaction into v2.1 (H-1 fix) — non-breaking change, immediate hardening.
- Add the `mcp` action-log fail-closed path (H-2) behind a default-on `WithStrictAudit()` option so operators who deliberately want the loose posture must opt out.
- Set `MaxDeliver` default in `natsbackend.ConsumerConfig` and document the DLQ pattern for poison messages (H-3).
- Migrate Postgres timestamp columns to `TIMESTAMPTZ` (M-2) and add a CI lint that rejects `TIMESTAMP` (without TZ) in new migrations.
- Length-prefix or escape canonical-form fields in `actionlog.canonicalForm` (L-1) — defence-in-depth, not a forgery fix.
- Reconcile the `natsbackend.dispatch` panic-recovery doc vs. behaviour (panic is logged + Nak'd, not re-raised).
- Document in `examples/agentic-service` that the literal HMAC secret is a placeholder (M-3); ideally read it from `KIT_DEMO_HMAC_KEY` env to make copy-paste-into-prod harder.
