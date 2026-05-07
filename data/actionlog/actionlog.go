package actionlog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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

	// ErrChainBroken is returned by [Logger.VerifyChain] when the
	// per-tenant hash chain shows a deletion, reordering, or
	// truncation. Distinct from [ErrSignatureInvalid] (which catches
	// per-row tampering) because the remediation differs: a chain
	// break implies missing or out-of-order rows in durable storage.
	ErrChainBroken = errors.New("actionlog: per-tenant hash chain is broken")

	// ErrQueryTenantRequired is returned by [Logger.List] when the
	// caller passes a [Query] with no [Query.TenantID] and has not
	// opted into [Query.AllTenants]. Cross-tenant listings are valid
	// but dangerous (admin tooling can inadvertently leak audit data
	// across customers); the API requires an explicit opt-in.
	ErrQueryTenantRequired = errors.New("actionlog: query requires TenantID or AllTenants=true")
)

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

	// PrevHash is the hex-encoded HMAC-SHA256 of the previous entry's
	// canonical form for this tenant; the first entry uses 64 zero
	// hex chars. The signed payload includes PrevHash and Seq, so a
	// row whose predecessor was deleted will fail verification because
	// its recomputed signature mismatches the stored value.
	PrevHash string `json:"prev_hash"`

	// Signature is HMAC-SHA256 over the canonical form of all other
	// fields (including [Entry.Seq] and [Entry.PrevHash]), hex-encoded.
	// Set by [Logger.Append]; read via [Logger.Verify] or implicitly by
	// [Logger.Get] / [Logger.List].
	Signature string `json:"signature"`
}

// zeroPrevHash is the placeholder used as PrevHash for the first entry
// in a tenant's chain. 64 hex chars (32 bytes) so it is structurally
// indistinguishable from a real SHA-256 hash.
const zeroPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Query controls which entries [Logger.List] returns. Filters compose
// with AND semantics; an empty filter field is unconstrained. The
// caller MUST set either [Query.TenantID] (single-tenant query) or
// [Query.AllTenants]=true (explicit cross-tenant query); a zero query
// is rejected with [ErrQueryTenantRequired].
type Query struct {
	// TenantID restricts to a single tenant. Required unless
	// AllTenants is true.
	TenantID string

	// AllTenants opts into a cross-tenant listing. Set this only on
	// admin / forensics tooling that genuinely needs to see audit
	// data across customers — it bypasses the tenant scoping that
	// the rest of the kit enforces. Ignored when TenantID is set.
	AllTenants bool

	// Actor restricts to a single principal.
	Actor string

	// Action restricts to a single verb.
	Action string

	// Since / Until bound by OccurredAt (inclusive on Since, inclusive
	// on Until). Zero value is unbounded.
	Since time.Time
	Until time.Time

	// Limit caps the number of entries returned. Stores apply a
	// default of 100 when Limit <= 0 to bound query cost.
	Limit int
}

// Logger appends and reads entries, signing on write and verifying on
// read.
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

	// List returns entries matching q, signature-verified. Tampered
	// entries surface [ErrSignatureInvalid] rather than being silently
	// skipped — a partial result that hides forgeries is worse than no
	// result.
	List(ctx context.Context, q Query) ([]Entry, error)

	// Sign returns the canonical signature for the entry without
	// touching the store. Useful for off-band verification tools and
	// for tests.
	Sign(e Entry) (signature, keyID string, err error)

	// Verify reports whether the entry's stored signature matches the
	// recomputed canonical signature. Returns nil on match;
	// [ErrSignatureInvalid] / [ErrUnknownKeyID] otherwise.
	Verify(e Entry) error

	// VerifyChain walks every entry for tenantID in chronological
	// order (Seq ascending) and verifies that:
	//   - each entry's Signature is valid (catches per-row tampering),
	//   - each entry's PrevHash equals the previous entry's hash
	//     (catches deletion / reordering),
	//   - Seq starts at 1 and increases by 1 each step (catches
	//     truncation and inserted rows).
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
	Get(ctx context.Context, id string) (Entry, error)

	// List returns entries matching q, ordered by OccurredAt
	// descending, then ID descending for stable ordering when
	// timestamps tie.
	List(ctx context.Context, q Query) ([]Entry, error)

	// ListByTenantSeq returns every entry for tenantID in Seq ASC
	// order. Used by [Logger.VerifyChain]; not subject to the default
	// limit because the verification needs to walk the full chain.
	ListByTenantSeq(ctx context.Context, tenantID string) ([]Entry, error)
}

