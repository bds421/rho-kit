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

	// Signature is HMAC-SHA256 over the canonical form of all other
	// fields, hex-encoded. Set by [Logger.Append]; read via
	// [Logger.Verify] or implicitly by [Logger.Get] / [Logger.List].
	Signature string `json:"signature"`
}

// Query controls which entries [Logger.List] returns. The zero value
// returns every entry (subject to the [Query.Limit] default). Filters
// compose with AND semantics; an empty filter field is unconstrained.
type Query struct {
	// TenantID restricts to a single tenant. Strongly recommended — a
	// cross-tenant List leaks audit data across customers.
	TenantID string

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
	// and SignatureKeyID if unset; returns the populated entry. Errors:
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
}

// Store is the persistence interface implemented by backends in
// data/actionlog/memory and data/actionlog/postgres. The store does
// not concern itself with signing — [Logger] computes the signature
// before calling [Store.Append] and verifies on the read path.
type Store interface {
	// Append persists a fully-populated entry (signature already
	// computed). Implementations must reject duplicate IDs.
	Append(ctx context.Context, e Entry) error

	// Get returns the entry by id, or [ErrNotFound] if absent.
	Get(ctx context.Context, id string) (Entry, error)

	// List returns entries matching q, ordered by OccurredAt
	// descending, then ID descending for stable ordering when
	// timestamps tie.
	List(ctx context.Context, q Query) ([]Entry, error)
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

// Resolve returns the secret for keyID.
func (s *StaticSecrets) Resolve(keyID string) ([]byte, bool) {
	k, ok := s.keys[keyID]
	return k, ok
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

// Append validates, signs, and persists the entry.
func (l *signedLogger) Append(ctx context.Context, e Entry) (Entry, error) {
	if e.ID == "" {
		e.ID = l.newID()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = l.clock()
	}
	e.OccurredAt = e.OccurredAt.UTC()

	if err := validate(e); err != nil {
		return Entry{}, err
	}

	keyID := l.secrets.CurrentKeyID()
	secret, ok := l.secrets.Resolve(keyID)
	if !ok {
		return Entry{}, fmt.Errorf("actionlog: current key id %q not resolvable: %w", keyID, ErrUnknownKeyID)
	}

	e.SignatureKeyID = keyID
	sig, err := computeSignature(e, secret)
	if err != nil {
		return Entry{}, fmt.Errorf("actionlog: compute signature: %w", err)
	}
	e.Signature = sig

	if err := l.store.Append(ctx, e); err != nil {
		return Entry{}, err
	}
	return e, nil
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

// List reads and verifies a batch of entries. The first verification
// failure aborts the call so callers don't get a half-truthful page.
func (l *signedLogger) List(ctx context.Context, q Query) ([]Entry, error) {
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
