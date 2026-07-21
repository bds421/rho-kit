package actionlog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/redact"
	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
)

// Outcome categorises an action's result. The three values are
// deliberately small and load-bearing: "success" for a verb that ran,
// "failure" for a verb that errored mid-execution, "denied" for a verb
// the system refused to start (authz reject, approval reject, quota
// breach). Forensics treats the three differently — denied entries
// cluster around access-control issues; failure entries around bugs and
// dependency outages.
type Outcome string

// Recognised Outcome values. [Append] rejects any other value via
// [ErrInvalidOutcome] so the canonicalisation contract is preserved
// even when callers construct entries by hand.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeDenied  Outcome = "denied"
)

// Sentinel errors. Callers use errors.Is to branch on these.
var (
	// ErrInvalidEntry is returned by [Append] when a required field is
	// missing or invalid. Callers should treat this as 400 Bad Request
	// rather than 500 — it's a programming error in the caller, not a
	// store error.
	ErrInvalidEntry = errors.New("actionlog: entry is missing required fields")

	// ErrInvalidOutcome is returned when [Entry.Outcome] is not one of
	// the recognised Outcome constants. Returned as a separate sentinel
	// so callers can distinguish "outcome typo" from "missing actor"
	// during incident triage.
	ErrInvalidOutcome = errors.New("actionlog: outcome must be one of success, failure, denied")

	// ErrInvalidStore is returned when a Store or Logger method is
	// invoked on a nil or otherwise uninitialized implementation.
	ErrInvalidStore = errors.New("actionlog: store is not initialized")

	// ErrNotFound is returned by [Get] when the requested ID is not in
	// the store.
	ErrNotFound = errors.New("actionlog: entry not found")

	// ErrSignatureInvalid is returned by [Get] / [List] when an entry's
	// stored signature does not match the recomputed signature. This is
	// the tamper signal — callers should fail closed and surface the
	// error to operators rather than skip the entry.
	ErrSignatureInvalid = errors.New("actionlog: entry signature failed verification")

	// ErrUnknownKeyID is returned when an entry's [Entry.SignatureKeyID]
	// is not resolvable by the [SecretSource]. Treated as tamper
	// distinct from a value mismatch so operators can tell rotation lag
	// from forgery.
	ErrUnknownKeyID = errors.New("actionlog: signature key id is not known to the secret source")

	// ErrSecretTooShort is returned when a [SecretSource] resolves an
	// HMAC key shorter than SHA-256's 32-byte output size. The logger
	// enforces this for every SecretSource implementation, not only
	// [StaticSecrets], so custom KMS/config adapters cannot silently
	// weaken entry signatures.
	ErrSecretTooShort = errors.New("actionlog: signature secret must be at least 32 bytes")

	// ErrChainBroken is returned by [Logger.VerifyChain] when the
	// per-tenant hash chain shows a mid-chain deletion, reordering, or
	// Seq gap. Distinct from [ErrSignatureInvalid] (which catches
	// per-row tampering) because the remediation differs: a chain
	// break implies missing or out-of-order rows in durable storage.
	// Note: pure tail truncation and full-chain deletion still verify
	// cleanly without an external head checkpoint — see VerifyChain.
	ErrChainBroken = errors.New("actionlog: per-tenant hash chain is broken")

	// ErrSecretSourceUnavailable is returned when a [SecretSource]
	// could not respond — e.g. a KMS / Vault / Secrets Manager
	// outage, a deadline-exceeded context, or any other transient
	// provider failure. Distinct from [ErrUnknownKeyID] (which means
	// "the id is genuinely no longer in rotation") so operators can
	// distinguish a permanent integrity break from a temporary
	// dependency outage. Callers should retry on this error rather
	// than treat it as audit-trail corruption.
	ErrSecretSourceUnavailable = errors.New("actionlog: secret source unavailable")

	// ErrQueryTenantRequired is returned by [Logger.List] when the
	// caller passes a [Query] with no [Query.TenantID] and has not
	// opted into [Query.AllTenants]. Cross-tenant listings are valid
	// but dangerous (admin tooling can inadvertently leak audit data
	// across customers); the API requires an explicit opt-in.
	ErrQueryTenantRequired = errors.New("actionlog: query requires TenantID or AllTenants=true")

	// ErrQueryScopeConflict is returned by [Logger.List] when a [Query]
	// sets both [Query.TenantID] and [Query.AllTenants]. Tenant-scoped
	// and cross-tenant reads are intentionally mutually exclusive so
	// privileged callers cannot accidentally hide a wiring bug behind
	// store-specific filter precedence.
	ErrQueryScopeConflict = errors.New("actionlog: query must not set both TenantID and AllTenants=true")

	// ErrLimitTooLarge is returned by [Logger.List] / [Query.Validate]
	// when [Query.Limit] exceeds [MaxPageLimit]. Callers that need
	// more than [MaxPageLimit] entries must follow [Query.Cursor]
	// across pages.
	ErrLimitTooLarge = errors.New("actionlog: query limit exceeds MaxPageLimit")

	// ErrLimitNegative is returned by [Logger.List] / [Query.Validate]
	// when [Query.Limit] is negative. A negative limit's behaviour is
	// Store-specific (the bundled stores treat <= 0 as a default page
	// size; a custom Store could interpret it as "no limit" and stream
	// the entire table). Reject at the API boundary so every Store
	// stays safe without per-impl defensive code.
	ErrLimitNegative = errors.New("actionlog: query limit must not be negative")
)

