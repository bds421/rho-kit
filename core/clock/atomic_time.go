package clock

import (
	"sync/atomic"
	"time"
)

// atomicTime is a goroutine-safe time.Time cell. atomic.Value is
// avoided because it requires the same concrete type on every Store
// (a time.Time stored once is fine, but the documentation cautions
// about it). A plain mutex would also work; sync/atomic keeps Stub
// allocation-free on the hot path.
type atomicTime struct {
	v atomic.Pointer[time.Time]
}

func (a *atomicTime) Load() time.Time {
	if p := a.v.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

func (a *atomicTime) Store(t time.Time) {
	a.v.Store(&t)
}

// Add atomically advances the stored time by d and returns the new
// value. Unlike a Load-then-Store pair, it retries on contention so
// concurrent callers never lose an increment.
func (a *atomicTime) Add(d time.Duration) time.Time {
	for {
		old := a.v.Load()
		var base time.Time
		if old != nil {
			base = *old
		}
		next := base.Add(d)
		if a.v.CompareAndSwap(old, &next) {
			return next
		}
	}
}
