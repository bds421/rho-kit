# NEW: data/actionlog — append-only signed log of agent actions

**Phase**: Theme 3 (Agentic safety)
**Status**: landed
**Module path**: `github.com/bds421/rho-kit/data/actionlog`
**Backends**: `data/actionlog/memory`, `data/actionlog/postgres`

## Why

Agentic services need a record of "agent X performed action Y at time
T against tenant Z" that is:

- **Distinct from request audit.** `observability/auditlog` records
  HTTP request shape ("POST /v1/users/123 → 200"). Forensics readers
  (compliance, incident response) need application-level verbs
  ("user.delete on users/123 by agent-7").
- **Tenant-scoped.** Multi-tenant deployments cannot read a single
  cross-tenant audit table without leaking customer data; tenant is a
  required first-class field.
- **Tamper-evident.** A DBA who edits a row directly should produce a
  visible failure on the next read, not silently rewrite history.

## Public API (summary)

```go
type Outcome string
const (
    OutcomeSuccess Outcome = "success"
    OutcomeFailure Outcome = "failure"
    OutcomeDenied  Outcome = "denied"
)

type Entry struct {
    ID, TenantID, Actor, Action, Resource, Reason string
    Outcome        Outcome
    Metadata       map[string]any
    OccurredAt     time.Time
    SignatureKeyID string
    Signature      string
}

type Logger interface {
    Append(ctx, Entry) (Entry, error)
    Get(ctx, id string) (Entry, error)
    List(ctx, Query) ([]Entry, error)
    Sign(Entry) (sig, keyID string, err error)
    Verify(Entry) error
}
```

`Logger` is constructed via `actionlog.New(store, secrets)`. Backends
in `data/actionlog/memory` (tests) and `data/actionlog/postgres` (prod)
implement `Store`.

## Signing

Signatures are HMAC-SHA256 over a deterministic newline-joined
canonical form of the entry's fields. The metadata bag is canonicalised
as JSON with lexicographically sorted keys at every level so two
semantically equal entries produce byte-identical canonical forms.
`Logger.Get` and `Logger.List` verify before returning; tampered
entries surface `ErrSignatureInvalid`.

The `SecretSource` abstraction supports key rotation: each row carries
the key id it was signed with, so old entries verify against old keys
even after the current key is rotated. `StaticSecrets` is the simple
in-process implementation; production deployments back this with their
secret manager.

## Definition of done

- [x] `data/actionlog` (top-level package, signed Logger, validators).
- [x] `data/actionlog/memory` (thread-safe in-process Store).
- [x] `data/actionlog/postgres` (GORM-backed Store + migration).
- [x] Tamper-detection tests for both backends.
- [x] Integration test (postgres testcontainer) under
      `//go:build integration`.
- [ ] Builder integration (Theme 3 sweep — separate PR).
- [ ] Recipe in `docs/ai/`.

## Trade-offs

- **Signing key rotation**: drop a key id from `SecretSource.Resolve`
  only after every row signed with it has aged out. Until then the
  read path needs to be able to resolve it.
- **Metadata canonicalisation**: the chosen rule (sorted keys, no HTML
  escape) is deterministic but not RFC8785 (JCS). RFC8785 would buy
  cross-language verification at the cost of extra dependencies.
  Revisit when an off-band verifier in another language is needed.
