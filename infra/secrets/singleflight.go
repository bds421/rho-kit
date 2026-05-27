package secrets

import "sync"

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
}

func newSingleflight() *singleflight {
	return &singleflight{m: make(map[string]*flightCall)}
}

func (sf *singleflight) do(key string, fn func() (Secret, error)) (Secret, error) {
	sf.mu.Lock()
	if call, ok := sf.m[key]; ok {
		sf.mu.Unlock()
		call.wg.Wait()
		return call.val, call.err
	}
	call := &flightCall{}
	call.wg.Add(1)
	sf.m[key] = call
	sf.mu.Unlock()

	call.val, call.err = fn()
	call.wg.Done()

	sf.mu.Lock()
	delete(sf.m, key)
	sf.mu.Unlock()

	return call.val, call.err
}