// MaxPageLimit caps the per-page entries [Query.Limit] may request.
// An admin handler that maps `?limit=1000000000` from a URL parameter
// straight into the query would otherwise force the store to allocate
// huge slices and (for Postgres) emit a giant LIMIT — both wasteful
// and a denial-of-service vector. Callers needing more than this in
// total must page using [Query.Cursor].
const MaxPageLimit = 10_000

// Entry records one agent-attributed action.
//
// Fields are exported so stores can serialise them, but [Logger.Append]
// is the only sanctioned construction path: it assigns ID, OccurredAt,
// Signature, and SignatureKeyID. Callers that build Entry literals (in
// tests, replay tools, off-line verifiers) take responsibility for the
// invariants documented on each field.
type Entry struct {
	// ID is a UUIDv7-shaped identifier assigned by [Logger.Append].
	// Callers may pre-supply for idempotent replay; an empty value is
	// filled in by Append.
	ID string `json:"id"`

	// TenantID scopes the entry to a tenant. Required. Stores reject
	// empty values via [ErrInvalidEntry] because a tenant-less audit
	// record is unusable for multi-tenant forensics.
	TenantID string `json:"tenant_id"`

	// Actor is the principal that performed the action: agent id, user
	// id, service name. Required. Use the actor's stable identifier, not
	// a display name — the log is read in incident timelines where
	// re-resolving a renamed user is a hassle.
	Actor string `json:"actor"`

	// Action is the verb performed. Convention: dotted lowercase scope,
	// e.g. "user.delete", "file.upload", "billing.invoice.void".
	// Required by stores.
	Action string `json:"action"`

	// Resource is the target id or path. May be empty for actions with
	// no single target (e.g. "session.login").
	Resource string `json:"resource,omitempty"`

	// Outcome is one of the three [Outcome] constants. Required.
	Outcome Outcome `json:"outcome"`

	// Reason is freeform context. Convention: populate on Outcome
	// failure or denied; leave empty on success.
	Reason string `json:"reason,omitempty"`

	// Metadata carries structured extras the caller chooses the shape
	// of. Canonical JSON encoding (keys sorted lexicographically;
	// nested maps recursively sorted) is part of the signed payload —
	// see the package doc.
	Metadata map[string]any `json:"metadata,omitempty"`

	// OccurredAt is the wall-clock time the action took place. Stored
	// as UTC. Zero values are filled with the [Logger]'s clock.
	OccurredAt time.Time `json:"occurred_at"`

	// SignatureKeyID identifies which key in the [SecretSource] signed
	// this entry. Required for verification across rotation; stores
	// reject empty values to avoid producing entries that can never be
	// verified after the next rotation.
	SignatureKeyID string `json:"signature_key_id"`

	// Seq is the per-tenant monotonic sequence number assigned by
	// [Logger.Append]. The first entry for a tenant gets Seq=1; each
	// subsequent Append increments. Combined with [Entry.PrevHash], Seq
	// makes the log a hash chain that detects deletion, reordering, and
	// truncation (per-row tampering is already caught by [Entry.Signature]).
	Seq int64 `json:"seq"`

	// PrevHash is the hex-encoded SHA-256 of the previous entry's
	// canonical form for this tenant; the first entry uses 64 zero
	// hex chars. The hash is intentionally key-independent (plain
	// SHA-256, not HMAC) so the chain remains verifiable across
	// signing-key rotation: the signed payload includes PrevHash and
	// Seq, so a row whose predecessor was deleted or rewritten still
	// fails verification because its current signature mismatches
	// once the prev entry's canonical bytes change.
	PrevHash string `json:"prev_hash"`

	// Signature is HMAC-SHA256 over the canonical form of all other
	// fields (including [Entry.Seq] and [Entry.PrevHash]), hex-encoded.
	// Set by [Logger.Append]; verified via [VerifyEntry] or implicitly by
	// [Logger.Get] / [Logger.List].
	Signature string `json:"signature"`
}

// zeroPrevHash is the placeholder used as PrevHash for the first entry
// in a tenant's chain. 64 hex chars (32 bytes) so it is structurally
// indistinguishable from a real SHA-256 hash.
const zeroPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

