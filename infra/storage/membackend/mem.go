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

	"github.com/bds421/rho-kit/infra/storage"
)

// Compile-time interface compliance checks.
var (
	_ storage.Storage = (*MemBackend)(nil)
	_ storage.Lister  = (*MemBackend)(nil)
	_ storage.Copier  = (*MemBackend)(nil)
)

type storedObject struct {
	data    []byte
	meta    storage.ObjectMeta
	modTime time.Time
}

// MemBackend is a thread-safe in-memory storage backend for testing.
type MemBackend struct {
	mu         sync.RWMutex
	objects    map[string]storedObject
	validators []storage.Validator
}

// New creates an empty MemBackend.
func New(validators ...storage.Validator) *MemBackend {
	return &MemBackend{
		objects:    make(map[string]storedObject),
		validators: validators,
	}
}

// Put stores content at key. The reader is fully consumed into memory.
func (b *MemBackend) Put(_ context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(r, &meta, b.validators)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(validated)
	if err != nil {
		return fmt.Errorf("membackend: read content: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.objects[key] = storedObject{
		data:    data,
		meta:    storage.ObjectMeta{ContentType: meta.ContentType, Size: int64(len(data)), Custom: meta.Custom},
		modTime: time.Now(),
	}
	return nil
}

// Get retrieves stored content. Returns ErrObjectNotFound if key is absent.
func (b *MemBackend) Get(_ context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	obj, ok := b.objects[key]
	if !ok {
		return nil, storage.ObjectMeta{}, fmt.Errorf("membackend: get %q: %w", key, storage.ErrObjectNotFound)
	}

	// Return a copy so callers cannot mutate stored data.
	buf := make([]byte, len(obj.data))
	copy(buf, obj.data)

	return io.NopCloser(bytes.NewReader(buf)), obj.meta, nil
}

// Delete removes an object. Returns nil if the key does not exist.
func (b *MemBackend) Delete(_ context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.objects, key)
	return nil
}

// Exists reports whether the key exists.
func (b *MemBackend) Exists(_ context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	_, ok := b.objects[key]
	return ok, nil
}

// Copy duplicates an object within the backend.
func (b *MemBackend) Copy(_ context.Context, srcKey, dstKey string) error {
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
		return fmt.Errorf("membackend: copy %q: %w", srcKey, storage.ErrObjectNotFound)
	}

	dataCopy := make([]byte, len(obj.data))
	copy(dataCopy, obj.data)

	metaCopy := storage.ObjectMeta{
		ContentType: obj.meta.ContentType,
		Size:        obj.meta.Size,
	}
	if len(obj.meta.Custom) > 0 {
		metaCopy.Custom = make(map[string]string, len(obj.meta.Custom))
		for k, v := range obj.meta.Custom {
			metaCopy.Custom[k] = v
		}
	}
	b.objects[dstKey] = storedObject{
		data:    dataCopy,
		meta:    metaCopy,
		modTime: time.Now(),
	}
	return nil
}

// List returns an iterator over objects matching the prefix.
func (b *MemBackend) List(_ context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		if prefix != "" {
			if err := storage.ValidatePrefix(prefix); err != nil {
				yield(storage.ObjectInfo{}, fmt.Errorf("membackend: %w", err))
				return
			}
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

// Len returns the number of stored objects. Useful in test assertions.
func (b *MemBackend) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.objects)
}

// Reset removes all stored objects. Useful between test cases.
func (b *MemBackend) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects = make(map[string]storedObject)
}
