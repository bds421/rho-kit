# AGENTS.md — `resilience/timeoutbudget`

## When to use this package

- The handler fans out to multiple downstreams and you want to
  give each call "as much time as the request has left" rather
  than fixed per-call deadlines that add up to far more than
  the request's SLO.
- You need to reserve a portion of the request budget for
  post-call work (audit log, idempotency unlock, metrics emit)
  that the downstream call must NOT eat into.
- You want observability into "where did the request's time go"
  — Budget tracks Remaining + Reservation.

## When to use something else

- **Single downstream call:** `context.WithTimeout` is simpler.
  This package's value is in propagating ONE budget across
  multiple call sites.
- **Bound the number of retry attempts, not total time:** use
  `resilience/retry.Policy.MaxRetries`. The kit also has
  `MaxElapsedTime` on retry policy for per-call retry budget —
  that's distinct from this package's request-level envelope.
- **Cap concurrent in-flight calls (saturation, not deadline):**
  `resilience/bulkhead` — different semantics.

## Key APIs

- `New(ctx, total, opts...)` — Construct. Returns `(ctx,
  *Budget, cancel)`. Pass the returned ctx down the call chain;
  callers extract the Budget via `FromContext(ctx)`.
- `Budget.WithRemaining(parent)` — Carve a child ctx whose
  deadline reflects the budget's current remaining time (minus
  active reservations). Returns `ErrBudgetExhausted` when
  remaining ≤ 0.
- `Budget.WithReservation(d)` — Hold back `d` from `Remaining()`
  for post-call work. Returns a restore function the caller
  MUST defer.
- `Budget.Remaining()` / `Used()` / `Reservation()` —
  Observability accessors for "where is the time going."
- `FromContext(ctx)` — Retrieve the budget attached by `New`.

## Common mistakes

- **Treating `WithRemaining` as a replacement for `context.WithTimeout`
  inside library code.** Library code should accept ctx and
  not assume a budget exists; use `FromContext` and fall back
  to "just use ctx" when nil.
- **Forgetting to defer the restore from `WithReservation`.**
  A reservation that's never restored leaves later calls
  short-budget.
- **Reservation accounting in a hot loop.** Reservations add
  and subtract under a mutex; `WithReservation` per request is
  the right granularity, not per loop iteration.
- **Confusing the package's request-scoped envelope with
  per-attempt retry budget.** `retry.Policy.MaxElapsedTime` is
  the right knob for "stop retrying after N seconds of attempts."
  This package wraps the WHOLE request — retries plus siblings.

## Observability

- No metrics emitted directly; Budget exposes Remaining / Used
  / Reservation as accessors. Wire the consumer's request-end
  middleware to record `Used()` and `Remaining()` into the
  service's request-duration metric labels (or as span
  attributes).
- OTel spans: not emitted per `WithRemaining` call — that
  would double the downstream call's span count. Record the
  budget snapshot as a span attribute at the request boundary.
