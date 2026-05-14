// Package revocation provides a cache-backed JWT revocation checker.
//
// It intentionally depends only on a tiny cache-shaped interface. The
// concrete [data/cache.Cache] type satisfies it, but security/jwtutil does not
// import the data module, so consumers that only need JWT verification do not
// inherit cache implementation dependencies.
//
// # Audit logging
//
// Mutating operations (Revoke, RevokeID, Unrevoke) modify token-lifecycle state
// with real authorization consequences and so are auditable events. Wire either
// [WithLogger] (slog) or [WithAuditSink] (an observability/auditlog-shaped
// sink) and every revocation operation will emit a structured record with
// fields {action, jti, issuer, actor, outcome, reason}. If neither is wired
// the operations remain silent — this is the caller-must-cooperate contract
// documented in docs/audit/THREAT_MODEL.md §4.7.
package revocation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

const (
	defaultKeyPrefix = "jwt-revoked:"
	maxKeyLen        = 1024
	maxPrefixLen     = 128
	maxPartLen       = 1024
)

var (
	// ErrInvalidStore is returned when a method is called on a zero-value
	// Store or a Store whose cache/prefix has been zeroed out.
	ErrInvalidStore = errors.New("jwt revocation: store is not initialized")
	// ErrMissingToken is returned by Revoke / IsRevoked when the caller
	// passes a nil [*jwtutil.Claims].
	ErrMissingToken = errors.New("jwt revocation: token claims are missing")
	// ErrMissingTokenID is returned when a token has no JTI claim. It
	// aliases [jwtutil.ErrMissingTokenID] so callers can match either
	// sentinel with errors.Is.
	ErrMissingTokenID = jwtutil.ErrMissingTokenID
	// ErrInvalidExpiry is returned when Revoke is called with a token
	// whose expiration is at or before the configured clock. Writing a
	// zero or negative TTL would race the cache backend into a no-op
	// (or worse, a permanent entry on some backends).
	ErrInvalidExpiry = errors.New("jwt revocation: token expiration must be in the future")
	// ErrInvalidKey is returned when the issuer or token ID contains
	// unsafe runes or when the assembled cache key exceeds the
	// per-implementation length budget.
	ErrInvalidKey = errors.New("jwt revocation: key contains invalid data")
)

// Cache is the minimal backend contract needed by Store. data/cache.Cache
// satisfies this interface, as do Redis-backed and tenant-scoped cache
// wrappers. Implementations must be safe for concurrent use.
type Cache interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// AuditEvent is the cross-package field set emitted for one revocation
// mutation. It mirrors the standard audit-log envelope used elsewhere in
// rho-kit so a single sink can decode events from any source — see the
// "Cross-cutting field set" note in docs/audit/THREAT_MODEL.md.
type AuditEvent struct {
	Action   string // dot-namespaced verb: "jwt.revoke", "jwt.revoke.undo"
	Actor    string // principal performing the operation, derived from ctx
	Resource string // canonical revocation key (length-prefixed issuer/jti)
	Issuer   string // token issuer; may be empty
	JTI      string // token id (jti); already validated for log safety
	Outcome  string // "success" | "error"
	Reason   string // short error-class for failed operations; empty on success
}

// AuditSink consumes structured revocation events. The shape matches the
// minimum surface of observability/auditlog.Logger so production callers wire
// the concrete Logger here without revocation importing the observability
// module. Implementations must be safe for concurrent use.
type AuditSink interface {
	LogRevocation(ctx context.Context, event AuditEvent)
}

// ActorFromContext extracts the acting principal from ctx. The function runs
// on every mutating call; it must be cheap and side-effect-free.
type ActorFromContext func(ctx context.Context) string

// Store records revoked JWT IDs until the token's natural expiration.
type Store struct {
	cache    Cache
	prefix   string
	clock    clock.Func
	logger   *slog.Logger
	audit    AuditSink
	actorFn  ActorFromContext
	verbose  bool
}

// Option configures Store.
type Option func(*Store)

// WithKeyPrefix overrides the cache key prefix. The prefix must be non-empty,
// bounded, valid UTF-8, and free of control characters.
func WithKeyPrefix(prefix string) Option {
	if !validPrefix(prefix) {
		panic("jwt revocation: WithKeyPrefix requires a non-empty safe prefix")
	}
	return func(s *Store) { s.prefix = prefix }
}

// WithClock overrides the time source. Useful for deterministic tests.
func WithClock(fn clock.Func) Option {
	if fn == nil {
		panic("jwt revocation: WithClock requires a non-nil time source")
	}
	return func(s *Store) { s.clock = fn }
}

