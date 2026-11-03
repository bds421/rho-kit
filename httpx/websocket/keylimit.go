package websocket

import (
	"net/http"
	"sync"
)

// KeyLimit caps concurrent connections per key a request names, such as
// the client address or the signed-in principal, so one key cannot take
// every connection [WithMaxConnections] allows. Create it with
// [NewKeyLimit], pass it to [WithKeyLimits], and ask [KeyLimit.Room]
// before telling a client it may connect.
type KeyLimit struct {
	max   int
	key   func(*http.Request) string
	mu    sync.Mutex
	count map[string]int
}

// NewKeyLimit allows n concurrent connections per non-empty key that key
// returns for the upgrade request; an empty key is not limited. It panics
// on n < 1 or a nil key so misconfiguration surfaces at startup.
func NewKeyLimit(n int, key func(*http.Request) string) *KeyLimit {
	if n < 1 {
		panic("httpx/websocket: NewKeyLimit requires a positive limit")
	}
	if key == nil {
		panic("httpx/websocket: NewKeyLimit requires a key function")
	}
	return &KeyLimit{max: n, key: key, count: map[string]int{}}
}

// Room reports whether r's key can take one more connection.
func (l *KeyLimit) Room(r *http.Request) bool {
	k := l.key(r)
	if k == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count[k] < l.max
}

func (l *KeyLimit) has(k string) bool { return k == "" || l.count[k] < l.max }

func (l *KeyLimit) add(k string, delta int) {
	if k == "" {
		return
	}
	if l.count[k] += delta; l.count[k] <= 0 {
		delete(l.count, k)
	}
}

// WithKeyLimits applies each limit to every upgrade: a request whose key
// holds its limit in any of them is refused with 429 Too Many Requests and
// Retry-After before the upgrade, counted as
// `httpx_websocket_rejected_total{reason="max_connections_per_key"}`.
// A connection counts against all of them or none.
func WithKeyLimits(limits ...*KeyLimit) Option {
	for _, l := range limits {
		if l == nil {
			panic("httpx/websocket: WithKeyLimits limit must not be nil")
		}
	}
	return func(c *config) { c.keyLimits = append(c.keyLimits, limits...) }
}

// keyHold is one connection's place in each key limit.
type keyHold struct {
	limits []*KeyLimit
	keys   []string
}

// acquireKeys takes a place in every limit for r, or none.
func acquireKeys(limits []*KeyLimit, r *http.Request) (keyHold, bool) {
	hold := keyHold{limits: limits, keys: make([]string, len(limits))}
	for i, l := range limits {
		hold.keys[i] = l.key(r)
	}
	for i, l := range limits {
		l.mu.Lock()
		ok := l.has(hold.keys[i])
		if ok {
			l.add(hold.keys[i], 1)
		}
		l.mu.Unlock()
		if !ok {
			hold.releaseFirst(i)
			return keyHold{}, false
		}
	}
	return hold, true
}

func (h keyHold) release() { h.releaseFirst(len(h.limits)) }

func (h keyHold) releaseFirst(n int) {
	for i := range n {
		h.limits[i].mu.Lock()
		h.limits[i].add(h.keys[i], -1)
		h.limits[i].mu.Unlock()
	}
}
