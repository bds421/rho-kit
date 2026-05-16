package websocket

import "sync/atomic"

// connLimiter caps the number of concurrent connections served by a
// single [Handle] handler. A nil receiver is treated as "no limit" so
// the handler code path is identical whether or not the cap is wired.
//
// The acquire uses a CAS loop rather than the naive
// Add(1)/compare/Add(-1) pattern: the naive version transiently
// inflates the count, which can cause unrelated concurrent acquirers
// to see the inflated value and falsely reject. CAS keeps the
// observable count an accurate single source of truth under
// contention.
type connLimiter struct {
	max     int64
	current atomic.Int64
}

func newConnLimiter(max int64) *connLimiter {
	if max <= 0 {
		return nil
	}
	return &connLimiter{max: max}
}

func (l *connLimiter) tryAcquire() bool {
	if l == nil {
		return true
	}
	for {
		cur := l.current.Load()
		if cur >= l.max {
			return false
		}
		if l.current.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (l *connLimiter) release() {
	if l == nil {
		return
	}
	l.current.Add(-1)
}