// WithLogger wires a slog.Logger that receives a structured record on every
// mutating call (Revoke, RevokeID, Unrevoke). Success records are emitted at
// info level; failures at error. If both [WithLogger] and [WithAuditSink] are
// wired both receive the same event — slog is intended for operator-visible
// triage and AuditSink for tamper-evident retention.
//
// Panics on nil to fail fast at wiring time.
func WithLogger(l *slog.Logger) Option {
	if l == nil {
		panic("jwt revocation: WithLogger requires a non-nil logger")
	}
	return func(s *Store) { s.logger = l }
}

// WithAuditSink wires an [AuditSink] that receives an [AuditEvent] for every
// mutating call. The concrete observability/auditlog.Logger satisfies this
// surface; revocation does not depend on the observability module so consumers
// pay no extra dep cost when the sink is not wired.
//
// Panics on nil to fail fast at wiring time.
func WithAuditSink(sink AuditSink) Option {
	if sink == nil {
		panic("jwt revocation: WithAuditSink requires a non-nil sink")
	}
	return func(s *Store) { s.audit = sink }
}

// WithActorFromContext registers a function that extracts the acting principal
// from the request context. The returned value is stamped on every audit
// record as "actor"; an empty string is rendered as "unknown".
//
// Panics on nil to fail fast at wiring time.
func WithActorFromContext(fn ActorFromContext) Option {
	if fn == nil {
		panic("jwt revocation: WithActorFromContext requires a non-nil function")
	}
	return func(s *Store) { s.actorFn = fn }
}

// WithVerboseAuditFields opts into emitting the raw jti / issuer in audit
// records instead of length-redacted placeholders. Use only when the audit
// sink is itself sensitive-safe (e.g. tamper-evident log with restricted
// access). Off by default — revocation is conservative because jti values can
// be guessable identifiers and issuer URLs can leak topology.
func WithVerboseAuditFields() Option {
	return func(s *Store) { s.verbose = true }
}

// New creates a cache-backed revocation store. Panics on nil cache or nil
// options so misconfiguration fails at startup.
func New(cache Cache, opts ...Option) *Store {
	if cache == nil {
		panic("jwt revocation: cache must not be nil")
	}
	s := &Store{
		cache:  cache,
		prefix: defaultKeyPrefix,
		clock:  time.Now,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("jwt revocation: option must not be nil")
		}
		opt(s)
	}
	return s
}

// Revoke stores claims.ID until claims.ExpiresAt. Expired tokens are rejected
// instead of being written with a zero or negative TTL.
func (s *Store) Revoke(ctx context.Context, claims *jwtutil.Claims) error {
	if claims == nil {
		return ErrMissingToken
	}
	return s.RevokeID(ctx, claims.Issuer, claims.ID, time.Unix(claims.ExpiresAt, 0))
}

// RevokeID stores id until expiresAt. issuer may be empty, but id must be
// present; the key encoding length-prefixes issuer and id so delimiters cannot
// collide.
func (s *Store) RevokeID(ctx context.Context, issuer, id string, expiresAt time.Time) error {
	err := s.revokeID(ctx, issuer, id, expiresAt)
	s.emit(ctx, "jwt.revoke", issuer, id, err)
	return err
}

func (s *Store) revokeID(ctx context.Context, issuer, id string, expiresAt time.Time) error {
	if err := s.ready(); err != nil {
		return err
	}
	key, err := s.key(issuer, id)
	if err != nil {
		return err
	}
	ttl := time.Until(expiresAt)
	if s.clock != nil {
		ttl = expiresAt.Sub(s.clock())
	}
	if ttl <= 0 {
		return ErrInvalidExpiry
	}
	return s.cache.Set(ctx, key, []byte("1"), ttl)
}

// IsRevoked implements jwtutil.RevocationChecker.
func (s *Store) IsRevoked(ctx context.Context, claims *jwtutil.Claims) (bool, error) {
	if claims == nil {
		return false, ErrMissingToken
	}
	return s.IsRevokedID(ctx, claims.Issuer, claims.ID)
}

// IsRevokedID reports whether id is currently revoked.
func (s *Store) IsRevokedID(ctx context.Context, issuer, id string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	key, err := s.key(issuer, id)
	if err != nil {
		return false, err
	}
	return s.cache.Exists(ctx, key)
}

// Unrevoke removes a revocation marker — the token referenced by (issuer, id)
// becomes valid again until its natural expiry. The previous name was
// Unrevoke, which was misleading: "Forget" implied cache eviction, but the
// semantic is "undo a Revoke." Used for tests and administrative repair
// after an accidental revocation.
func (s *Store) Unrevoke(ctx context.Context, issuer, id string) error {
	err := s.unrevoke(ctx, issuer, id)
	s.emit(ctx, "jwt.revoke.undo", issuer, id, err)
	return err
}

func (s *Store) unrevoke(ctx context.Context, issuer, id string) error {
	if err := s.ready(); err != nil {
		return err
	}
	key, err := s.key(issuer, id)
	if err != nil {
		return err
	}
	return s.cache.Delete(ctx, key)
}

