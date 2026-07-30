package s3backend

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

type exactVersionListClient interface {
	ListObjectVersions(
		context.Context,
		*s3.ListObjectVersionsInput,
		...func(*s3.Options),
	) (*s3.ListObjectVersionsOutput, error)
}

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

func (b *Backend) Versions(
	ctx context.Context,
	key string,
	limit int,
) ([]storage.ObjectVersion, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := storage.ValidateExactVersionListLimit(limit); err != nil {
		return nil, err
	}
	client, ok := b.client.(exactVersionListClient)
	if !ok {
		return nil, storage.ErrExactVersionUnavailable
	}
	result := make([]storage.ObjectVersion, 0, min(limit, 16))
	var keyMarker, versionMarker *string
	seenEntries := 0
	for {
		pageSize := int32(min(1000, limit-len(result)+1))
		output, err := client.ListObjectVersions(
			ctx,
			&s3.ListObjectVersionsInput{
				Bucket:          aws.String(b.bucket),
				Prefix:          aws.String(key),
				KeyMarker:       keyMarker,
				VersionIdMarker: versionMarker,
				MaxKeys:         aws.Int32(pageSize),
			},
		)
		if err != nil {
			return nil, storage.WrapSafe(
				"s3backend: list exact versions failed",
				err,
			)
		}
		for _, marker := range output.DeleteMarkers {
			if aws.ToString(marker.Key) == key {
				seenEntries++
			}
		}
		for _, version := range output.Versions {
			if aws.ToString(version.Key) == key {
				seenEntries++
			}
		}
		if seenEntries > limit {
			return nil, storage.ErrBatchTooLarge
		}
		for _, version := range output.Versions {
			if aws.ToString(version.Key) != key {
				continue
			}
			versionID := aws.ToString(version.VersionId)
			object := storage.ObjectVersion{Key: key, Version: versionID}
			if object.Validate() != nil {
				return nil, storage.ErrExactVersionUnavailable
			}
			result = append(result, object)
			if len(result) > limit {
				return nil, storage.ErrBatchTooLarge
			}
		}
		if !aws.ToBool(output.IsTruncated) {
			break
		}
		nextKey := aws.ToString(output.NextKeyMarker)
		nextVersion := aws.ToString(output.NextVersionIdMarker)
		if nextKey > key {
			break
		}
		if nextKey == "" || nextVersion == "" ||
			keyMarker != nil && nextKey == *keyMarker &&
				versionMarker != nil && nextVersion == *versionMarker {
			return nil, storage.ErrExactVersionUnavailable
		}
		keyMarker, versionMarker = aws.String(nextKey), aws.String(nextVersion)
	}
	return result, nil
}

func (b *Backend) VersionsByPrefix(
	ctx context.Context,
	prefix string,
	limit int,
) ([]storage.ObjectVersion, error) {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	if err := storage.ValidateExactVersionListLimit(limit); err != nil {
		return nil, err
	}
	client, ok := b.client.(exactVersionListClient)
	if !ok {
		return nil, storage.ErrExactVersionUnavailable
	}
	result := make([]storage.ObjectVersion, 0, min(limit, 16))
	var keyMarker, versionMarker *string
	seenEntries := 0
	for {
		output, err := client.ListObjectVersions(
			ctx,
			&s3.ListObjectVersionsInput{
				Bucket:          aws.String(b.bucket),
				Prefix:          aws.String(prefix),
				KeyMarker:       keyMarker,
				VersionIdMarker: versionMarker,
				MaxKeys:         aws.Int32(int32(min(1000, limit-seenEntries+1))),
			},
		)
		if err != nil {
			return nil, storage.WrapSafe(
				"s3backend: list exact versions by prefix failed",
				err,
			)
		}
		for _, marker := range output.DeleteMarkers {
			if strings.HasPrefix(aws.ToString(marker.Key), prefix) {
				seenEntries++
			}
		}
		for _, version := range output.Versions {
			key := aws.ToString(version.Key)
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			seenEntries++
			object := storage.ObjectVersion{
				Key: key,
				Version: aws.ToString(
					version.VersionId,
				),
			}
			if object.Validate() != nil {
				return nil, storage.ErrExactVersionUnavailable
			}
			result = append(result, object)
		}
		if seenEntries > limit || len(result) > limit {
			return nil, storage.ErrBatchTooLarge
		}
		if !aws.ToBool(output.IsTruncated) {
			break
		}
		nextKey := aws.ToString(output.NextKeyMarker)
		nextVersion := aws.ToString(output.NextVersionIdMarker)
		if nextKey == "" ||
			keyMarker != nil && nextKey == *keyMarker &&
				versionMarker != nil && nextVersion == *versionMarker {
			return nil, storage.ErrExactVersionUnavailable
		}
		keyMarker, versionMarker = aws.String(nextKey), aws.String(nextVersion)
	}
	return result, nil
}