// SecretSource resolves HMAC secrets by key id. Implementations
// typically read from a config-managed map or a secret manager. Use
// [StaticSecrets] for the common case of a small in-process map.
type SecretSource interface {
	// CurrentKeyID returns the id of the key new entries should be
	// signed with. Rotation works by changing the value this returns
	// while keeping older ids resolvable.
	CurrentKeyID() string

	// Resolve returns the secret bytes for the given key id, or false
	// if the id is no longer known. Returning false produces
	// [ErrUnknownKeyID] on read.
	Resolve(keyID string) ([]byte, bool)
}

// StaticSecrets is the simple SecretSource backed by an in-memory map.
// The map is captured by value at construction; later mutation does
// not affect resolution.
type StaticSecrets struct {
	current string
	keys    map[string][]byte
}

// NewStaticSecrets builds a [StaticSecrets] with the given current key
// id and key map. Panics if currentKeyID is not present in keys, or
// if any key is shorter than 32 bytes (HMAC-SHA256 requires at least
// the hash output size to retain its security guarantees).
func NewStaticSecrets(currentKeyID string, keys map[string][]byte) *StaticSecrets {
	if _, ok := keys[currentKeyID]; !ok {
		panic("actionlog: NewStaticSecrets: currentKeyID is not in keys map")
	}
	dup := make(map[string][]byte, len(keys))
	for id, k := range keys {
		if len(k) < 32 {
			panic("actionlog: NewStaticSecrets: secret for key id " + id + " must be at least 32 bytes")
		}
		buf := make([]byte, len(k))
		copy(buf, k)
		dup[id] = buf
	}
	return &StaticSecrets{current: currentKeyID, keys: dup}
}

// CurrentKeyID returns the configured current key id.
func (s *StaticSecrets) CurrentKeyID() string { return s.current }

// Resolve returns a defensive copy of the secret for keyID. Returning
// the underlying slice would let a caller mutate the stored key (and
// thereby break or forge subsequent verification/signing) — the copy
// keeps the in-memory map immutable from the outside.
func (s *StaticSecrets) Resolve(keyID string) ([]byte, bool) {
	k, ok := s.keys[keyID]
	if !ok {
		return nil, false
	}
	out := make([]byte, len(k))
	copy(out, k)
	return out, true
}

// signedLogger is the default [Logger] implementation: a [Store] plus a
// [SecretSource] plus a clock and id-source for testability.
type signedLogger struct {
	store   Store
	secrets SecretSource
	clock   func() time.Time
	newID   func() string
}

// LoggerOption configures a [Logger] returned by [New].
type LoggerOption func(*signedLogger)

// WithClock overrides the wall-clock used for [Entry.OccurredAt]. Used
// by tests to make signed payloads deterministic.
func WithClock(fn func() time.Time) LoggerOption {
	return func(l *signedLogger) { l.clock = fn }
}

// WithIDFunc overrides the id generator. Default: UUIDv7 string.
func WithIDFunc(fn func() string) LoggerOption {
	return func(l *signedLogger) { l.newID = fn }
}

