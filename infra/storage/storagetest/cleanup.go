package storagetest

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// removing records every key the suites put and deletes them when the test
// ends, so a run against a shared, long-lived backend leaves nothing behind.
type removing struct {
	storage.Storage

	mu   sync.Mutex
	keys map[string]struct{}
}

func removeWritten(t *testing.T, backend storage.Storage) storage.Storage {
	t.Helper()
	r := &removing{Storage: backend, keys: make(map[string]struct{})}
	t.Cleanup(func() {
		// The test's context has ended by now; the cleanup still needs one.
		ctx := context.WithoutCancel(t.Context())
		r.mu.Lock()
		defer r.mu.Unlock()
		for key := range r.keys {
			if err := backend.Delete(ctx, key); err != nil {
				t.Errorf("storagetest: delete %s: %v", key, err)
			}
		}
	})
	return r
}

// Put records key before writing, so a write that fails halfway is
// removed too.
func (r *removing) Put(ctx context.Context, key string, rd io.Reader, meta storage.ObjectMeta) error {
	if key != "" {
		r.mu.Lock()
		r.keys[key] = struct{}{}
		r.mu.Unlock()
	}
	return r.Storage.Put(ctx, key, rd, meta)
}
