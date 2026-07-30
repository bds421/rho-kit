// Package versionrelocation decorates exact-version operations with a
// caller-owned historical-to-current provider generation resolver. Core
// unversioned operations and physical generation enumeration remain unchanged.
package versionrelocation

import (
	"context"
	"fmt"
	"io"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Resolver returns the current physical provider generation for one exact
// historical identity. Returning the input unchanged is a valid pass-through
// for generations created after the latest relocation set.
type Resolver interface {
	ResolveExactVersion(
		context.Context,
		storage.ObjectVersion,
	) (storage.ObjectVersion, error)
}

type ResolverFunc func(
	context.Context,
	storage.ObjectVersion,
) (storage.ObjectVersion, error)

func (resolve ResolverFunc) ResolveExactVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectVersion, error) {
	return resolve(ctx, object)
}

type relocatedStorage struct {
	backend  storage.Storage
	exact    storage.ExactVersionStore
	resolver Resolver
}

// New requires a generation-pinned backend and returns a transparent storage
// decorator. Capability discovery stops on the relocated ExactVersionStore
// implementation but may intentionally unwrap for unrelated capabilities and
// for physical version enumeration.
func New(
	backend storage.Storage,
	resolver Resolver,
) (storage.Storage, error) {
	if backend == nil || resolver == nil {
		return nil, fmt.Errorf(
			"%w: backend and version resolver are required",
			storage.ErrValidation,
		)
	}
	exact, ok := storage.AsExactVersionStore(backend)
	if !ok {
		return nil, fmt.Errorf(
			"%w: backend has no exact-version capability",
			storage.ErrExactVersionUnavailable,
		)
	}
	return &relocatedStorage{
		backend: backend, exact: exact, resolver: resolver,
	}, nil
}

func (relocated *relocatedStorage) Put(
	ctx context.Context,
	key string,
	source io.Reader,
	meta storage.ObjectMeta,
) error {
	return relocated.backend.Put(ctx, key, source, meta)
}

func (relocated *relocatedStorage) Get(
	ctx context.Context,
	key string,
) (io.ReadCloser, storage.ObjectMeta, error) {
	return relocated.backend.Get(ctx, key)
}

func (relocated *relocatedStorage) Delete(
	ctx context.Context,
	key string,
) error {
	return relocated.backend.Delete(ctx, key)
}

func (relocated *relocatedStorage) Exists(
	ctx context.Context,
	key string,
) (bool, error) {
	return relocated.backend.Exists(ctx, key)
}

func (relocated *relocatedStorage) CurrentVersion(
	ctx context.Context,
	key string,
) (storage.ObjectVersion, storage.ObjectMeta, error) {
	return relocated.exact.CurrentVersion(ctx, key)
}

func (relocated *relocatedStorage) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	resolved, err := relocated.resolve(ctx, object)
	if err != nil {
		return storage.ObjectMeta{}, err
	}
	return relocated.exact.StatVersion(ctx, resolved)
}

func (relocated *relocatedStorage) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	resolved, err := relocated.resolve(ctx, object)
	if err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	return relocated.exact.GetVersion(ctx, resolved)
}

func (relocated *relocatedStorage) DeleteVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) error {
	resolved, err := relocated.resolve(ctx, object)
	if err != nil {
		return err
	}
	return relocated.exact.DeleteVersion(ctx, resolved)
}

func (relocated *relocatedStorage) resolve(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectVersion, error) {
	if ctx == nil {
		return storage.ObjectVersion{}, fmt.Errorf(
			"%w: context is required",
			storage.ErrValidation,
		)
	}
	if err := object.Validate(); err != nil {
		return storage.ObjectVersion{}, err
	}
	resolved, err := relocated.resolver.ResolveExactVersion(ctx, object)
	if err != nil {
		return storage.ObjectVersion{}, fmt.Errorf(
			"storage version relocation: resolve %q: %w",
			object.Key,
			err,
		)
	}
	if err = resolved.Validate(); err != nil {
		return storage.ObjectVersion{}, fmt.Errorf(
			"%w: resolver returned an invalid generation",
			storage.ErrValidation,
		)
	}
	if resolved.Key != object.Key {
		return storage.ObjectVersion{}, fmt.Errorf(
			"%w: resolver changed the object key",
			storage.ErrValidation,
		)
	}
	return resolved, nil
}

func (relocated *relocatedStorage) Unwrap() storage.Storage {
	return relocated.backend
}

var (
	_ storage.Storage           = (*relocatedStorage)(nil)
	_ storage.ExactVersionStore = (*relocatedStorage)(nil)
	_ storage.Unwrapper         = (*relocatedStorage)(nil)
)
