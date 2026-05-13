package membackend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance checks.
var (
	_ storage.Storage = (*Backend)(nil)
	_ storage.Lister  = (*Backend)(nil)
	_ storage.Copier  = (*Backend)(nil)
)

type storedObject struct {
	data    []byte
	meta    storage.ObjectMeta
	modTime time.Time
}

// Backend is a thread-safe in-memory storage backend for testing.
type Backend struct {
	mu         sync.RWMutex
	objects    map[string]storedObject
	validators []storage.Validator
}

// New creates an empty Backend.
func New(validators ...storage.Validator) *Backend {
	return &Backend{
		objects:    make(map[string]storedObject),
		validators: storage.CloneValidators(validators...),
	}
}

// Put stores content at key. The reader is fully consumed into memory.
// Honours context cancellation symmetrically with remote backends: a
// cancelled ctx is rejected at entry and again before the map write.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(ctx, r, &meta, b.validators)
	if err != nil {
		return err
	}
	if len(b.validators) > 0 {
		defer func() { _ = storage.CloseValidatedReader(validated) }()
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return err
	}

	data, err := io.ReadAll(validated)
	if err != nil {
		return storage.WrapSafe("membackend: read content failed", err)
	}

	if err := ctxErr(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.objects[key] = storedObject{
		data:    data,
		meta:    storage.ObjectMeta{ContentType: meta.ContentType, Size: int64(len(data)), Custom: storage.CloneCustomMeta(meta.Custom)},
		modTime: time.Now(),
	}
	return nil
}

// Get retrieves stored content. Returns ErrObjectNotFound if key is absent.
// Honours context cancellation: ctx.Err is checked at entry.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	obj, ok := b.objects[key]
	if !ok {
		return nil, storage.ObjectMeta{}, fmt.Errorf("membackend: get: %w", storage.ErrObjectNotFound)
	}

	// Return a copy so callers cannot mutate stored data.
	buf := make([]byte, len(obj.data))
	copy(buf, obj.data)

	return io.NopCloser(bytes.NewReader(buf)), storage.CloneObjectMeta(obj.meta), nil
}

// Delete removes an object. Returns nil if the key does not exist.
// Honours context cancellation: ctx.Err is checked at entry.
func (b *Backend) Delete(ctx context.Context, key string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.objects, key)
	return nil
}

// Exists reports whether the key exists.
// Honours context cancellation: ctx.Err is checked at entry.
func (b *Backend) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctxErr(ctx); err != nil {
		return false, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	_, ok := b.objects[key]
	return ok, nil
}

// Close releases any resources. Memory backend has none, so this is a
// documented no-op present only for uniform interface implementation.
func (b *Backend) Close() error { return nil }

// Copy duplicates an object within the backend.
// Honours context cancellation: ctx.Err is checked at entry.
func (b *Backend) Copy(ctx context.Context, srcKey, dstKey string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := storage.ValidateKey(srcKey); err != nil {
		return fmt.Errorf("membackend: copy: invalid source key: %w", err)
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return fmt.Errorf("membackend: copy: invalid destination key: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	obj, ok := b.objects[srcKey]
	if !ok {
		return fmt.Errorf("membackend: copy: %w", storage.ErrObjectNotFound)
	}

	dataCopy := make([]byte, len(obj.data))
	copy(dataCopy, obj.data)

	b.objects[dstKey] = storedObject{
		data:    dataCopy,
		meta:    storage.CloneObjectMeta(obj.meta),
		modTime: time.Now(),
	}
	return nil
}

// List returns an iterator over objects matching the prefix.
// Honours context cancellation symmetrically with remote backends:
// ctx.Err is checked at entry and on every yielded item, so a long
// scan over a large keyset terminates promptly when the caller cancels.
func (b *Backend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		if err := ctxErr(ctx); err != nil {
			yield(storage.ObjectInfo{}, err)
			return
		}
		if err := storage.ValidatePrefix(prefix); err != nil {
			yield(storage.ObjectInfo{}, fmt.Errorf("membackend: %w", err))
			return
		}
		if err := storage.ValidateListOptions(opts); err != nil {
			yield(storage.ObjectInfo{}, fmt.Errorf("membackend: %w", err))
			return
		}

		b.mu.RLock()
		// Collect matching keys in sorted order for deterministic output.
		keys := make([]string, 0, len(b.objects))
		for k := range b.objects {
			if prefix == "" || strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		b.mu.RUnlock()

		sort.Strings(keys)
		count := 0

		for _, key := range keys {
			if err := ctxErr(ctx); err != nil {
				yield(storage.ObjectInfo{}, err)
				return
			}
			if opts.StartAfter != "" && key <= opts.StartAfter {
				continue
			}

			b.mu.RLock()
			obj, ok := b.objects[key]
			b.mu.RUnlock()
			if !ok {
				continue // deleted between snapshot and iteration
			}

			count++
			info := storage.ObjectInfo{
				Key:         key,
				Size:        int64(len(obj.data)),
				ContentType: obj.meta.ContentType,
				ModTime:     obj.modTime,
			}
			if !yield(info, nil) {
				return
			}
			if opts.MaxKeys > 0 && count >= opts.MaxKeys {
				return
			}
		}
	}
}

// ctxErr returns ctx.Err() for non-nil ctx, or nil otherwise.
// Matches the kit-wide convention: nil ctx is treated as
// context.Background() rather than rejected.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Len returns the number of stored objects. Useful in test assertions.
func (b *Backend) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.objects)
}

// Reset removes all stored objects. Useful between test cases.
func (b *Backend) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects = make(map[string]storedObject)
}
