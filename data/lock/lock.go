package lock

import "context"

// Lock represents an acquired distributed lock.
type Lock interface {
	// Release releases the lock. Must be called after the protected operation.
	Release(ctx context.Context) error
	// Extend extends the lock's TTL. Use for long-running operations.
	Extend(ctx context.Context) (bool, error)
}

// Locker acquires distributed locks by key.
type Locker interface {
	// Acquire attempts to acquire a lock for the given key.
	// Returns the Lock and true if acquired, or nil and false if already held.
	Acquire(ctx context.Context, key string) (Lock, bool, error)
}
