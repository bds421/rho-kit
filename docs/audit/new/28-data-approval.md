# NEW: data/approval — pending → approved/rejected → executed lifecycle

**Phase**: Theme 3 (Agentic safety)
**Status**: landed
**Module path**: `github.com/bds421/rho-kit/data/approval`
**Backends**: `data/approval/memory`, `data/approval/postgres`
**Middleware**: `httpx/middleware/approval`

## Why

Authorization answers "is this principal allowed to perform this
action?" Approval answers "yes, but should this *specific* instance go
through right now?"

For destructive verbs (delete a user, void an invoice, evict a tenant)
agentic services need the gate: agent calls a flagged endpoint, the
middleware records pending and returns 202, an approver hits a
separate decision endpoint, and only then does the original action
execute.

## Public API (summary)

```go
type State string
const (
    StatePending  State = "pending"
    StateApproved State = "approved"
    StateRejected State = "rejected"
    StateExecuted State = "executed"
    StateExpired  State = "expired"
)

type Request struct {
    ID, TenantID, Actor, Action, Resource string
    Payload   json.RawMessage
    State     State
    DecidedBy string
    DecidedAt time.Time
    Reason    string
    CreatedAt time.Time
    ExpiresAt time.Time
}

type Store interface {
    Create(ctx, Request) (Request, error)
    Get(ctx, id string) (Request, error)
    List(ctx, Query) ([]Request, error)
    Decide(ctx, id, decidedBy, reason string, approve bool) (Request, error)
    MarkExecuted(ctx, id string) (Request, error)
}
```

### State-transition contract

- **pending → approved | rejected**: `Decide(approve=true|false)`.
  Idempotent: same decision twice is a no-op.
- **approved → executed**: `MarkExecuted`. Idempotent.
- **pending → expired**: implicit on next `Decide` call past
  `ExpiresAt`; `Decide` returns `ErrInvalidTransition` so the late
  approver gets a distinct signal.
- **executed | expired**: terminal. Transitioning out returns
  `ErrInvalidTransition`.
- **flip refused**: approved → rejected (or vice-versa) returns
  `ErrInvalidTransition`. A flipped decision needs a fresh request so
  the audit trail records the reconsideration.

## HTTP middleware

`httpx/middleware/approval.Middleware(store, opts...)` wraps a
destructive route. On a request:

1. Resolve tenant via the configured tenant source (default header
   `X-Tenant-ID`) — 400 if missing.
2. Read up to `MaxBodyBytes` of the body (default 64 KiB) — 413 if
   over.
3. Resolve actor via the configured extractor (default
   `"anonymous"`).
4. `Create` a `Request` with the configured expiry (default 24h).
5. Respond 202 Accepted with `{"approval_id": "...", "status":
   "pending"}`.

Approval and execution are services' concerns: the kit doesn't define
the approver endpoint or the executor side. `WithExecutor` is wired
on the option for callers that want to plumb both sides through the
same middleware constructor.

## Definition of done

- [x] `data/approval` (top-level package).
- [x] `data/approval/memory` (in-process Store).
- [x] `data/approval/postgres` (GORM-backed Store + migration +
      transactional Decide).
- [x] `httpx/middleware/approval` (HTTP middleware).
- [x] Integration test (postgres testcontainer) under
      `//go:build integration`.
- [ ] Builder integration (Theme 3 sweep — separate PR).
- [ ] Recipe in `docs/ai/`.

## Trade-offs

- **Transactional store semantics**: postgres uses
  `SELECT FOR UPDATE` inside `Decide`/`MarkExecuted` to serialise
  concurrent approvers on the same row. SQLite (in tests) elides FOR
  UPDATE — fine because tests run serially. The auto-expire branch
  intentionally commits the state flip even though the surface error
  is `ErrInvalidTransition`; documented inline.
- **Body retention privacy**: the request body is persisted in the
  approval store until execution. Services with PII payloads
  (delete-user, etc.) should scrub before middleware.
- **Idempotency keying**: the middleware does not yet de-duplicate
  retries by an Idempotency-Key — a client retry produces a second
  approval request. Combine with `httpx/middleware/idempotency` if the
  client may legitimately retry.
