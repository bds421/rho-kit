package azurebackend

import (
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

func (b *Backend) CurrentVersion(
	ctx context.Context,
	key string,
) (storage.ObjectVersion, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	client, ok := b.client.(versionedBlobClient)
	if !ok {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	start := now()
	response, err := client.GetPropertiesVersion(ctx, key, "")
	b.metrics.observeOp(b.instance, "current-version", start, azureMetricErr(err))
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return storage.ObjectVersion{}, storage.ObjectMeta{},
				fmt.Errorf("azurebackend: current version: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectVersion{}, storage.ObjectMeta{},
			storage.WrapSafe("azurebackend: current version failed", err)
	}
	if response.VersionID == nil || *response.VersionID == "" {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return storage.ObjectVersion{Key: key, Version: *response.VersionID},
		azurePropertiesMeta(response), nil
}

func (b *Backend) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return storage.ObjectMeta{}, err
	}
	client, ok := b.client.(versionedBlobClient)
	if !ok {
		return storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	start := now()
	response, err := client.GetPropertiesVersion(ctx, object.Key, object.Version)
	b.metrics.observeOp(b.instance, "stat-version", start, azureMetricErr(err))
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return storage.ObjectMeta{}, fmt.Errorf("azurebackend: stat version: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectMeta{}, storage.WrapSafe("azurebackend: stat version failed", err)
	}
	if response.VersionID == nil || *response.VersionID != object.Version {
		return storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return azurePropertiesMeta(response), nil
}

func (b *Backend) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	client, ok := b.client.(versionedBlobClient)
	if !ok {
		return nil, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	start := now()
	response, err := client.DownloadVersion(ctx, object.Key, object.Version)
	b.metrics.observeOp(b.instance, "get-version", start, azureMetricErr(err))
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("azurebackend: get version: %w", storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, storage.WrapSafe("azurebackend: get version failed", err)
	}
	if response.VersionID == nil || *response.VersionID != object.Version {
		_ = response.Body.Close()
		return nil, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return response.Body, azureDownloadMeta(response), nil
}

func (b *Backend) DeleteVersion(ctx context.Context, object storage.ObjectVersion) error {
	if err := object.Validate(); err != nil {
		return err
	}
	client, ok := b.client.(versionedBlobClient)
	if !ok {
		return storage.ErrExactVersionUnavailable
	}
	start := now()
	_, err := client.DeleteVersion(ctx, object.Key, object.Version)
	b.metrics.observeOp(b.instance, "delete-version", start, azureMetricErr(err))
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return storage.WrapSafe("azurebackend: delete version failed", err)
	}
	_, err = client.GetPropertiesVersion(ctx, object.Key, object.Version)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil
	}
	if err != nil {
		return storage.WrapSafe("azurebackend: verify deleted version failed", err)
	}
	return storage.ErrExactVersionStillExists
}

func azurePropertiesMeta(response blob.GetPropertiesResponse) storage.ObjectMeta {
	meta := storage.ObjectMeta{Custom: fromAzureMetadata(response.Metadata)}
	if response.ContentType != nil {
		meta.ContentType = *response.ContentType
	}
	if response.ContentLength != nil {
		meta.Size = *response.ContentLength
	}
	if response.ETag != nil {
		meta.ETag = string(*response.ETag)
	}
	if response.LastModified != nil {
		meta.LastModified = *response.LastModified
	}
	return meta
}

func azureDownloadMeta(response blob.DownloadStreamResponse) storage.ObjectMeta {
	meta := storage.ObjectMeta{Custom: fromAzureMetadata(response.Metadata)}
	if response.ContentType != nil {
		meta.ContentType = *response.ContentType
	}
	if response.ContentLength != nil {
		meta.Size = *response.ContentLength
	}
	if response.ETag != nil {
		meta.ETag = string(*response.ETag)
	}
	if response.LastModified != nil {
		meta.LastModified = *response.LastModified
	}
	return meta
}

var _ storage.ExactVersionStore = (*Backend)(nil)
