package gcsbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	gcsstorage "cloud.google.com/go/storage"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

func (b *Backend) CurrentVersion(
	ctx context.Context,
	key string,
) (storage.ObjectVersion, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	start := now()
	attrs, err := b.bucket.Object(key).Attrs(ctx)
	b.metrics.observeOp(b.instance, "current-version", start, gcsMetricErr(err))
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return storage.ObjectVersion{}, storage.ObjectMeta{},
				fmt.Errorf("gcsbackend: current version: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectVersion{}, storage.ObjectMeta{},
			storage.WrapSafe("gcsbackend: current version failed", err)
	}
	if attrs.Generation <= 0 {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return storage.ObjectVersion{
		Key: key, Version: strconv.FormatInt(attrs.Generation, 10),
	}, gcsObjectMeta(attrs), nil
}

func (b *Backend) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	generation, err := gcsGeneration(object)
	if err != nil {
		return storage.ObjectMeta{}, err
	}
	start := now()
	attrs, err := b.bucket.Object(object.Key).Generation(generation).Attrs(ctx)
	b.metrics.observeOp(b.instance, "stat-version", start, gcsMetricErr(err))
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return storage.ObjectMeta{}, fmt.Errorf("gcsbackend: stat version: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectMeta{}, storage.WrapSafe("gcsbackend: stat version failed", err)
	}
	if attrs.Generation != generation {
		return storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return gcsObjectMeta(attrs), nil
}

func (b *Backend) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	generation, err := gcsGeneration(object)
	if err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	handle := b.bucket.Object(object.Key).Generation(generation)
	start := now()
	attrs, err := handle.Attrs(ctx)
	if err == nil {
		var reader io.ReadCloser
		reader, err = handle.NewReader(ctx)
		if err == nil {
			b.metrics.observeOp(b.instance, "get-version", start, nil)
			return reader, gcsObjectMeta(attrs), nil
		}
	}
	b.metrics.observeOp(b.instance, "get-version", start, gcsMetricErr(err))
	if errors.Is(err, gcsstorage.ErrObjectNotExist) {
		return nil, storage.ObjectMeta{}, fmt.Errorf("gcsbackend: get version: %w", storage.ErrObjectNotFound)
	}
	return nil, storage.ObjectMeta{}, storage.WrapSafe("gcsbackend: get version failed", err)
}

func (b *Backend) DeleteVersion(ctx context.Context, object storage.ObjectVersion) error {
	generation, err := gcsGeneration(object)
	if err != nil {
		return err
	}
	handle := b.bucket.Object(object.Key).Generation(generation)
	start := now()
	err = handle.Delete(ctx)
	b.metrics.observeOp(b.instance, "delete-version", start, gcsMetricErr(err))
	if err != nil && !errors.Is(err, gcsstorage.ErrObjectNotExist) {
		return storage.WrapSafe("gcsbackend: delete version failed", err)
	}
	_, err = handle.Attrs(ctx)
	if errors.Is(err, gcsstorage.ErrObjectNotExist) {
		return nil
	}
	if err != nil {
		return storage.WrapSafe("gcsbackend: verify deleted version failed", err)
	}
	return storage.ErrExactVersionStillExists
}

func gcsGeneration(object storage.ObjectVersion) (int64, error) {
	if err := object.Validate(); err != nil {
		return 0, err
	}
	generation, err := strconv.ParseInt(object.Version, 10, 64)
	if err != nil || generation <= 0 || strconv.FormatInt(generation, 10) != object.Version {
		return 0, fmt.Errorf("%w: GCS generation is invalid", storage.ErrValidation)
	}
	return generation, nil
}

func gcsObjectMeta(attrs *gcsstorage.ObjectAttrs) storage.ObjectMeta {
	return storage.ObjectMeta{
		ContentType:  attrs.ContentType,
		Size:         attrs.Size,
		ETag:         attrs.Etag,
		LastModified: attrs.Updated,
		Custom:       storage.CloneCustomMeta(attrs.Metadata),
	}
}

var _ storage.ExactVersionStore = (*Backend)(nil)
