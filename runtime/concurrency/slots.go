package concurrency

import (
	"context"
	"fmt"
	"sync"
)

// KeyedSlots bound how much work runs at once, in all and per key, so one
// key cannot take every slot: a service that stores files bounds the
// uploads and downloads open at once per caller, so an account that sends
// or reads slowly holds only its own share. Keys with nothing held or
// waiting are forgotten. Safe for concurrent use.
type KeyedSlots[K comparable] struct {
	total  chan struct{}
	perKey int
	mu     sync.Mutex
	keys   map[K]*keySlots
}

// keySlots are one key's slots and how many callers hold or wait for them,
// so the entry goes when none does.
type keySlots struct {
	held  chan struct{}
	users int
}

// NewKeyedSlots allows total holders at once and perKey per key. Both must
// be positive.
func NewKeyedSlots[K comparable](total, perKey int) *KeyedSlots[K] {
	if total <= 0 || perKey <= 0 {
		panic("concurrency: NewKeyedSlots requires positive bounds")
	}
	return &KeyedSlots[K]{total: make(chan struct{}, total), perKey: perKey, keys: make(map[K]*keySlots)}
}

// Acquire waits for one of key's slots, then for one of all, until ctx
// ends; it then returns the cause of ctx's end (also when ctx has ended
// before the call). release gives both slots
// back; calling it again does nothing.
func (s *KeyedSlots[K]) Acquire(ctx context.Context, key K) (release func(), err error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("concurrency: wait for a slot: %w", context.Cause(ctx))
	}
	mine := s.join(key)
	select {
	case mine.held <- struct{}{}:
	case <-ctx.Done():
		s.leave(key, mine)
		return nil, fmt.Errorf("concurrency: wait for a slot: %w", context.Cause(ctx))
	}
	select {
	case s.total <- struct{}{}:
	case <-ctx.Done():
		<-mine.held
		s.leave(key, mine)
		return nil, fmt.Errorf("concurrency: wait for a slot: %w", context.Cause(ctx))
	}
	return sync.OnceFunc(func() {
		<-s.total
		<-mine.held
		s.leave(key, mine)
	}), nil
}

func (s *KeyedSlots[K]) join(key K) *keySlots {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keys[key]
	if k == nil {
		k = &keySlots{held: make(chan struct{}, s.perKey)}
		s.keys[key] = k
	}
	k.users++
	return k
}

func (s *KeyedSlots[K]) leave(key K, k *keySlots) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k.users--; k.users == 0 {
		delete(s.keys, key)
	}
}

// Keys reports how many keys hold or wait for a slot.
func (s *KeyedSlots[K]) Keys() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}
