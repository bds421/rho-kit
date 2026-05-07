// Package approval is the kit's pending → approved/rejected → executed
// state machine for destructive operations.
//
// # Why this exists
//
// Agentic services need a gate around irreversible verbs (delete a
// user, void an invoice, evict a tenant) that's stronger than authz
// alone. authz answers "is this principal allowed to perform this
// action?"; approval answers "yes, but should this specific instance
// of the action go through right now?"
//
// The flow is:
//
//  1. Agent calls a flagged endpoint with the original request body.
//  2. Middleware reads tenant + actor + body, creates a [Request] in
//     state [StatePending], and returns 202 Accepted with the
//     approval id.
//  3. Out-of-band, an approver calls [Store.Decide] with approve=true
//     or false. The kit doesn't define this endpoint — services choose
//     where it lives — but the [Store] interface is shared.
//  4. The original handler executes (via the middleware's optional
//     executor callback) and the request transitions to
//     [StateExecuted].
//
// Pending requests automatically transition to [StateExpired] when
// [Store.Decide] is called past [Request.ExpiresAt]; the middleware
// applies a 24h default unless overridden.
//
// # Idempotency and irreversibility
//
// [Store.Decide] is idempotent for the same approve value:
// approve+approve is OK, reject+reject is OK. The transition out of a
// terminal state is rejected with [ErrInvalidTransition], so an
// already-executed request can't be re-rejected by a late-arriving
// approver, and an expired request can't be rescued by a late
// approval. This matches how production approval systems behave —
// once an action has run, the only path back is a compensating action.
package approval
