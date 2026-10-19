package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// indexAddScript adds a session ID to an index set and extends the set's
// expiry to at least the session TTL, so the index outlives every session it
// lists without being shortened by a later, shorter-lived session.
var indexAddScript = goredis.NewScript(`
redis.call("SADD", KEYS[1], ARGV[1])
local ttl = tonumber(ARGV[2])
if redis.call("PTTL", KEYS[1]) < ttl then
	redis.call("PEXPIRE", KEYS[1], ttl)
end
return 1
`)

// subjectIndexKey and sidIndexKey hash the subject and provider session ID
// so index key names are fixed-size and carry no user identifiers.
func (s *Store) subjectIndexKey(subject string) string {
	return s.prefix + "subject:" + hashKeyPart(subject)
}

func (s *Store) sidIndexKey(sid string) string {
	return s.prefix + "sid:" + hashKeyPart(sid)
}

func hashKeyPart(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// queueIndexAdd queues index updates for session on p.
func (s *Store) queueIndexAdd(ctx context.Context, p goredis.Pipeliner, sessionID string, session oauth2.Session, ttl time.Duration) {
	ms := ttl.Milliseconds()
	if session.UserID != "" {
		indexAddScript.Eval(ctx, p, []string{s.subjectIndexKey(session.UserID)}, sessionID, ms)
	}
	if session.SID != "" {
		indexAddScript.Eval(ctx, p, []string{s.sidIndexKey(session.SID)}, sessionID, ms)
	}
}

// queueIndexRemove queues removal of sessionID from the indexes of session.
func (s *Store) queueIndexRemove(ctx context.Context, p goredis.Pipeliner, sessionID string, session oauth2.Session) {
	if session.UserID != "" {
		p.SRem(ctx, s.subjectIndexKey(session.UserID), sessionID)
	}
	if session.SID != "" {
		p.SRem(ctx, s.sidIndexKey(session.SID), sessionID)
	}
}

// SessionIDsBySubject implements [oauth2.SessionIndex]. Members whose
// session no longer exists are pruned from the index.
func (s *Store) SessionIDsBySubject(ctx context.Context, subject string) ([]string, error) {
	return s.indexedIDs(ctx, subject, s.subjectIndexKey)
}

// SessionIDsBySID implements [oauth2.SessionIndex]. Members whose session
// no longer exists are pruned from the index.
func (s *Store) SessionIDsBySID(ctx context.Context, sid string) ([]string, error) {
	return s.indexedIDs(ctx, sid, s.sidIndexKey)
}

func (s *Store) indexedIDs(ctx context.Context, value string, keyFor func(string) string) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("oauth2/redis: context must not be nil")
	}
	if value == "" {
		return nil, errors.New("oauth2/redis: index value must not be empty")
	}
	key := keyFor(value)
	members, err := s.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, redact.WrapError("oauth2/redis: read session index", err)
	}
	if len(members) == 0 {
		return nil, nil
	}
	exists := make([]*goredis.IntCmd, len(members))
	if _, err := s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, id := range members {
			exists[i] = p.Exists(ctx, s.sessionKey(id))
		}
		return nil
	}); err != nil {
		return nil, redact.WrapError("oauth2/redis: read session index", err)
	}
	live := make([]string, 0, len(members))
	var stale []any
	for i, id := range members {
		if exists[i].Val() > 0 {
			live = append(live, id)
		} else {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		// Best effort: a failed prune only leaves entries the next lookup
		// filters again.
		_ = s.client.SRem(ctx, key, stale...).Err()
	}
	return live, nil
}

// Remember implements [oauth2.ReplayCache] with SET NX under the store
// prefix, so all replicas sharing the store share the replay window.
func (s *Store) Remember(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if err := validateWrite(ctx, key, ttl); err != nil {
		return false, err
	}
	first, err := s.client.SetNX(ctx, s.prefix+"replay:"+key, "1", ttl).Result()
	if err != nil {
		return false, redact.WrapError("oauth2/redis: record replay key", err)
	}
	return first, nil
}