const minSignatureSecretLen = sha256.Size

// Query controls which entries [Logger.List] returns. Filters compose
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
	// admin / forensics tooling that genuinely needs to see audit
	// data across customers — it bypasses the tenant scoping that
	// the rest of the kit enforces. Mutually exclusive with TenantID.
	AllTenants bool

	// Actor restricts to a single principal.
	Actor string

	// Action restricts to a single verb.
	Action string

	// Since / Until bound by OccurredAt (inclusive on Since, inclusive
	// on Until). Zero value is unbounded.
	Since time.Time
	Until time.Time

	// Limit caps the number of entries returned in one page. Stores
	// apply a default of 100 when Limit <= 0 to bound query cost. The
	// list's total size is bounded by [Cursor] pagination, not by
	// Limit — callers must follow the returned next cursor to read
	// all entries.
	Limit int

	// Cursor is an opaque page marker returned by a previous call to
	// [Logger.List]. Empty cursor reads from the head; opaque format
	// is implementation-defined and verified by [CursorSigner.Decode]. A
	// malformed cursor surfaces [ErrInvalidCursor].
	Cursor string
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
	if q.Limit < 0 {
		return ErrLimitNegative
	}
	if q.Limit > MaxPageLimit {
		return ErrLimitTooLarge
	}
	return nil
}

// ValidateStoredEntry enforces the low-level store append contract shared by
// the bundled stores and custom Store implementations. tenantID is the chain
// being extended; e.TenantID must match it so a caller cannot take a lock for
// one tenant and persist an entry under another.
func ValidateStoredEntry(tenantID string, e Entry) error {
	if tenantID == "" || e.TenantID != tenantID {
		return ErrInvalidEntry
	}
	return validate(e)
}

// Logger appends and reads entries, signing on write and verifying on
// read. The chain-aware methods live on the interface; the stateless
// per-entry primitives ([SignEntry], [VerifyEntry]) are package-level
// free functions because they only depend on the [SecretSource], not
// on a Store. Off-band tools (forensics, replay, audit dumps) can
// verify entries without constructing a Logger or wiring a Store.
type Logger interface {
	// Append persists the entry. Fills in ID, OccurredAt, Signature,
	// SignatureKeyID, Seq, and PrevHash; returns the populated entry.
	// Errors:
	//   - [ErrInvalidEntry]   – missing required field
	//   - [ErrInvalidOutcome] – Outcome not in the recognised set
	//   - any store-level error wrapping the cause
	Append(ctx context.Context, e Entry) (Entry, error)

	// Get returns the entry with the given id and verifies its
	// signature. Returns [ErrNotFound] if absent, [ErrSignatureInvalid]
	// if the signature doesn't match, [ErrUnknownKeyID] if the entry's
	// key id is no longer resolvable.
	Get(ctx context.Context, id string) (Entry, error)

	// List returns the next page of entries matching q, signature-verified.
	// Tampered entries surface [ErrSignatureInvalid] rather than being
	// silently skipped — a partial result that hides forgeries is worse
	// than no result. The returned cursor is empty when the page is the
	// last one; otherwise it is the opaque marker callers feed back via
	// [Query.Cursor] to retrieve the next page.
	List(ctx context.Context, q Query) ([]Entry, string, error)

	// VerifyChain streams every entry for tenantID in chronological
	// order (Seq ascending) and verifies that:
	//   - each entry's Signature is valid (catches per-row tampering),
	//   - each entry's PrevHash equals the previous entry's hash
	//     (catches mid-chain deletion / reordering),
	//   - Seq starts at 1 and increases by 1 each step (catches
	//     mid-chain gaps and inserted rows).
	//
	// Tail truncation (deleting the newest N rows) and whole-chain
	// deletion leave a still-consistent remaining prefix (or empty
	// set) and therefore return nil without an external head anchor.
	// Operators who need to detect those cases must checkpoint the
	// latest (Seq, entryHash) out-of-band and compare after verify.
	// Returns nil on success, [ErrChainBroken] / [ErrSignatureInvalid]
	// / [ErrUnknownKeyID] on detection.
	VerifyChain(ctx context.Context, tenantID string) error
}

