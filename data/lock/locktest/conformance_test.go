package locktest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/data/v2/lock/locktest"
)

// TestMemoryLocker_Conformance dogfoods the conformance suite
// against an in-memory Locker. This is NOT a kit-shipped
// primitive — a real Locker needs cross-process exclusivity
// which a Go map cannot provide. It exists here purely so the
// conformance harness has a target to validate itself against.
// pgadvisory + redislock pass the same suite from their own
// integration tests.
func TestMemoryLocker_Conformance(t *testing.T) {
	locktest.Run(t, func(t *testing.T) lock.Locker {
		return newMemoryLocker()
	})
}

// memoryLocker is a process-local fake for the conformance smoke
// test only.
type memoryLocker struct {
	mu     sync.Mutex
	holders map[string]*memoryLock
}

func newMemoryLocker() *memoryLocker {
	return &memoryLocker{holders: make(map[string]*memoryLock)}
}

type memoryLock struct {
	parent   *memoryLocker
	key      string
	released bool
	mu       sync.Mutex
}

func (m *memoryLocker) Acquire(_ context.Context, key string) (lock.Lock, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, held := m.holders[key]; held {
		return nil, false, nil
	}
	l := &memoryLock{parent: m, key: key}
	m.holders[key] = l
	return l, true, nil
}

func (l *memoryLock) Release(_ context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return lock.ErrLockLost
	}
	l.released = true
	l.parent.mu.Lock()
	defer l.parent.mu.Unlock()
	if cur, ok := l.parent.holders[l.key]; ok && cur == l {
		delete(l.parent.holders, l.key)
	}
	return nil
}

func (l *memoryLock) Extend(_ context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return false, nil
	}
	return true, nil
}
