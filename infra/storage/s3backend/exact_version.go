package s3backend

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

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
	output, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "current-version", start, s3MetricErr(err))
	if err != nil {
		if isS3NotFound(err) {
			return storage.ObjectVersion{}, storage.ObjectMeta{},
				fmt.Errorf("s3backend: current version: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectVersion{}, storage.ObjectMeta{},
			storage.WrapSafe("s3backend: current version failed", err)
	}
	version := aws.ToString(output.VersionId)
	if version == "" {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return storage.ObjectVersion{Key: key, Version: version}, s3HeadMeta(output), nil
}

func (b *Backend) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return storage.ObjectMeta{}, err
	}
	start := now()
	output, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:    aws.String(b.bucket),
		Key:       aws.String(object.Key),
		VersionId: aws.String(object.Version),
	})
	b.metrics.observeOp(b.instance, "stat-version", start, s3MetricErr(err))
	if err != nil {
		if isS3NotFound(err) {
			return storage.ObjectMeta{}, fmt.Errorf("s3backend: stat version: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectMeta{}, storage.WrapSafe("s3backend: stat version failed", err)
	}
	if actual := aws.ToString(output.VersionId); actual != object.Version {
		return storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return s3HeadMeta(output), nil
}

func (b *Backend) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	start := now()
	output, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:    aws.String(b.bucket),
		Key:       aws.String(object.Key),
		VersionId: aws.String(object.Version),
	})
	b.metrics.observeOp(b.instance, "get-version", start, s3MetricErr(err))
	if err != nil {
		if isS3NotFound(err) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("s3backend: get version: %w", storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, storage.WrapSafe("s3backend: get version failed", err)
	}
	if actual := aws.ToString(output.VersionId); actual != object.Version {
		_ = output.Body.Close()
		return nil, storage.ObjectMeta{}, storage.ErrExactVersionUnavailable
	}
	return output.Body, s3GetMeta(output), nil
}

func (b *Backend) DeleteVersion(ctx context.Context, object storage.ObjectVersion) error {
	if err := object.Validate(); err != nil {
		return err
	}
	start := now()
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket:    aws.String(b.bucket),
		Key:       aws.String(object.Key),
		VersionId: aws.String(object.Version),
	})
	b.metrics.observeOp(b.instance, "delete-version", start, s3MetricErr(err))
	if err != nil && !isS3NotFound(err) {
		return storage.WrapSafe("s3backend: delete version failed", err)
	}
	_, err = b.StatVersion(ctx, object)
	if errors.Is(err, storage.ErrObjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return storage.ErrExactVersionStillExists
}

func s3HeadMeta(output *s3.HeadObjectOutput) storage.ObjectMeta {
	meta := storage.ObjectMeta{
		ContentType: aws.ToString(output.ContentType),
		ETag:        aws.ToString(output.ETag),
		Custom:      storage.CloneCustomMeta(output.Metadata),
	}
	if output.ContentLength != nil {
		meta.Size = *output.ContentLength
	}
	if output.LastModified != nil {
		meta.LastModified = *output.LastModified
	}
	return meta
}

func s3GetMeta(output *s3.GetObjectOutput) storage.ObjectMeta {
	meta := storage.ObjectMeta{
		ContentType: aws.ToString(output.ContentType),
		ETag:        aws.ToString(output.ETag),
		Custom:      storage.CloneCustomMeta(output.Metadata),
	}
	if output.ContentLength != nil {
		meta.Size = *output.ContentLength
	}
	if output.LastModified != nil {
		meta.LastModified = *output.LastModified
	}
	return meta
}

var _ storage.ExactVersionStore = (*Backend)(nil)
