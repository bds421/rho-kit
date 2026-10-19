package redis

import (
	"context"
	"crypto/rand"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// releaseLockScript deletes the lock only while it still holds the caller's
// token, so a lock that expired and was re-acquired by another replica is not
// released by the former holder.
var releaseLockScript = goredis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

// TryLockRefresh implements [oauth2.RefreshLocker] with SET NX PX on a
// per-session key holding a random token. The lock expires after ttl even
// when its holder crashes.
func (s *Store) TryLockRefresh(ctx context.Context, sessionID string, ttl time.Duration) (func(context.Context) error, bool, error) {
	if err := validateWrite(ctx, sessionID, ttl); err != nil {
		return nil, false, err
	}
	key := s.refreshLockKey(sessionID)
	token := rand.Text()
	acquired, err := s.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, redact.WrapError("oauth2/redis: acquire refresh lock", err)
	}
	if !acquired {
		return nil, false, nil
	}
	release := func(ctx context.Context) error {
		if err := releaseLockScript.Run(ctx, s.client, []string{key}, token).Err(); err != nil {
			return redact.WrapError("oauth2/redis: release refresh lock", err)
		}
		return nil
	}
	return release, true, nil
}

func (s *Store) refreshLockKey(id string) string { return s.prefix + "refresh-lock:" + id }