// Store is the persistence interface implemented by backends in
// data/actionlog/memory and data/actionlog/postgres. The store does
// not concern itself with signing — [Logger] computes the signature
// before calling [Store.AppendChained] and verifies on the read path.
type Store interface {
	// AppendChained persists a fully-populated entry under a
	// per-tenant lock (memory: per-tenant mutex; postgres: SELECT
	// FOR UPDATE on the latest row). The supplied build callback
	// receives the previous entry (or zero Entry + Seq=0 if this is
	// the tenant's first entry) and must return a fully-signed entry
	// to persist. Implementations MUST hold the tenant lock across
	// build + persist so that concurrent Appends produce monotonic
	// Seq and an unbroken hash chain.
	AppendChained(ctx context.Context, tenantID string, build func(prev Entry, prevSeq int64) (Entry, error)) (Entry, error)

	// Get returns the entry by id, or [ErrNotFound] if absent.
	// The returned Entry is caller-owned: Metadata and other mutable
	// fields MUST NOT alias the store's internal state (deep-clone or
	// re-decode before return). Logger.Get does not re-clone.
	Get(ctx context.Context, id string) (Entry, error)

	// List returns entries matching q, ordered by OccurredAt
	// descending, then ID descending for stable ordering when
	// timestamps tie. Implementations honour [Query.Cursor] via
	// keyset pagination on (OccurredAt, ID) so total list size is
	// bounded by the caller's cursor follow, not by [Query.Limit].
	// The returned cursor is empty when no more rows match.
	// Each returned Entry is caller-owned (same detachment contract as Get).
	List(ctx context.Context, q Query) ([]Entry, string, error)

	// RangeByTenantSeq calls fn for every entry for tenantID in Seq ASC
	// order. Used by [Logger.VerifyChain]. Implementations should stream
	// entries where the backend supports it so long tenant chains do not
	// have to be materialized as []Entry before verification can start.
	//
	// If fn returns an error, iteration must stop and return that error.
	RangeByTenantSeq(ctx context.Context, tenantID string, fn func(Entry) error) error
}

// SecretSource resolves HMAC secrets by key id. Implementations
// typically read from a config-managed map or a secret manager. Use
// [StaticSecrets] for the common case of a small in-process map.
//
// The ctx-and-error signature is the v2 production-shape: KMS / Vault
// / Secrets Manager adapters need to honour deadlines, report
// dependency failures, and distinguish "unknown key" from "manager
// unavailable". Implementations must return:
//
//   - [ErrUnknownKeyID] when the id is genuinely not in the keyring
//     (permanent — rotation lag or deletion).
//   - [ErrSecretSourceUnavailable] (or an error wrapping it) when the
//     backing provider failed transiently. Callers retry on this.
//   - ctx.Err() (via [errors.Is](err, context.Canceled) /
//     context.DeadlineExceeded) when the context is cancelled or
//     deadline-exceeded before the provider responds.
//
// [StaticSecrets] is the simple in-memory implementation; it always
// returns [ErrUnknownKeyID] for unknown ids and never returns
// transient errors.
type SecretSource interface {
	// CurrentKeyID returns the id of the key new entries should be
	// signed with. Rotation works by changing the value this returns
	// while keeping older ids resolvable.
	CurrentKeyID(ctx context.Context) (string, error)

	// Resolve returns the secret bytes for the given key id. See the
	// interface docstring for the typed-error contract.
	Resolve(ctx context.Context, keyID string) ([]byte, error)
}

// StaticSecrets is the simple SecretSource backed by an in-memory map.
// The map is captured by value at construction; later mutation does
// not affect resolution.
type StaticSecrets struct {
	current string
	keys    map[string][]byte
}

// NewStaticSecrets builds a [StaticSecrets] with the given current key
// id and key map. Panics if:
//   - currentKeyID is empty (audit FR-050: empty key IDs persisted as
//     SignatureKeyID="" produce entries that fail [Verify] forever
//     because Verify treats "" as ErrSignatureInvalid).
//   - currentKeyID is not present in keys.
//   - any key id is empty (same forge-protection rationale).
//   - any key is shorter than 32 bytes (HMAC-SHA256 requires at least
//     the hash output size to retain its security guarantees).
func NewStaticSecrets(currentKeyID string, keys map[string][]byte) *StaticSecrets {
	if currentKeyID == "" {
		panic("actionlog: NewStaticSecrets currentKeyID must not be empty (would persist unverifiable entries)")
	}
	if _, ok := keys[currentKeyID]; !ok {
		panic("actionlog: NewStaticSecrets currentKeyID is not in keys map")
	}
	dup := make(map[string][]byte, len(keys))
	for id, k := range keys {
		if id == "" {
			panic("actionlog: NewStaticSecrets empty key id is not allowed")
		}
		if len(k) < minSignatureSecretLen {
			panic("actionlog: NewStaticSecrets secret must be at least 32 bytes")
		}
		buf := make([]byte, len(k))
		copy(buf, k)
		dup[id] = buf
	}
	return &StaticSecrets{current: currentKeyID, keys: dup}
}

// CurrentKeyID returns the configured current key id. The in-memory
// source never fails so the returned error is always nil; the signature
// stays ctx-and-error for [SecretSource] conformance.
func (s *StaticSecrets) CurrentKeyID(context.Context) (string, error) {
	if s == nil {
		return "", nil
	}
	return s.current, nil
}

