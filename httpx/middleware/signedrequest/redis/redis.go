package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/httpx/middleware/signedrequest"
)

// Compile-time assertion that *RedisNonceStore implements the
// kit's NonceStore contract. If the interface ever drifts the build
// fails here, not at the consumer's [signedrequest.Middleware] call.
var _ signedrequest.NonceStore = (*RedisNonceStore)(nil)

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
func WithKeyPrefix(p string) Option {
	return func(s *RedisNonceStore) { s.prefix = p }
}

// WithCallTimeout bounds the per-call context used for the Redis
// round trip. Default: 2 seconds. Set tighter for latency-sensitive
// services; set looser only if your Redis is reliably slow (you
// almost certainly have a different problem in that case).
func WithCallTimeout(d time.Duration) Option {
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
func (s *RedisNonceStore) SeenOrStore(nonce string) (bool, error) {
	if nonce == "" {
		return false, errors.New("signedrequest/redis: empty nonce")
	}
	ctx, cancel := s.ctx()
	defer cancel()

	ok, err := s.client.SetNX(ctx, s.key(nonce), 1, s.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("signedrequest/redis: SET NX EX: %w", err)
	}
	return ok, nil
}

func (s *RedisNonceStore) key(nonce string) string {
	return s.prefix + nonce
}
