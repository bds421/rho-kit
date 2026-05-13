package approval

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
)

// MaxIDLen caps Request.ID length (audit FR-055). The Postgres schema
// declares id VARCHAR(36) — UUIDs (36 chars), shorter ULIDs (26),
// nanoid (21), and hex IDs all fit. Pre-fix the package allowed up to
// 255 characters and the database surfaced the failure late.
const MaxIDLen = 36

// MaxPayloadSize caps Request.Payload bytes (audit FR-056). Approval
// payloads carry a copy of the original verb body and persist
// indefinitely until decided + executed; without a cap a misuse can
// retain multi-megabyte JSON for hours and amplify privacy/storage
// risk. 64 KiB is comfortably above any realistic API payload while
// preventing accidental large persistence.
const MaxPayloadSize = 64 * 1024

// Store field length caps mirror the Postgres schema so the memory and
// Postgres stores expose the same API contract. Validate at the package
// boundary instead of letting only one backend surface late database errors.
const (
	MaxTenantIDLen = 255
	MaxActorLen    = 255
	MaxActionLen   = 255
	MaxResourceLen = 500
	MaxReasonLen   = 4096
)

// requestIDPattern bounds Request.ID to a safe character set: ASCII
// letters, digits, hyphen, and underscore. UUIDs (with hyphens), ULIDs,
// and hex IDs all fit. Same policy used by data/queue/redisqueue.Message.ID
// — caller-supplied IDs that survive into log lines, metric labels, and
// downstream key paths must be tokens, not free text.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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
	// is empty or otherwise invalid. A blank approver makes the audit
	// record useless and hides the responsible operator behind state
	// transitions on destructive operations.
	ErrInvalidApprover = errors.New("approval: decidedBy is invalid")

	// ErrInvalidReason is returned by [Store.Decide] when the optional
	// reason is malformed or exceeds [MaxReasonLen].
	ErrInvalidReason = errors.New("approval: reason is invalid")

	// ErrInvalidStore is returned when a Store method is invoked on a nil
	// or otherwise uninitialized store implementation.
	ErrInvalidStore = errors.New("approval: store is not initialized")
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

var (
	// ErrQueryTenantRequired is returned by Store.List when the caller
	// passes a [Query] with no [Query.TenantID] and has not opted into
	// [Query.AllTenants]. Cross-tenant approval listings are valid for
	// admin / forensics tooling but must be opt-in (audit FR-053).
	ErrQueryTenantRequired = errors.New("approval: query requires TenantID or AllTenants=true")

	// ErrQueryScopeConflict is returned when a [Query] sets both
	// [Query.TenantID] and [Query.AllTenants]. Tenant-scoped and
	// cross-tenant reads are intentionally mutually exclusive so
	// privileged callers cannot accidentally hide a wiring bug behind
	// store-specific filter precedence.
	ErrQueryScopeConflict = errors.New("approval: query must not set both TenantID and AllTenants=true")
)

// Query controls which requests [Store.List] returns. Filters compose
// with AND semantics; an empty filter field is unconstrained. The
// caller MUST set exactly one of [Query.TenantID] (single-tenant query)
// or [Query.AllTenants]=true (explicit cross-tenant query); a zero query
// is rejected with [ErrQueryTenantRequired], and a query that sets both
// scope modes is rejected with [ErrQueryScopeConflict].
type Query struct {
	// TenantID restricts to a single tenant. Required unless
	// AllTenants is true. Mutually exclusive with AllTenants.
	TenantID string

	// AllTenants opts into a cross-tenant listing. Set this only on
	// admin / forensics tooling that genuinely needs to see approval
	// requests across customers — it bypasses the tenant scoping
	// that the rest of the kit enforces. Mutually exclusive with
	// TenantID. Audit FR-053 [HIGH]: pre-2.0, the absence of this flag
	// meant a handler that forgot to set TenantID silently leaked
	// approval requests across tenants.
	AllTenants bool

	Actor  string
	Action string
	State  State
	Since  time.Time
	Until  time.Time
	Limit  int
}