// New returns a [Logger] backed by store + secrets. Panics if either
// is nil — both are programming errors that would otherwise defer the
// failure to the first Append call.
func New(store Store, secrets SecretSource, opts ...LoggerOption) Logger {
	if store == nil {
		panic("actionlog: New: store must not be nil")
	}
	if secrets == nil {
		panic("actionlog: New: secrets must not be nil")
	}
	l := &signedLogger{
		store:   store,
		secrets: secrets,
		clock:   time.Now,
		newID:   func() string { return uuid.Must(uuid.NewV7()).String() },
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Append validates, signs, and persists the entry. The signature
// covers Seq and PrevHash so deletion / reordering / truncation of
// rows in the durable store breaks the chain on the next
// VerifyChain. Concurrent Appends for the same tenant serialise
// inside the store's per-tenant lock.
func (l *signedLogger) Append(ctx context.Context, e Entry) (Entry, error) {
	if e.TenantID == "" {
		return Entry{}, ErrInvalidEntry
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = l.clock()
	}
	e.OccurredAt = e.OccurredAt.UTC()

	keyID := l.secrets.CurrentKeyID()
	secret, ok := l.secrets.Resolve(keyID)
	if !ok {
		return Entry{}, fmt.Errorf("actionlog: current key id %q not resolvable: %w", keyID, ErrUnknownKeyID)
	}

	return l.store.AppendChained(ctx, e.TenantID, func(prev Entry, prevSeq int64) (Entry, error) {
		entry := e
		if entry.ID == "" {
			entry.ID = l.newID()
		}
		entry.SignatureKeyID = keyID
		entry.Seq = prevSeq + 1
		if prevSeq == 0 {
			entry.PrevHash = zeroPrevHash
		} else {
			h, err := entryHash(prev, secret)
			if err != nil {
				return Entry{}, fmt.Errorf("actionlog: prev hash: %w", err)
			}
			entry.PrevHash = h
		}
		if err := validate(entry); err != nil {
			return Entry{}, err
		}
		sig, err := computeSignature(entry, secret)
		if err != nil {
			return Entry{}, fmt.Errorf("actionlog: compute signature: %w", err)
		}
		entry.Signature = sig
		return entry, nil
	})
}

// Get reads and verifies an entry.
func (l *signedLogger) Get(ctx context.Context, id string) (Entry, error) {
	e, err := l.store.Get(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if err := l.Verify(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// List reads and verifies a batch of entries. Rejects queries that
// lack a TenantID and have not opted into AllTenants — see [Query].
// The first verification failure aborts the call so callers don't
// get a half-truthful page.
func (l *signedLogger) List(ctx context.Context, q Query) ([]Entry, error) {
	if q.TenantID == "" && !q.AllTenants {
		return nil, ErrQueryTenantRequired
	}
	entries, err := l.store.List(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := l.Verify(e); err != nil {
			return nil, fmt.Errorf("actionlog: entry %q: %w", e.ID, err)
		}
	}
	return entries, nil
}

// VerifyChain walks the entire per-tenant chain and reports any
// deletion, reordering, truncation, or row tampering.
func (l *signedLogger) VerifyChain(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrQueryTenantRequired
	}
	entries, err := l.store.ListByTenantSeq(ctx, tenantID)
	if err != nil {
		return err
	}
	var prev Entry
	for i, e := range entries {
		if err := l.Verify(e); err != nil {
			return fmt.Errorf("actionlog: entry %q: %w", e.ID, err)
		}
		if e.Seq != int64(i+1) {
			return fmt.Errorf("%w: tenant %q expected seq %d, got %d at id %q", ErrChainBroken, tenantID, i+1, e.Seq, e.ID)
		}
		if i == 0 {
			if e.PrevHash != zeroPrevHash {
				return fmt.Errorf("%w: tenant %q first entry must have zero prev_hash", ErrChainBroken, tenantID)
			}
		} else {
			secret, ok := l.secrets.Resolve(prev.SignatureKeyID)
			if !ok {
				return ErrUnknownKeyID
			}
			expected, err := entryHash(prev, secret)
			if err != nil {
				return err
			}
			if e.PrevHash != expected {
				return fmt.Errorf("%w: tenant %q seq %d prev_hash mismatch", ErrChainBroken, tenantID, e.Seq)
			}
		}
		prev = e
	}
	return nil
}

// Sign computes and returns the canonical signature for an entry
// without persisting it. Used by off-band verifiers and tests.
func (l *signedLogger) Sign(e Entry) (string, string, error) {
	keyID := l.secrets.CurrentKeyID()
	secret, ok := l.secrets.Resolve(keyID)
	if !ok {
		return "", "", fmt.Errorf("actionlog: current key id %q not resolvable: %w", keyID, ErrUnknownKeyID)
	}
	e.SignatureKeyID = keyID
	sig, err := computeSignature(e, secret)
	if err != nil {
		return "", "", err
	}
	return sig, keyID, nil
}

// Verify recomputes the signature and constant-time compares.
func (l *signedLogger) Verify(e Entry) error {
	if e.SignatureKeyID == "" {
		return ErrSignatureInvalid
	}
	secret, ok := l.secrets.Resolve(e.SignatureKeyID)
	if !ok {
		return ErrUnknownKeyID
	}
	expected, err := computeSignature(e, secret)
	if err != nil {
		return err
	}
	got, err := hex.DecodeString(e.Signature)
	if err != nil {
		return ErrSignatureInvalid
	}
	want, err := hex.DecodeString(expected)
	if err != nil {
		return ErrSignatureInvalid
	}
	if !hmac.Equal(got, want) {
		return ErrSignatureInvalid
	}
	return nil
}

// validate enforces required-field invariants before signing.
func validate(e Entry) error {
	if e.ID == "" || e.TenantID == "" || e.Actor == "" || e.Action == "" {
		return ErrInvalidEntry
	}
	switch e.Outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomeDenied:
	default:
		return ErrInvalidOutcome
	}
	return nil
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

// entryHash returns the hex-encoded HMAC-SHA256 of the entry's
// canonical form. Used to compute the next entry's PrevHash and to
// verify the chain. Distinct from the entry's own [Entry.Signature]
// only conceptually — the algorithm and key are identical, so a
// chained verifier and a row verifier see the same bytes.
func entryHash(e Entry, secret []byte) (string, error) {
	return computeSignature(e, secret)
}
