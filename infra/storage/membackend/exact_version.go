package membackend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"iter"
	"sync"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// ImmutableBackend exposes generation-pinned operations for an in-memory
// keyspace whose writes cannot replace an existing key. The wrapper lock makes
// compare-and-delete atomic with every write and delete through this instance.
type ImmutableBackend struct {
	backend  *Backend
	mu       sync.RWMutex
	versions map[string]string
}

// NewImmutable creates an in-memory immutable-key backend. A key may be
// created again after deletion, but it cannot be overwritten while present.
func NewImmutable(validators ...storage.Validator) *ImmutableBackend {
	return &ImmutableBackend{
		backend:  New(validators...),
		versions: make(map[string]string),
	}
}

func (backend *ImmutableBackend) Put(
	ctx context.Context,
	key string,
	reader io.Reader,
	meta storage.ObjectMeta,
) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	exists, err := backend.backend.Exists(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: immutable storage key already exists", storage.ErrValidation)
	}
	version, err := newImmutableVersion()
	if err != nil {
		return err
	}
	backend.versions[key] = version
	return backend.backend.Put(ctx, key, reader, meta)
}

func (backend *ImmutableBackend) Get(
	ctx context.Context,
	key string,
) (io.ReadCloser, storage.ObjectMeta, error) {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	return backend.backend.Get(ctx, key)
}

func (backend *ImmutableBackend) Delete(ctx context.Context, key string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.backend.Delete(ctx, key)
}

func (backend *ImmutableBackend) Exists(ctx context.Context, key string) (bool, error) {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	return backend.backend.Exists(ctx, key)
}

func (backend *ImmutableBackend) Stat(ctx context.Context, key string) (storage.ObjectMeta, error) {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	return backend.backend.Stat(ctx, key)
}

func (backend *ImmutableBackend) List(
	ctx context.Context,
	prefix string,
	options storage.ListOptions,
) iter.Seq2[storage.ObjectInfo, error] {
	return backend.backend.List(ctx, prefix, options)
}

func (backend *ImmutableBackend) Copy(ctx context.Context, sourceKey, destinationKey string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	exists, err := backend.backend.Exists(ctx, destinationKey)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: immutable storage key already exists", storage.ErrValidation)
	}
	version, err := newImmutableVersion()
	if err != nil {
		return err
	}
	backend.versions[destinationKey] = version
	return backend.backend.Copy(ctx, sourceKey, destinationKey)
}

func (backend *ImmutableBackend) CurrentVersion(
	ctx context.Context,
	key string,
) (storage.ObjectVersion, storage.ObjectMeta, error) {
	if err := ctxErr(ctx); err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	object, ok := backend.object(key)
	if !ok {
		return storage.ObjectVersion{}, storage.ObjectMeta{},
			fmt.Errorf("membackend: current version: %w", storage.ErrObjectNotFound)
	}
	version := backend.versions[key]
	if version == "" {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return storage.ObjectVersion{Key: key, Version: version}, memoryMeta(object), nil
}

func (backend *ImmutableBackend) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	if err := contextAndVersion(ctx, object); err != nil {
		return storage.ObjectMeta{}, err
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	stored, ok := backend.object(object.Key)
	if !ok || backend.versions[object.Key] != object.Version {
		return storage.ObjectMeta{}, fmt.Errorf("membackend: stat version: %w", storage.ErrObjectNotFound)
	}
	return memoryMeta(stored), nil
}

func (backend *ImmutableBackend) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := contextAndVersion(ctx, object); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	stored, ok := backend.object(object.Key)
	if !ok || backend.versions[object.Key] != object.Version {
		return nil, storage.ObjectMeta{}, fmt.Errorf("membackend: get version: %w", storage.ErrObjectNotFound)
	}
	body := append([]byte(nil), stored.data...)
	return io.NopCloser(bytes.NewReader(body)), memoryMeta(stored), nil
}

func (backend *ImmutableBackend) DeleteVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) error {
	if err := contextAndVersion(ctx, object); err != nil {
		return err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	_, ok := backend.object(object.Key)
	if !ok || backend.versions[object.Key] != object.Version {
		return nil
	}
	if err := backend.backend.Delete(ctx, object.Key); err != nil {
		return err
	}
	if _, stillPresent := backend.object(object.Key); stillPresent {
		return storage.ErrExactVersionStillExists
	}
	return nil
}

func (backend *ImmutableBackend) Close() error {
	return backend.backend.Close()
}

func (backend *ImmutableBackend) object(key string) (storedObject, bool) {
	backend.backend.mu.RLock()
	defer backend.backend.mu.RUnlock()
	value, ok := backend.backend.objects[key]
	return value, ok
}

func memoryMeta(object storedObject) storage.ObjectMeta {
	meta := storage.CloneObjectMeta(object.meta)
	meta.Size = int64(len(object.data))
	meta.LastModified = object.modTime
	return meta
}

func newImmutableVersion() (string, error) {
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return "", fmt.Errorf("membackend: generate object version: %w", err)
	}
	return "iv1-" + hex.EncodeToString(generation[:]), nil
}

func contextAndVersion(ctx context.Context, object storage.ObjectVersion) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	return object.Validate()
}

var (
	_ storage.Storage           = (*ImmutableBackend)(nil)
	_ storage.Statter           = (*ImmutableBackend)(nil)
	_ storage.Lister            = (*ImmutableBackend)(nil)
	_ storage.Copier            = (*ImmutableBackend)(nil)
	_ storage.ExactVersionStore = (*ImmutableBackend)(nil)
)