// Validate enforces the tenant-scoping contract documented above.
// Implementations of [Store.List] MUST call this before issuing the
// underlying query.
func (q Query) Validate() error {
	if q.TenantID != "" && q.AllTenants {
		return ErrQueryScopeConflict
	}
	if q.TenantID == "" && !q.AllTenants {
		return ErrQueryTenantRequired
	}
	return nil
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
	// the same id is a no-op and returns the unchanged request. The
	// original DecidedBy, Reason, and DecidedAt are preserved so a
	// replay or second operator cannot rewrite the audit metadata
	// attached to the first decision.
	//
	// Expiry: if the request is in StatePending and CreatedAt + ttl
	// has passed, Decide first transitions it to StateExpired, then
	// returns [ErrInvalidTransition] — the late approver gets a
	// distinct error so they can communicate the timeout to the
	// requester.
	//
	// Out-of-terminal: Approve/Reject return [ErrInvalidTransition] when
	// the request is in StateExecuted or StateExpired and the requested
	// transition is not idempotent.
	//
	// Approve transitions a StatePending request to StateApproved. Reject
	// transitions to StateRejected. The split (vs. a single
	// `Decide(..., approve bool)` method) keeps the audit trail's verb
	// readable at the call site: `store.Approve(ctx, id, who, why)` reads
	// as the action it performs; `store.Decide(ctx, id, who, why, true)`
	// did not.
	Approve(ctx context.Context, id, decidedBy, reason string) (Request, error)
	Reject(ctx context.Context, id, decidedBy, reason string) (Request, error)

	// MarkExecuted moves a StateApproved request to StateExecuted.
	// Returns [ErrInvalidTransition] for any other source state. The
	// transition is idempotent: a second MarkExecuted on an already-
	// executed request returns the unchanged request rather than
	// erroring, matching the Decide contract.
	MarkExecuted(ctx context.Context, id string) (Request, error)
}

// ValidateForCreate enforces required-field invariants for new requests.
//
// ExpiresAt MUST be set and in the future. Direct store callers can
// otherwise create permanent pending approvals that never auto-expire,
// which defeats the kit's bounded-decision-window invariant.
func ValidateForCreate(r Request, now time.Time) error {
	if !validTenantID(r.TenantID) ||
		!validTextField(r.Actor, MaxActorLen, true) ||
		!validLineField(r.Action, MaxActionLen, true) ||
		!validTextField(r.Resource, MaxResourceLen, false) ||
		!validReason(r.Reason) {
		return ErrInvalidRequest
	}
	if len(r.ID) > MaxIDLen || !requestIDPattern.MatchString(r.ID) {
		return ErrInvalidRequest
	}
	if len(r.Payload) > MaxPayloadSize {
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

// ValidateDecision enforces the shared decider metadata contract used by all
// store implementations before they write decided_by.
func ValidateDecision(decidedBy string) error {
	if !validTextField(decidedBy, MaxActorLen, true) {
		return ErrInvalidApprover
	}
	return nil
}

// ValidateReason enforces the optional approval decision reason contract.
// Reasons are free text, but remain bounded and single-line so audit views
// and logs cannot be flooded or line-spoofed by direct Store callers.
func ValidateReason(reason string) error {
	if !validReason(reason) {
		return ErrInvalidReason
	}
	return nil
}

func validate(r Request, now time.Time) error {
	return ValidateForCreate(r, now)
}

func validTenantID(s string) bool {
	if len(s) > MaxTenantIDLen {
		return false
	}
	return coretenant.ValidateID(s) == nil
}

func validTextField(s string, maxLen int, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validLineField(s string, maxLen int, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validReason(reason string) bool {
	if len(reason) > MaxReasonLen || !utf8.ValidString(reason) {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