func (b *Backend) VersionsPage(
	ctx context.Context,
	prefix string,
	options storage.ExactVersionPageOptions,
) (storage.ExactVersionPage, error) {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return storage.ExactVersionPage{}, err
	}
	if err := storage.ValidateExactVersionPageOptions(options); err != nil {
		return storage.ExactVersionPage{}, err
	}
	client, ok := b.client.(exactVersionListClient)
	if !ok {
		return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
	}
	keyMarker, versionMarker, err := decodeExactVersionPageCursor(
		prefix,
		options.Cursor,
	)
	if err != nil {
		return storage.ExactVersionPage{}, err
	}
	output, err := client.ListObjectVersions(
		ctx,
		&s3.ListObjectVersionsInput{
			Bucket:          aws.String(b.bucket),
			Prefix:          aws.String(prefix),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
			MaxKeys:         aws.Int32(int32(options.Limit)),
		},
	)
	if err != nil {
		return storage.ExactVersionPage{}, storage.WrapSafe(
			"s3backend: page exact versions failed",
			err,
		)
	}
	if output == nil {
		return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
	}
	seenEntries := len(output.DeleteMarkers) + len(output.Versions)
	if seenEntries > options.Limit {
		return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
	}
	page := storage.ExactVersionPage{
		Objects: make([]storage.ObjectVersion, 0, len(output.Versions)),
	}
	for _, marker := range output.DeleteMarkers {
		if !strings.HasPrefix(aws.ToString(marker.Key), prefix) {
			return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
		}
	}
	for _, version := range output.Versions {
		object := storage.ObjectVersion{
			Key:     aws.ToString(version.Key),
			Version: aws.ToString(version.VersionId),
		}
		if !strings.HasPrefix(object.Key, prefix) || object.Validate() != nil {
			return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
		}
		page.Objects = append(page.Objects, object)
	}
	if !aws.ToBool(output.IsTruncated) {
		return page, nil
	}
	nextKey := aws.ToString(output.NextKeyMarker)
	nextVersion := aws.ToString(output.NextVersionIdMarker)
	if nextKey == "" ||
		nextKey == aws.ToString(keyMarker) &&
			nextVersion == aws.ToString(versionMarker) {
		return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
	}
	page.NextCursor, err = encodeExactVersionPageCursor(
		prefix,
		nextKey,
		nextVersion,
	)
	if err != nil {
		return storage.ExactVersionPage{}, err
	}
	page.Truncated = true
	return page, nil
}

const exactVersionPageCursorKind = "rho-kit-s3-exact-page-v1"

func encodeExactVersionPageCursor(
	prefix string,
	key string,
	version string,
) (storage.ExactVersionCursor, error) {
	if !strings.HasPrefix(key, prefix) {
		return "", storage.ErrExactVersionUnavailable
	}
	if version == "" {
		if err := storage.ValidateKey(key); err != nil {
			return "", storage.ErrExactVersionUnavailable
		}
	} else if err := (storage.ObjectVersion{
		Key: key, Version: version,
	}).Validate(); err != nil {
		return "", storage.ErrExactVersionUnavailable
	}
	raw := strings.Join(
		[]string{exactVersionPageCursorKind, prefix, key, version},
		"\x00",
	)
	return storage.ExactVersionCursor(
		base64.RawURLEncoding.EncodeToString([]byte(raw)),
	), nil
}

func decodeExactVersionPageCursor(
	prefix string,
	cursor storage.ExactVersionCursor,
) (keyMarker *string, versionMarker *string, err error) {
	if cursor == "" {
		return nil, nil, nil
	}
	raw, decodeErr := base64.RawURLEncoding.DecodeString(string(cursor))
	if decodeErr != nil {
		return nil, nil, storage.ErrExactVersionUnavailable
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 4 ||
		parts[0] != exactVersionPageCursorKind ||
		parts[1] != prefix ||
		!strings.HasPrefix(parts[2], prefix) {
		return nil, nil, storage.ErrExactVersionUnavailable
	}
	if parts[3] == "" {
		if validateErr := storage.ValidateKey(parts[2]); validateErr != nil {
			return nil, nil, storage.ErrExactVersionUnavailable
		}
		return aws.String(parts[2]), nil, nil
	}
	if validateErr := (storage.ObjectVersion{
		Key: parts[2], Version: parts[3],
	}).Validate(); validateErr != nil {
		return nil, nil, storage.ErrExactVersionUnavailable
	}
	return aws.String(parts[2]), aws.String(parts[3]), nil
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
var _ storage.ExactVersionLister = (*Backend)(nil)
var _ storage.ExactVersionPrefixLister = (*Backend)(nil)
var _ storage.ExactVersionPageLister = (*Backend)(nil)
