package s3backend

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

const MaxMultipartPartBytes int64 = 64 << 20

func (b *Backend) multipartClient() (MultipartClient, error) {
	client, ok := b.client.(MultipartClient)
	if !ok {
		return nil, fmt.Errorf("s3backend: configured client does not implement multipart operations")
	}
	return client, nil
}

func (b *Backend) InitUpload(ctx context.Context, key string, meta storage.ObjectMeta) (storage.MultipartUpload, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.MultipartUpload{}, err
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return storage.MultipartUpload{}, err
	}
	if len(b.validators) != 0 {
		return storage.MultipartUpload{}, fmt.Errorf("%w: multipart upload requires caller-side whole-object validation", storage.ErrValidation)
	}
	client, err := b.multipartClient()
	if err != nil {
		return storage.MultipartUpload{}, err
	}
	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(b.bucket), Key: aws.String(key), ContentType: aws.String(contentType),
		Metadata: storage.CloneCustomMeta(meta.Custom), ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	}
	if err := applyMultipartSSE(input, b.cfg); err != nil {
		return storage.MultipartUpload{}, err
	}
	start := now()
	output, callErr := client.CreateMultipartUpload(ctx, input)
	b.metrics.observeOp(b.instance, "multipart_init", start, callErr)
	if callErr != nil {
		return storage.MultipartUpload{}, storage.WrapSafe("s3backend: initialize multipart upload failed", callErr)
	}
	uploadID := strings.TrimSpace(aws.ToString(output.UploadId))
	if uploadID == "" {
		return storage.MultipartUpload{}, fmt.Errorf("s3backend: initialize multipart upload returned an empty upload id")
	}
	return storage.MultipartUpload{Key: key, UploadID: uploadID}, nil
}

func (b *Backend) UploadPart(ctx context.Context, upload storage.MultipartUpload, partNumber int, reader io.Reader) (storage.PartInfo, error) {
	if err := validateMultipartUpload(upload); err != nil {
		return storage.PartInfo{}, err
	}
	if partNumber < 1 || partNumber > 10000 || reader == nil {
		return storage.PartInfo{}, fmt.Errorf("%w: multipart part number or reader is invalid", storage.ErrValidation)
	}
	client, err := b.multipartClient()
	if err != nil {
		return storage.PartInfo{}, err
	}
	part, size, checksum, cleanup, err := spoolMultipartPart(reader)
	if err != nil {
		return storage.PartInfo{}, err
	}
	defer cleanup()
	input := &s3.UploadPartInput{
		Bucket: aws.String(b.bucket), Key: aws.String(upload.Key), UploadId: aws.String(upload.UploadID),
		PartNumber: aws.Int32(int32(partNumber)), Body: part, ContentLength: aws.Int64(size),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(checksum),
	}
	start := now()
	output, callErr := client.UploadPart(ctx, input)
	b.metrics.observeOp(b.instance, "multipart_part", start, callErr)
	if callErr != nil {
		return storage.PartInfo{}, storage.WrapSafe("s3backend: upload multipart part failed", callErr)
	}
	returnedChecksum := strings.TrimSpace(aws.ToString(output.ChecksumSHA256))
	if returnedChecksum != "" && returnedChecksum != checksum {
		return storage.PartInfo{}, fmt.Errorf("s3backend: multipart part checksum mismatch")
	}
	etag := strings.TrimSpace(aws.ToString(output.ETag))
	if etag == "" {
		return storage.PartInfo{}, fmt.Errorf("s3backend: multipart part returned an empty etag")
	}
	return storage.PartInfo{PartNumber: partNumber, ETag: etag, Size: size, ChecksumSHA256: checksum}, nil
}

func spoolMultipartPart(reader io.Reader) (io.ReadSeeker, int64, string, func(), error) {
	file, err := os.CreateTemp("", "rho-kit-s3-part-*")
	if err != nil {
		return nil, 0, "", func() {}, storage.WrapSafe("s3backend: create multipart spool failed", err)
	}
	cleanup := func() {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, MaxMultipartPartBytes+1))
	if err != nil {
		cleanup()
		return nil, 0, "", func() {}, storage.WrapSafe("s3backend: spool multipart part failed", err)
	}
	if size > MaxMultipartPartBytes {
		cleanup()
		return nil, 0, "", func() {}, fmt.Errorf("%w: multipart part exceeds %d bytes", storage.ErrValidation, MaxMultipartPartBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, "", func() {}, storage.WrapSafe("s3backend: rewind multipart part failed", err)
	}
	return file, size, base64.StdEncoding.EncodeToString(hash.Sum(nil)), cleanup, nil
}

