// asvs: V11.1.1, V11.1.2
package lock

import (
	"context"
	"errors"
)

// ErrLockLost is returned by Lock.Release / Lock.Extend when the operation
// detected that the caller no longer holds the lock — either the TTL expired
// while the critical section was running, or another process forcibly took
// the key, or the previous Release silently succeeded on the broker but
// failed on the response path.
//
// Callers that need to short-circuit a critical section on TTL expiry should
// either drive Extend on a heartbeat and treat (false, nil) as lost, or
// inspect the Release return value with errors.Is(err, lock.ErrLockLost).
var ErrLockLost = errors.New("lock: ownership lost")

// Lock represents an acquired distributed lock. Implementations must be safe
// to call Release/Extend from a single goroutine; concurrent access from
// multiple goroutines on the same Lock value is not required to be safe.
type Lock interface {
	// Release releases the lock. Returns ErrLockLost if the lock was already
	// expired or held by someone else by the time Release ran.
	Release(ctx context.Context) error
	// Extend extends the lock's TTL. Returns (true, nil) on success,
	// (false, nil) if the lock was already lost (no error in the
	// distributed-systems sense — the caller simply lost the race).
	Extend(ctx context.Context) (bool, error)
}

// Locker acquires distributed locks by key. Each call returns a fresh Lock
// handle bound to a private owner token; callers cannot accidentally release
// someone else's lock by re-using a Lock value.
type Locker interface {
	// Acquire attempts to acquire a lock for the given key.
	// Returns (Lock, true, nil) on success, (nil, false, nil) if the lock is
	// held by another process, or (nil, false, err) on backend errors.
	Acquire(ctx context.Context, key string) (Lock, bool, error)
}
