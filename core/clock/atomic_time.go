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