// Resolve returns a defensive copy of the secret for keyID. Returning
// the underlying slice would let a caller mutate the stored key (and
// thereby break or forge subsequent verification/signing) — the copy
// keeps the in-memory map immutable from the outside. Returns
// [ErrUnknownKeyID] if the id is not in the map.
func (s *StaticSecrets) Resolve(_ context.Context, keyID string) ([]byte, error) {
	if s == nil || s.keys == nil {
		return nil, ErrUnknownKeyID
	}
	k, ok := s.keys[keyID]
	if !ok {
		return nil, ErrUnknownKeyID
	}
	out := make([]byte, len(k))
	copy(out, k)
	return out, nil
}

func resolveSignatureSecret(ctx context.Context, source SecretSource, keyID string) ([]byte, error) {
	if keyID == "" {
		return nil, ErrUnknownKeyID
	}
	secret, err := source.Resolve(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if len(secret) < minSignatureSecretLen {
		return nil, ErrSecretTooShort
	}
	return secret, nil
}

// cachingSecrets is a per-call memoizing decorator around a
// [SecretSource]. A single [Logger.List] page (up to [MaxPageLimit]
// rows) or a full [Logger.VerifyChain] sweep verifies many entries that
// almost always share one keyID; without memoization that is one
// [SecretSource.Resolve] per row, i.e. one KMS/Vault round-trip per row
// for remote-backed sources. Wrapping the source for the duration of the
// call collapses those to one Resolve per distinct keyID.
//
// Resolve results (both the secret bytes and any error) are cached by
// keyID; errors are cached too so a permanently-unknown key does not
// re-hit the backing provider for every row that references it. Each
// Resolve still returns a fresh copy of the bytes so callers cannot
// mutate the cached secret, preserving the [SecretSource] contract.
//
// It is NOT safe for concurrent use; it is only ever constructed and
// consumed within one single-goroutine List/VerifyChain call.
type cachingSecrets struct {
	source SecretSource
	cache  map[string]resolveResult
}

type resolveResult struct {
	secret []byte
	err    error
}

func newCachingSecrets(source SecretSource) *cachingSecrets {
	return &cachingSecrets{source: source, cache: map[string]resolveResult{}}
}

func (c *cachingSecrets) CurrentKeyID(ctx context.Context) (string, error) {
	return c.source.CurrentKeyID(ctx)
}

func (c *cachingSecrets) Resolve(ctx context.Context, keyID string) ([]byte, error) {
	if r, ok := c.cache[keyID]; ok {
		return copyBytes(r.secret), r.err
	}
	secret, err := c.source.Resolve(ctx, keyID)
	c.cache[keyID] = resolveResult{secret: secret, err: err}
	return copyBytes(secret), err
}

func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// signedLogger is the default [Logger] implementation: a [Store] plus a
// [SecretSource] plus a clock and id-source for testability.
type signedLogger struct {
	store   Store
	secrets SecretSource
	clock   clock.Func
	newID   func() (string, error)
}

// LoggerOption configures a [Logger] returned by [New].
type LoggerOption func(*signedLogger)

// WithClock overrides the wall-clock used for [Entry.OccurredAt]. Used
// by tests to make signed payloads deterministic. Panics on nil so a
// misconfigured test option does not turn into a production panic on
// the first Append.
func WithClock(fn clock.Func) LoggerOption {
	if fn == nil {
		panic("actionlog: WithClock fn must not be nil")
	}
	return func(l *signedLogger) { l.clock = fn }
}

// WithIDFunc overrides the id generator. Default: UUIDv7 string.
// Panics on nil — see [WithClock].
func WithIDFunc(fn func() string) LoggerOption {
	if fn == nil {
		panic("actionlog: WithIDFunc fn must not be nil")
	}
	return func(l *signedLogger) {
		l.newID = func() (string, error) {
			return fn(), nil
		}
	}
}

// WithIDFuncE overrides the id generator with an error-returning source.
// Use this when IDs come from a dependency that can fail. Panics on nil.
func WithIDFuncE(fn func() (string, error)) LoggerOption {
	if fn == nil {
		panic("actionlog: WithIDFuncE fn must not be nil")
	}
	return func(l *signedLogger) { l.newID = fn }
}

// New returns a [Logger] backed by store + secrets. Panics if either
// is nil — both are programming errors that would otherwise defer the
// failure to the first Append call.
func New(store Store, secrets SecretSource, opts ...LoggerOption) Logger {
	if store == nil {
		panic("actionlog: New store must not be nil")
	}
	if secrets == nil {
		panic("actionlog: New secrets must not be nil")
	}
	l := &signedLogger{
		store:   store,
		secrets: secrets,
		clock:   time.Now,
		newID: func() (string, error) {
			return id.New(), nil
		},
	}
	for _, o := range opts {
		if o == nil {
			panic("actionlog: New option must not be nil")
		}
		o(l)
	}
	return l
}

func (l *signedLogger) ready() error {
	if l == nil || l.store == nil || l.secrets == nil || l.clock == nil || l.newID == nil {
		return ErrInvalidStore
	}
	return nil
}

// Append validates, signs, and persists the entry. The signature
// covers Seq and PrevHash so deletion / reordering / truncation of
// rows in the durable store breaks the chain on the next
// VerifyChain. Concurrent Appends for the same tenant serialise
// inside the store's per-tenant lock.
func (l *signedLogger) Append(ctx context.Context, e Entry) (Entry, error) {
	if err := l.ready(); err != nil {
		return Entry{}, err
	}
	if !validMetadata(e.Metadata) {
		return Entry{}, ErrInvalidEntry
	}
	e = cloneEntry(e)
	if e.TenantID == "" {
		return Entry{}, ErrInvalidEntry
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = l.clock()
	}
	// Truncate to microsecond precision before signing: TIMESTAMPTZ-
	// backed stores (Postgres via pgx) persist OccurredAt at µs
	// granularity, but canonicalForm signs it as RFC3339Nano. Without
	// this, a ns-precision clock (time.Now on Linux) would sign a value
	// the durable store reads back differently, so Get / List /
	// VerifyChain would reject ~every entry with ErrSignatureInvalid.
	// Applies uniformly across all stores so the in-memory and Postgres
	// stores agree on the signed canonical form.
	e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)

	keyID, err := l.secrets.CurrentKeyID(ctx)
	if err != nil {
		return Entry{}, redact.WrapError("actionlog: resolve current key id", err)
	}
	if keyID == "" {
		// FR-050 [HIGH] belt-and-suspenders: NewStaticSecrets panics
		// on empty current key id, but a custom Secrets implementation
		// could still return "". Reject here so we never persist an
		// entry whose SignatureKeyID Verify will reject permanently.
		return Entry{}, redact.WrapError("actionlog: Secrets.CurrentKeyID returned empty string", ErrUnknownKeyID)
	}
	secret, err := resolveSignatureSecret(ctx, l.secrets, keyID)
	if err != nil {
		return Entry{}, redact.WrapError("actionlog: current key id", err)
	}

	entry, err := l.store.AppendChained(ctx, e.TenantID, func(prev Entry, prevSeq int64) (Entry, error) {
		entry := e
		if entry.ID == "" {
			id, err := l.newID()
			if err != nil {
				return Entry{}, err
			}
			entry.ID = id
		}
		entry.SignatureKeyID = keyID
		entry.Seq = prevSeq + 1
		if prevSeq == 0 {
			entry.PrevHash = zeroPrevHash
		} else {
			h, err := entryHash(prev)
			if err != nil {
				return Entry{}, redact.WrapError("actionlog: prev hash", err)
			}
			entry.PrevHash = h
		}
		if err := validateExceptMetadata(entry); err != nil {
			return Entry{}, err
		}
		sig, err := computeSignature(entry, secret)
		if err != nil {
			return Entry{}, redact.WrapError("actionlog: compute signature", err)
		}
		entry.Signature = sig
		return entry, nil
	})
	if err != nil {
		return Entry{}, err
	}
	return cloneEntry(entry), nil
}

