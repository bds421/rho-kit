package secrets

import (
	"context"
	"sync"
)

// singleflight coalesces concurrent calls for the same key so a stampede
// of cache-miss Gets produces ONE upstream fetch. We embed our own
// minimal singleflight here rather than depend on golang.org/x/sync to
// keep the umbrella module dependency-free (per docs/audit/dependency-
// allowlist.txt review).
type singleflight struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

type flightCall struct {
	wg  sync.WaitGroup
	val Secret
	err error
	// panicValue holds a value recovered from a panicking fn so the
	// leader and all waiters can re-panic instead of returning a zero
	// Secret. Without recover, a panicking fn would leave the call in
	// the map and block every future do() for the key forever.
	panicValue any
}

func newSingleflight() *singleflight {
	return &singleflight{m: make(map[string]*flightCall)}
}

// do coalesces concurrent fn executions for key. Waiters honour ctx so a
// caller with a short deadline can return early while the shared fetch
// continues on the leader's detached context (leader still runs fn to
// completion so other waiters are not poisoned).
func (sf *singleflight) do(ctx context.Context, key string, fn func() (Secret, error)) (Secret, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sf.mu.Lock()
	if call, ok := sf.m[key]; ok {
		sf.mu.Unlock()
		return call.wait(ctx)
	}
	call := &flightCall{}
	call.wg.Add(1)
	sf.m[key] = call
	sf.mu.Unlock()

	// Run fn under recover so a panic does not poison the key (wg never
	// Done, map entry never deleted), which would deadlock every future
	// do() for this key in call.wg.Wait(). Cleanup and wg.Done run via
	// defer regardless of panic; the recovered value is re-panicked to
	// the leader and propagated to any waiters.
	func() {
		defer func() {
			if r := recover(); r != nil {
				call.panicValue = r
			}
			sf.mu.Lock()
			delete(sf.m, key)
			sf.mu.Unlock()
			call.wg.Done()
		}()
		call.val, call.err = fn()
	}()

	if call.panicValue != nil {
		panic(call.panicValue)
	}
	// Leader also observes ctx so a cancelled leader does not return a
	// success it no longer wants, even though the fetch already completed.
	if err := ctx.Err(); err != nil {
		return Secret{}, err
	}
	return call.val, call.err
}

func (call *flightCall) wait(ctx context.Context) (Secret, error) {
	done := make(chan struct{})
	go func() {
		call.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return Secret{}, ctx.Err()
	case <-done:
		if call.panicValue != nil {
			panic(call.panicValue)
		}
		return call.val, call.err
	}
}