func (b *Backend) CompleteUpload(ctx context.Context, upload storage.MultipartUpload, parts []storage.PartInfo) error {
	if err := validateMultipartUpload(upload); err != nil {
		return err
	}
	if len(parts) == 0 || len(parts) > 10000 {
		return fmt.Errorf("%w: multipart completion requires between 1 and 10000 parts", storage.ErrValidation)
	}
	ordered := slices.Clone(parts)
	slices.SortFunc(ordered, func(left, right storage.PartInfo) int { return left.PartNumber - right.PartNumber })
	completed := make([]types.CompletedPart, len(ordered))
	for index, part := range ordered {
		if part.PartNumber != index+1 || part.ETag == "" || part.Size < 0 || part.ChecksumSHA256 == "" {
			return fmt.Errorf("%w: multipart completion parts must be contiguous and checksummed", storage.ErrValidation)
		}
		completed[index] = types.CompletedPart{
			PartNumber: aws.Int32(int32(part.PartNumber)), ETag: aws.String(part.ETag), ChecksumSHA256: aws.String(part.ChecksumSHA256),
		}
	}
	client, err := b.multipartClient()
	if err != nil {
		return err
	}
	start := now()
	_, callErr := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(b.bucket), Key: aws.String(upload.Key), UploadId: aws.String(upload.UploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	b.metrics.observeOp(b.instance, "multipart_complete", start, callErr)
	if callErr == nil {
		return nil
	}
	if isNoSuchUpload(callErr) {
		exists, existsErr := b.Exists(ctx, upload.Key)
		if existsErr == nil && exists {
			return nil
		}
	}
	return storage.WrapSafe("s3backend: complete multipart upload failed", callErr)
}

func (b *Backend) AbortUpload(ctx context.Context, upload storage.MultipartUpload) error {
	if err := validateMultipartUpload(upload); err != nil {
		return err
	}
	client, err := b.multipartClient()
	if err != nil {
		return err
	}
	start := now()
	_, callErr := client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(b.bucket), Key: aws.String(upload.Key), UploadId: aws.String(upload.UploadID),
	})
	b.metrics.observeOp(b.instance, "multipart_abort", start, callErr)
	if callErr == nil || isNoSuchUpload(callErr) {
		return nil
	}
	return storage.WrapSafe("s3backend: abort multipart upload failed", callErr)
}

func (b *Backend) ListMultipartUploads(ctx context.Context, prefix string, opts storage.MultipartUploadListOptions) (storage.MultipartUploadPage, error) {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return storage.MultipartUploadPage{}, err
	}
	if err := storage.ValidateMultipartUploadListOptions(opts); err != nil {
		return storage.MultipartUploadPage{}, err
	}
	client, err := b.multipartClient()
	if err != nil {
		return storage.MultipartUploadPage{}, err
	}
	input := &s3.ListMultipartUploadsInput{
		Bucket: aws.String(b.bucket), MaxUploads: aws.Int32(int32(opts.MaxUploads)),
	}
	// Native AWS honors Prefix and may enforce it in IAM policy. Some MinIO
	// path-style endpoints return an empty page when Prefix is present even
	// though the unfiltered request returns the active upload, so those
	// endpoints use a bounded global provider page followed by local filtering.
	if prefix != "" && (b.cfg.Endpoint == "" || !b.cfg.ForcePathStyle) {
		input.Prefix = aws.String(prefix)
	}
	if opts.KeyMarker != "" {
		input.KeyMarker = aws.String(opts.KeyMarker)
	}
	if opts.UploadIDMarker != "" {
		input.UploadIdMarker = aws.String(opts.UploadIDMarker)
	}
	start := now()
	output, callErr := client.ListMultipartUploads(ctx, input)
	b.metrics.observeOp(b.instance, "multipart_list", start, callErr)
	if callErr != nil {
		return storage.MultipartUploadPage{}, storage.WrapSafe("s3backend: list multipart uploads failed", callErr)
	}
	page := storage.MultipartUploadPage{
		Uploads:       make([]storage.MultipartUploadInfo, 0, len(output.Uploads)),
		NextKeyMarker: aws.ToString(output.NextKeyMarker), NextUploadIDMarker: aws.ToString(output.NextUploadIdMarker),
		Truncated: aws.ToBool(output.IsTruncated),
	}
	for _, value := range output.Uploads {
		initiated := aws.ToTime(value.Initiated)
		if !opts.InitiatedBefore.IsZero() && !initiated.Before(opts.InitiatedBefore) {
			continue
		}
		key, uploadID := aws.ToString(value.Key), aws.ToString(value.UploadId)
		if storage.ValidateKey(key) != nil || strings.TrimSpace(uploadID) == "" {
			return storage.MultipartUploadPage{}, fmt.Errorf("s3backend: list multipart uploads returned invalid state")
		}
		// Local filtering is also harmless for native provider-filtered pages.
		// Pagination markers remain the provider's markers, so custom endpoint
		// callers can scan bounded pages without returning another prefix's
		// entries.
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		page.Uploads = append(page.Uploads, storage.MultipartUploadInfo{
			Upload: storage.MultipartUpload{Key: key, UploadID: uploadID}, InitiatedAt: initiated.UTC(),
		})
	}
	return page, nil
}

func validateMultipartUpload(upload storage.MultipartUpload) error {
	if err := storage.ValidateKey(upload.Key); err != nil {
		return err
	}
	if upload.UploadID == "" || len(upload.UploadID) > storage.MaxKeyLen || strings.TrimSpace(upload.UploadID) != upload.UploadID {
		return fmt.Errorf("%w: multipart upload id is invalid", storage.ErrValidation)
	}
	return nil
}

func isNoSuchUpload(err error) bool {
	var typed *types.NoSuchUpload
	if errors.As(err, &typed) {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "NoSuchUpload"
}

func applyMultipartSSE(input *s3.CreateMultipartUploadInput, cfg Config) error {
	if err := validateSSEConfig(cfg); err != nil {
		return err
	}
	switch cfg.SSE {
	case "":
	case "AES256":
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	case "aws:kms":
		input.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(cfg.SSEKMSKeyID)
	}
	return nil
}