// Get reads and verifies an entry.
func (l *signedLogger) Get(ctx context.Context, id string) (Entry, error) {
	if err := l.ready(); err != nil {
		return Entry{}, err
	}
	e, err := l.store.Get(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	// Store.Get returns a caller-owned Entry (see Store interface); do not
	// re-clone — that doubled reflection-based metadata copies on the read path.
	if err := VerifyEntry(ctx, e, l.secrets); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// List reads and verifies a batch of entries. Rejects queries that lack
// a TenantID and have not opted into AllTenants, or that specify both
// scope modes — see [Query]. The first verification failure aborts the
// call so callers don't get a half-truthful page. Returns the next
// page cursor (empty when the page is the last one).
func (l *signedLogger) List(ctx context.Context, q Query) ([]Entry, string, error) {
	if err := l.ready(); err != nil {
		return nil, "", err
	}
	if err := q.Validate(); err != nil {
		return nil, "", err
	}
	entries, next, err := l.store.List(ctx, q)
	if err != nil {
		return nil, "", err
	}
	// Memoize secret resolution for the page: entries in one page almost
	// always share a keyID, so this collapses up to MaxPageLimit Resolve
	// calls (one KMS/Vault round-trip each, for remote sources) into one
	// per distinct keyID. Semantics are unchanged for static sources.
	secrets := newCachingSecrets(l.secrets)
	out := make([]Entry, len(entries))
	for i, e := range entries {
		// Store.List returns caller-owned entries; skip the second deep-clone.
		if err := VerifyEntry(ctx, e, secrets); err != nil {
			return nil, "", redact.WrapError("actionlog: entry verification failed", err)
		}
		out[i] = e
	}
	return out, next, nil
}

// VerifyChain streams the per-tenant chain and reports mid-chain
// deletion, reordering, Seq gaps, or row tampering. Tail truncation
// and empty chains still return nil (see Logger.VerifyChain docs).
func (l *signedLogger) VerifyChain(ctx context.Context, tenantID string) error {
	if err := l.ready(); err != nil {
		return err
	}
	if tenantID == "" {
		return ErrQueryTenantRequired
	}
	var prev Entry
	var wantSeq int64 = 1
	// Memoize secret resolution for the whole chain sweep: a tenant chain
	// is signed under one keyID (or a handful across rotations), so this
	// turns one Resolve per row into one per distinct keyID. The callback
	// runs sequentially, so the (unsynchronized) cache is safe here.
	secrets := newCachingSecrets(l.secrets)
	return l.store.RangeByTenantSeq(ctx, tenantID, func(e Entry) error {
		if err := VerifyEntry(ctx, e, secrets); err != nil {
			return redact.WrapError("actionlog: entry verification failed", err)
		}
		if e.Seq != wantSeq {
			return fmt.Errorf("%w: expected seq %d, got %d", ErrChainBroken, wantSeq, e.Seq)
		}
		if wantSeq == 1 {
			if e.PrevHash != zeroPrevHash {
				return fmt.Errorf("%w: first entry must have zero prev_hash", ErrChainBroken)
			}
		} else {
			expected, err := entryHash(prev)
			if err != nil {
				return err
			}
			if e.PrevHash != expected {
				return fmt.Errorf("%w: seq %d prev_hash mismatch", ErrChainBroken, e.Seq)
			}
		}
		prev = e
		wantSeq++
		return nil
	})
}

// SignEntry computes and returns both the canonical signature and the
// resolved key id for an entry without persisting it. Useful for off-band
// tools that need to sign without constructing a Logger / Store pair.
//
// e is taken by value and is NOT mutated. The returned signature is
// computed against the resolved keyID, so callers must apply BOTH return
// values to their entry — set e.Signature = signature and
// e.SignatureKeyID = keyID — before persisting or verifying. Applying only
// the signature leaves SignatureKeyID empty and [VerifyEntry] will reject
// it. This mirrors [Logger.Append], which fills both fields.
//
// Returns [ErrUnknownKeyID] when [SecretSource.CurrentKeyID] is empty.
// Returns [ErrSecretTooShort] when the resolved secret is shorter than
// [minSignatureSecretLen]. The ctx is passed through to the
// [SecretSource] for deadline / cancel propagation; KMS/Vault-backed
// sources should honour it.
func SignEntry(ctx context.Context, e Entry, secrets SecretSource) (signature, keyID string, err error) {
	if secrets == nil {
		return "", "", ErrInvalidStore
	}
	if !validMetadata(e.Metadata) {
		return "", "", ErrInvalidEntry
	}
	keyID, err = secrets.CurrentKeyID(ctx)
	if err != nil {
		return "", "", redact.WrapError("actionlog: resolve current key id", err)
	}
	if keyID == "" {
		return "", "", redact.WrapError("actionlog: Secrets.CurrentKeyID returned empty string", ErrUnknownKeyID)
	}
	secret, err := resolveSignatureSecret(ctx, secrets, keyID)
	if err != nil {
		return "", "", redact.WrapError("actionlog: current key id", err)
	}
	// Match Append: truncate to microsecond so off-band SignEntry
	// signatures verify against stores that persist TIMESTAMPTZ at µs.
	e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)
	e.SignatureKeyID = keyID
	sig, err := computeSignature(e, secret)
	if err != nil {
		return "", "", err
	}
	return sig, keyID, nil
}

// VerifyEntry reports whether the entry's stored signature matches the
// recomputed canonical signature. Returns nil on match,
// [ErrSignatureInvalid] / [ErrUnknownKeyID] /
// [ErrSecretSourceUnavailable] otherwise.
//
// Like [SignEntry], this is stateless — verifiers can validate a dump
// of entries without a Logger or Store. The implementation uses a
// fixed-size buffer for the constant-time compare so a valid-hex but
// wrong-length stored signature does not take a faster code path than
// a same-length forgery attempt (FR-052 [LOW]). The ctx is passed
// through to the [SecretSource] for deadline / cancel propagation.
func VerifyEntry(ctx context.Context, e Entry, secrets SecretSource) error {
	if secrets == nil {
		return ErrInvalidStore
	}
	if !validMetadata(e.Metadata) {
		return ErrInvalidEntry
	}
	if e.SignatureKeyID == "" {
		return ErrSignatureInvalid
	}
	secret, err := resolveSignatureSecret(ctx, secrets, e.SignatureKeyID)
	if err != nil {
		return err
	}
	expected, err := computeSignature(e, secret)
	if err != nil {
		return err
	}
	gotRaw, err := hex.DecodeString(e.Signature)
	if err != nil {
		return ErrSignatureInvalid
	}
	want, err := hex.DecodeString(expected)
	if err != nil {
		return ErrSignatureInvalid
	}
	var got [sha256.Size]byte
	if len(gotRaw) == sha256.Size {
		copy(got[:], gotRaw)
	}
	if !hmac.Equal(got[:], want) {
		return ErrSignatureInvalid
	}
	return nil
}

// MaxIDLen is the inclusive upper bound on Entry.ID accepted by Logger.Append
// (audit FR-051). The Postgres schema declares id VARCHAR(36), so the kit
// validates at the package boundary rather than letting the database surface
// the failure late and make integration tests harder to debug.
const MaxIDLen = 36

// Entry field length caps mirror the Postgres schema so Logger.Append has the
// same contract regardless of the configured store implementation.
const (
	MaxTenantIDLen       = 255
	MaxActorLen          = 255
	MaxActionLen         = 255
	MaxResourceLen       = 500
	MaxReasonLen         = 4096
	MaxSignatureKeyIDLen = 64
)

// validate enforces required-field invariants before signing.
//
// FR-051 [MED]: ID length is capped at [MaxIDLen] (36, matching the
// VARCHAR(36) declared in the Postgres migration). Pre-fix only
// emptiness was checked, so a too-long ID would pass the in-memory
// store and fail at INSERT time in Postgres with a low-value error.
// The same FR-051 rationale extends to invalid UTF-8 / control bytes:
// a caller-supplied replay ID with those bytes is signed and folded
// into signed cursor payloads, then fails late at the Postgres INSERT
// (invalid byte sequence) — so [validID] rejects them at the boundary
// like every other caller-supplied text field.
func validate(e Entry) error {
	if err := validateExceptMetadata(e); err != nil {
		return err
	}
	if !validMetadata(e.Metadata) {
		return ErrInvalidEntry
	}
	return nil
}

// validateExceptMetadata is used on the Append hot path after
// validMetadata has already run outside the per-tenant lock, so we do
// not re-marshal Metadata (up to 8 KiB) while holding the lock.
func validateExceptMetadata(e Entry) error {
	if e.ID == "" {
		return ErrInvalidEntry
	}
	// Length before character scan so multi-MiB IDs fail fast without
	// a full UTF-8 walk (and keep the dedicated too-long diagnostic).
	if len(e.ID) > MaxIDLen {
		return fmt.Errorf("%w: ID exceeds maximum length", ErrInvalidEntry)
	}
	if !validID(e.ID) ||
		!validTenantID(e.TenantID) ||
		!validTextField(e.Actor, MaxActorLen, true) ||
		!validTextField(e.Action, MaxActionLen, true) ||
		!validTextField(e.Resource, MaxResourceLen, false) ||
		!validTextField(e.SignatureKeyID, MaxSignatureKeyIDLen, true) ||
		!validReason(e.Reason) {
		return ErrInvalidEntry
	}
	switch e.Outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomeDenied:
	default:
		return ErrInvalidOutcome
	}
	return nil
}

