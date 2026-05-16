// Package timeoutbudget propagates a request-scoped time budget
// across multiple downstream calls. Each downstream call takes
// the budget's remaining time, optionally reserves a portion for
// post-call work, and leaves the rest for siblings.
//
// # Why this exists
//
// In a service that fans out to N downstreams, each call needs a
// timeout. The naive approach gives each its own fixed deadline,
// which has two failure modes:
//
//   - **Late-budget waste:** Downstream A takes 4900ms of the
//     5s SLO; downstream B then starts with the full 5s timeout
//     and the operator-visible total request time becomes ~10s.
//   - **Early-budget bottleneck:** Each downstream has 1s; if
//     the first call takes 950ms, the remaining downstreams
//     all start with 1s in isolation but the total budget is
//     blown after just a few of them.
//
// `timeoutbudget.New(ctx, total)` carves up the original ctx's
// deadline so every `WithRemaining(ctx)` returns a child ctx
// with the BUDGET's remaining time (or the parent ctx's, if
// tighter). Each downstream sees a deadline that reflects
// "how much time is left for the whole request, not just for
// me." This composes correctly with circuit breakers and
// retries — the breaker's fast-fail releases budget back to
// siblings.
//
// # Reservations
//
// Some service shapes need post-call work that the budget must
// cover (writing an audit log, releasing an idempotency lock,
// emitting metrics). `WithReservation(d)` keeps `d` aside so
// the downstream call deadline is `remaining - reservation`,
// leaving exactly `reservation` for post-call work.
//
// # Tracking
//
// The Budget exposes `Used()`, `Remaining()`, and
// `Reservations()` so an observability layer can record how
// the budget was spent — useful when multiple downstreams
// share one budget and an operator wants to know which one
// burned it.
//
// # Cooperation with circuitbreaker + retry
//
// Canonical order:
//
//	timeoutbudget.New (per request)
//	  → circuitbreaker.ExecuteCtx (fast-fail releases budget)
//	    → retry.DoWith (each attempt sees the budget)
//	      → downstream call (deadline = budget.Remaining())
//
// `retry.Policy.MaxElapsedTime` is the kit's existing per-call
// retry budget. `timeoutbudget` is the request-scoped envelope
// that contains all such per-call retries plus any siblings.
package timeoutbudget