// emit writes a single audit record for a mutating revocation operation. It is
// a no-op when neither WithLogger nor WithAuditSink is configured so default
// stores stay silent (the historical behaviour). Field naming matches the
// cross-package audit shape so a single sink can decode events from any
// rho-kit source — see AuditEvent.
func (s *Store) emit(ctx context.Context, action, issuer, id string, err error) {
	if s == nil || (s.logger == nil && s.audit == nil) {
		return
	}
	outcome := "success"
	reason := ""
	if err != nil {
		outcome = "error"
		// Reason carries the error class only — never the wrapped Error()
		// string, which can include backend topology / message text. Callers
		// who need the full chain inspect the returned error.
		reason = errorClass(err)
	}
	actor := s.actor(ctx)
	jtiField, issuerField := s.encodeIdentifiers(issuer, id)
	resource := identifierResource(issuer, id)

	if s.logger != nil {
		s.logger.Info("jwt revocation",
			slog.String("action", action),
			slog.String("actor", actor),
			slog.String("resource", resource),
			slog.String("issuer", issuerField),
			slog.String("jti", jtiField),
			slog.String("outcome", outcome),
			slog.String("reason", reason),
		)
	}
	if s.audit != nil {
		// Audit sinks run AFTER the underlying mutation has already
		// committed. A panic in the sink must not propagate to the
		// caller — wave 66 caught that a buggy sink could crash the
		// goroutine performing Revoke/IsRevoked even though the
		// revocation state was successfully recorded.
		func() {
			defer func() {
				if r := recover(); r != nil && s.logger != nil {
					s.logger.Error("jwt revocation audit sink panicked",
						slog.String("action", action),
						slog.String("panic", redact.PanicValue(r)),
					)
				}
			}()
			s.audit.LogRevocation(ctx, AuditEvent{
				Action:   action,
				Actor:    actor,
				Resource: resource,
				Issuer:   issuerField,
				JTI:      jtiField,
				Outcome:  outcome,
				Reason:   reason,
			})
		}()
	}
}

func (s *Store) actor(ctx context.Context) string {
	if s == nil || s.actorFn == nil {
		return "unknown"
	}
	if actor := s.actorFn(ctx); actor != "" {
		return actor
	}
	return "unknown"
}

// encodeIdentifiers redacts jti / issuer unless the operator opted into
// verbose audit fields. Even in verbose mode an oversized issuer or invalid
// id is collapsed to the length-only form to keep audit records bounded.
func (s *Store) encodeIdentifiers(issuer, id string) (jtiField, issuerField string) {
	if s.verbose && validPart(id) {
		jtiField = id
	} else {
		jtiField = redactedLen(id)
	}
	if s.verbose && validPart(issuer) {
		issuerField = issuer
	} else {
		issuerField = redactedLen(issuer)
	}
	return jtiField, issuerField
}

// identifierResource returns a stable, log-safe identifier for the revocation
// record. It never exposes the raw jti / issuer; consumers correlate via the
// per-record audit ID + the action verb.
func identifierResource(issuer, id string) string {
	return fmt.Sprintf("jwt:%s/%s", redactedLen(issuer), redactedLen(id))
}

func redactedLen(value string) string {
	if value == "" {
		return "<empty>"
	}
	return fmt.Sprintf("<redacted %d bytes>", len(value))
}

// errorClass returns the canonical kit sentinel an error wraps, or "unknown"
// for unrecognised infrastructure failures. The audit record carries the
// class, not the wrapped Error() string, so log scrapers cannot leak backend
// topology / cache error text.
func errorClass(err error) string {
	switch {
	case errors.Is(err, ErrInvalidStore):
		return "invalid_store"
	case errors.Is(err, ErrMissingToken):
		return "missing_token"
	case errors.Is(err, ErrMissingTokenID):
		return "missing_token_id"
	case errors.Is(err, ErrInvalidExpiry):
		return "invalid_expiry"
	case errors.Is(err, ErrInvalidKey):
		return "invalid_key"
	default:
		return "backend_error"
	}
}

func (s *Store) ready() error {
	if s == nil || s.cache == nil || !validPrefix(s.prefix) {
		return ErrInvalidStore
	}
	return nil
}

func (s *Store) key(issuer, id string) (string, error) {
	if id == "" {
		return "", ErrMissingTokenID
	}
	if !validPart(issuer) || !validPart(id) {
		return "", ErrInvalidKey
	}
	key := fmt.Sprintf("%s%d:%s:%d:%s", s.prefix, len(issuer), issuer, len(id), id)
	if len(key) > maxKeyLen {
		return "", ErrInvalidKey
	}
	return key, nil
}

func validPart(part string) bool {
	return len(part) <= maxPartLen && !containsInvalidKeyRune(part)
}

func validPrefix(prefix string) bool {
	return prefix != "" &&
		len(prefix) <= maxPrefixLen &&
		!containsInvalidKeyRune(prefix)
}

func containsInvalidKeyRune(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
