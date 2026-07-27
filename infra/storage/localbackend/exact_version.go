package localbackend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// ImmutableBackend adds generation-pinned access to a local backend whose root
// is exclusively owned by this process and whose keys cannot be overwritten
// while present. It is intended for local development and single-process
// Docker deployments, not a shared network filesystem.
type ImmutableBackend struct {
	backend *Backend
	markers *Backend
	mu      sync.RWMutex
}

// NewImmutable creates an immutable-key local backend. The caller must give
// this instance exclusive write ownership of dir.
func NewImmutable(dir string, options ...Option) (*ImmutableBackend, error) {
	backend, err := New(dir, options...)
	if err != nil {
		return nil, err
	}
	markers, err := New(backend.root + ".rho-exact-versions")
	if err != nil {
		return nil, fmt.Errorf("localbackend: create exact-version marker store: %w", err)
	}
	return &ImmutableBackend{backend: backend, markers: markers}, nil
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
	if err = backend.markers.Put(ctx, key, strings.NewReader(version), storage.ObjectMeta{}); err != nil {
		return fmt.Errorf("localbackend: persist object version: %w", err)
	}
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
	if err = backend.markers.Put(
		ctx,
		destinationKey,
		strings.NewReader(version),
		storage.ObjectMeta{},
	); err != nil {
		return fmt.Errorf("localbackend: persist copied object version: %w", err)
	}
	return backend.backend.Copy(ctx, sourceKey, destinationKey)
}

func (backend *ImmutableBackend) CurrentVersion(
	ctx context.Context,
	key string,
) (storage.ObjectVersion, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	version, meta, err := backend.readVersion(ctx, key, "")
	if err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	return storage.ObjectVersion{Key: key, Version: version}, meta, nil
}

func (backend *ImmutableBackend) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return storage.ObjectMeta{}, err
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	_, meta, err := backend.readVersion(ctx, object.Key, object.Version)
	return meta, err
}

func (backend *ImmutableBackend) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	version, err := backend.markerVersion(ctx, object.Key)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			exists, existsErr := backend.backend.Exists(ctx, object.Key)
			if existsErr != nil {
				return nil, storage.ObjectMeta{}, existsErr
			}
			if exists {
				return nil, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
			}
		}
		return nil, storage.ObjectMeta{}, err
	}
	if version != object.Version {
		return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get version: %w", storage.ErrObjectNotFound)
	}
	return backend.backend.Get(ctx, object.Key)
}

func (backend *ImmutableBackend) DeleteVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) error {
	if err := object.Validate(); err != nil {
		return err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	version, err := backend.markerVersion(ctx, object.Key)
	if errors.Is(err, storage.ErrObjectNotFound) {
		exists, existsErr := backend.backend.Exists(ctx, object.Key)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			return storage.ErrExactVersionUnavailable
		}
		return nil
	}
	if err != nil {
		return err
	}
	if version != object.Version {
		return nil
	}
	if err = backend.backend.Delete(ctx, object.Key); err != nil {
		return err
	}
	exists, err := backend.backend.Exists(ctx, object.Key)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return storage.ErrExactVersionStillExists
}

func (backend *ImmutableBackend) Close() error {
	return errors.Join(backend.backend.Close(), backend.markers.Close())
}

func (backend *ImmutableBackend) readVersion(
	ctx context.Context,
	key string,
	required string,
) (string, storage.ObjectMeta, error) {
	version, err := backend.markerVersion(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			exists, existsErr := backend.backend.Exists(ctx, key)
			if existsErr != nil {
				return "", storage.ObjectMeta{}, existsErr
			}
			if exists {
				return "", storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
			}
		}
		return "", storage.ObjectMeta{}, err
	}
	if required != "" && version != required {
		return "", storage.ObjectMeta{}, fmt.Errorf("localbackend: stat version: %w", storage.ErrObjectNotFound)
	}
	meta, err := backend.backend.Stat(ctx, key)
	return version, meta, err
}

func (backend *ImmutableBackend) markerVersion(ctx context.Context, key string) (string, error) {
	reader, _, err := backend.markers.Get(ctx, key)
	if err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, maxImmutableVersionBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return "", localFileError("read object version", readErr)
	}
	if closeErr != nil {
		return "", localFileError("close object version", closeErr)
	}
	if len(body) > maxImmutableVersionBytes {
		return "", storage.ErrExactVersionUnavailable
	}
	version := string(body)
	if err = (storage.ObjectVersion{Key: key, Version: version}).Validate(); err != nil {
		return "", storage.ErrExactVersionUnavailable
	}
	return version, nil
}

const maxImmutableVersionBytes = 128

func newImmutableVersion() (string, error) {
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return "", fmt.Errorf("localbackend: generate object version: %w", err)
	}
	return "iv1-" + hex.EncodeToString(generation[:]), nil
}

var (
	_ storage.Storage           = (*ImmutableBackend)(nil)
	_ storage.Statter           = (*ImmutableBackend)(nil)
	_ storage.Lister            = (*ImmutableBackend)(nil)
	_ storage.Copier            = (*ImmutableBackend)(nil)
	_ storage.ExactVersionStore = (*ImmutableBackend)(nil)
)
