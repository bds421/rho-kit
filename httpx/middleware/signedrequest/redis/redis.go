package redis

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

// Compile-time assertion that *RedisNonceStore implements the
// kit's NonceStore contract. If the interface ever drifts the build
// fails here, not at the consumer's [signedrequest.Middleware] call.
var _ signedrequest.NonceStore = (*RedisNonceStore)(nil)

// ErrInvalidStore is returned when SeenOrStore is invoked on a nil or
// otherwise uninitialized RedisNonceStore.
var ErrInvalidStore = errors.New("signedrequest/redis: store is not initialized")

// defaultKeyPrefix is the namespace under which nonces are stored.
// "signedrequest:nonce:" mirrors the kit's package path so a single
// shared Redis can host other rho-kit subsystems without collision.
const defaultKeyPrefix = "signedrequest:nonce:"

// RedisNonceStore is a [signedrequest.NonceStore] backed by a shared
// Redis. It implements replay protection across multiple replicas of
// the same signed-request audience.
//
// Construct with [New]; the store is safe for concurrent use.
type RedisNonceStore struct {
	client goredis.UniversalClient
	ttl    time.Duration
	prefix string
	ctx    func() (context.Context, context.CancelFunc)
}

// Option configures a [RedisNonceStore].
type Option func(*RedisNonceStore)

// WithKeyPrefix overrides the Redis key namespace. Default:
// `signedrequest:nonce:`. Use a per-environment or per-audience
// prefix when the same Redis is shared by independent services so
// a nonce observed by one cannot reject a fresh request to another.
//
// Panics if the prefix is empty, invalid, or longer than [maxKeyPrefixLen]
// (audit FR-027) — pathological prefixes inflate/corrupt every Redis key
// and have caused production OOMs in our incident history.
func WithKeyPrefix(p string) Option {
	if p == "" {
		panic("signedrequest/redis: WithKeyPrefix requires a non-empty prefix")
	}
	if len(p) > maxKeyPrefixLen {
		panic("signedrequest/redis: WithKeyPrefix prefix exceeds maximum length")
	}
	if containsInvalidStringBytes(p) {
		panic("signedrequest/redis: WithKeyPrefix prefix contains invalid characters")
	}
	return func(s *RedisNonceStore) { s.prefix = p }
}

// maxKeyPrefixLen caps Redis key prefixes so a misconfigured /
// attacker-influenced prefix cannot create pathologically long
// Redis keys. 128 bytes is generous for a "namespace:env:audience:"
// shape and well below Redis's hard cap.
const maxKeyPrefixLen = 128

// WithCallTimeout bounds the per-call context used for the Redis
// round trip. Default: 2 seconds. Set tighter for latency-sensitive
// services; set looser only if your Redis is reliably slow (you
// almost certainly have a different problem in that case).
//
// Panics if d <= 0 — a zero or negative timeout would create an
// immediately expired context and fail every nonce SET NX call closed,
// silently turning the verifier into a denial-of-service.
func WithCallTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("signedrequest/redis: WithCallTimeout requires a positive duration")
	}
	return func(s *RedisNonceStore) {
		s.ctx = func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), d)
		}
	}
}

// New constructs a RedisNonceStore.
//
// client is the Redis connection. It must be non-nil — the
// constructor panics on nil to fail loudly at startup; a misconfigured
// nonce store is worse than no signing at all.
//
// ttl is the lifetime of each stored nonce in Redis. It must be at
// least 2 × the signed-request middleware's max clock skew so a
// timestamp inside the verifier's window cannot outlive its replay
// guard.
func New(client goredis.UniversalClient, ttl time.Duration, opts ...Option) *RedisNonceStore {
	if client == nil {
		panic("signedrequest/redis: client must not be nil")
	}
	if ttl <= 0 {
		panic("signedrequest/redis: ttl must be > 0")
	}
	s := &RedisNonceStore{
		client: client,
		ttl:    ttl,
		prefix: defaultKeyPrefix,
		ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 2*time.Second)
		},
	}
	for _, o := range opts {
		if o == nil {
			panic("signedrequest/redis: option must not be nil")
		}
		o(s)
	}
	return s
}

// SeenOrStore implements [signedrequest.NonceStore] using SET NX EX.
//
// Returns:
//   - (true, nil)  when the nonce is fresh and was just stored.
//   - (false, nil) when Redis already held the nonce — replay.
//   - (false, err) on any Redis-side failure. The middleware
//     translates this into a 500; the package does NOT fail open.
//
// FR-027 [LOW]: rejects nonces longer than the verifier's wire
// limit and non-portable bytes so a caller bypassing the middleware
// (e.g. test harness) cannot construct unbounded or corrupt Redis keys.
func (s *RedisNonceStore) SeenOrStore(nonce string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if nonce == "" {
		return false, errors.New("signedrequest/redis: empty nonce")
	}
	if len(nonce) > maxNonceLen {
		return false, errors.New("signedrequest/redis: nonce exceeds maximum length")
	}
	if containsInvalidStringBytes(nonce) {
		return false, errors.New("signedrequest/redis: nonce contains invalid characters")
	}
	ctx, cancel := s.ctx()
	defer cancel()

	ok, err := s.client.SetNX(ctx, s.key(nonce), 1, s.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("signedrequest/redis: SET NX EX: %w", err)
	}
	return ok, nil
}

func (s *RedisNonceStore) ready() error {
	if s == nil ||
		s.client == nil ||
		s.ttl <= 0 ||
		s.prefix == "" ||
		len(s.prefix) > maxKeyPrefixLen ||
		containsInvalidStringBytes(s.prefix) ||
		s.ctx == nil {
		return ErrInvalidStore
	}
	return nil
}

// maxNonceLen caps the wire-level nonce length we are willing to
// accept as a Redis key suffix. Mirrors the verifier's cap (audit
// FR-026 / FR-027); the redis store independently enforces this so
// direct callers cannot bypass it.
const maxNonceLen = 64

func (s *RedisNonceStore) key(nonce string) string {
	return s.prefix + nonce
}

func containsInvalidStringBytes(s string) bool {
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