// validID checks the character-level invariants for a caller-supplied
// [Entry.ID]: valid UTF-8 with no control or whitespace runes, matching
// the checks applied to every other bounded text field. Length is
// enforced separately by [validate] so the too-long case keeps its own
// diagnostic error. Empty is also handled by [validate]; treat it as
// valid here so emptiness reports through the dedicated branch.
func validID(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
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

func validFreeText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, '\x00')
}

// validReason rejects control characters (newline/ANSI/ESC) so freeform
// audit reasons cannot line-spoof forensic views or terminal renderers.
// Spaces are allowed (unlike Actor/Action) because reasons are prose.
func validReason(s string) bool {
	if len(s) > MaxReasonLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// computeSignature builds the canonical form and HMAC-SHA256s it.
func computeSignature(e Entry, secret []byte) (string, error) {
	canonical, err := canonicalForm(e)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// entryHash returns the hex-encoded SHA-256 of the entry's canonical
// form. Used to compute the next entry's PrevHash and to verify the
// chain.
//
// Why plain SHA-256 (not HMAC) and key-independent: the prev_hash
// participates in the next entry's signed canonical form, so any
// change to a previous entry's bytes (including its own prev_hash)
// invalidates the next entry's signature. The chain's tamper evidence
// rides on the per-row HMAC signatures — making prev_hash itself
// key-free means a key rotation between two entries does not break
// VerifyChain. With an HMAC-keyed prev_hash, the signed prev_hash on
// entry N (computed under the new key) would not match the
// re-derived hash of entry N-1 (recomputed under entry N-1's older
// key), causing a false ErrChainBroken on a perfectly valid log.
func entryHash(e Entry) (string, error) {
	canonical, err := canonicalForm(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
