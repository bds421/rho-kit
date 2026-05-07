package approval

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// State is the lifecycle position of a [Request].
type State string

// State values. The set is closed: stores reject any other value.
const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateRejected State = "rejected"
	StateExecuted State = "executed"
	StateExpired  State = "expired"
)

// Sentinel errors for the [Store] contract.
var (
	// ErrNotFound is returned by [Store.Get] / [Store.Decide] /
	// [Store.MarkExecuted] when the id is unknown.
	ErrNotFound = errors.New("approval: request not found")

	// ErrInvalidTransition is returned by [Store.Decide] when the
	// caller tries to move out of a terminal state ([StateExecuted] or
	// [StateExpired]), or by [Store.MarkExecuted] when the request is
	// not currently approved.
	ErrInvalidTransition = errors.New("approval: invalid state transition")

	// ErrInvalidRequest is returned by [Store.Create] when required
	// fields are missing or invalid (e.g. zero/past ExpiresAt).
	ErrInvalidRequest = errors.New("approval: request is missing required fields")

	// ErrInvalidApprover is returned by [Store.Decide] when decidedBy
	// is empty. A blank approver makes the audit record useless and
	// hides the responsible operator behind state transitions on
	// destructive operations.
	ErrInvalidApprover = errors.New("approval: decidedBy must not be empty")
)

// Request represents a destructive operation pending human approval.
//
// Payload carries the original request body so the executor can replay
// the verb verbatim — middleware copies the body before handing the
// request off to the store. Storing the body has a privacy implication
// (e.g. PII in a delete-user payload) that callers should be aware of;
// scrub before construction if the body shouldn't be retained.
type Request struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenant_id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Resource  string          `json:"resource,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	State     State           `json:"state"`
	DecidedBy string          `json:"decided_by,omitempty"`
	DecidedAt time.Time       `json:"decided_at,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// IsTerminal reports whether the state is one no further transition can
// move out of: executed or expired.
func (s State) IsTerminal() bool {
	return s == StateExecuted || s == StateExpired
}

// Query controls which requests [Store.List] returns.
type Query struct {
	TenantID string
	Actor    string
	Action   string
	State    State
	Since    time.Time
	Until    time.Time
	Limit    int
}

// Store is the persistence interface implemented by data/approval/memory
// and data/approval/postgres.
//
// Decide and MarkExecuted are deliberately separate so the middleware
// that drives execution can record "approved" cleanly and "executed"
// only after the work has actually run. A combined "approve and
// execute" call would force the store to span the executor's lifetime.
type Store interface {
	// Create persists a new request in [StatePending]. CreatedAt and
	// ExpiresAt are caller-supplied so the middleware can decide its
	// own clock + expiry policy. ID is also caller-supplied — the
	// middleware generates UUIDv7 ids.
	Create(ctx context.Context, r Request) (Request, error)

	// Get returns the request by id, or [ErrNotFound] if absent.
	// Get does not change state, even if the request is past
	// ExpiresAt; the implicit transition to [StateExpired] happens on
	// the next Decide call. This keeps reads side-effect-free.
	Get(ctx context.Context, id string) (Request, error)

	// List returns requests matching q, newest-first.
	List(ctx context.Context, q Query) ([]Request, error)

	// Decide records an approver's decision.
	//
	// Idempotency: calling Decide twice with the same approve value on
	// the same id is a no-op (returns the unchanged request). The
	// caller's DecidedBy/Reason replace the stored values, since the
	// "same decision" is semantically the same regardless of who
	// repeated the call.
	//
	// Expiry: if the request is in StatePending and CreatedAt + ttl
	// has passed, Decide first transitions it to StateExpired, then
	// returns [ErrInvalidTransition] — the late approver gets a
	// distinct error so they can communicate the timeout to the
	// requester.
	//
	// Out-of-terminal: Decide returns [ErrInvalidTransition] when the
	// request is in StateExecuted or StateExpired and the requested
	// transition is not idempotent.
	Decide(ctx context.Context, id, decidedBy, reason string, approve bool) (Request, error)

	// MarkExecuted moves a StateApproved request to StateExecuted.
	// Returns [ErrInvalidTransition] for any other source state. The
	// transition is idempotent: a second MarkExecuted on an already-
	// executed request returns the unchanged request rather than
	// erroring, matching the Decide contract.
	MarkExecuted(ctx context.Context, id string) (Request, error)
}

// validate enforces required-field invariants for new requests.
//
// ExpiresAt MUST be set and in the future. Direct store callers can
// otherwise create permanent pending approvals that never auto-expire,
// which defeats the kit's bounded-decision-window invariant.
func validate(r Request, now time.Time) error {
	if r.ID == "" || r.TenantID == "" || r.Actor == "" || r.Action == "" {
		return ErrInvalidRequest
	}
	if r.State != "" && r.State != StatePending {
		return ErrInvalidRequest
	}
	if r.ExpiresAt.IsZero() {
		return ErrInvalidRequest
	}
	if !r.ExpiresAt.After(now) {
		return ErrInvalidRequest
	}
	return nil
}
