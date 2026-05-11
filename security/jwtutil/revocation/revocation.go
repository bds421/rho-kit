// Package revocation provides a cache-backed JWT revocation checker.
//
// It intentionally depends only on a tiny cache-shaped interface. The
// concrete [data/cache.Cache] type satisfies it, but security/jwtutil does not
// import the data module, so consumers that only need JWT verification do not
// inherit cache implementation dependencies.
package revocation

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

const (
	defaultKeyPrefix = "jwt-revoked:"
	maxKeyLen        = 1024
	maxPrefixLen     = 128
	maxPartLen       = 1024
)

var (
	ErrInvalidStore   = errors.New("jwt revocation: store is not initialized")
	ErrMissingToken   = errors.New("jwt revocation: token claims are missing")
	ErrMissingTokenID = jwtutil.ErrMissingTokenID
	ErrInvalidExpiry  = errors.New("jwt revocation: token expiration must be in the future")
	ErrInvalidKey     = errors.New("jwt revocation: key contains invalid data")
)

// Cache is the minimal backend contract needed by Store. data/cache.Cache
// satisfies this interface, as do Redis-backed and tenant-scoped cache
// wrappers. Implementations must be safe for concurrent use.
type Cache interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// Store records revoked JWT IDs until the token's natural expiration.
type Store struct {
	cache  Cache
	prefix string
	clock  func() time.Time
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
func WithClock(fn func() time.Time) Option {
	if fn == nil {
		panic("jwt revocation: WithClock requires a non-nil time source")
	}
	return func(s *Store) { s.clock = fn }
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

// ForgetID removes a revocation marker. It is mostly useful for tests and
// administrative repair after an accidental revocation.
func (s *Store) ForgetID(ctx context.Context, issuer, id string) error {
	if err := s.ready(); err != nil {
		return err
	}
	key, err := s.key(issuer, id)
	if err != nil {
		return err
	}
	return s.cache.Delete(ctx, key)
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
