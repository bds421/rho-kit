package s3backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

type mockMultipartClient struct {
	*mockS3Client
	createFn   func(context.Context, *s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error)
	uploadFn   func(context.Context, *s3.UploadPartInput) (*s3.UploadPartOutput, error)
	completeFn func(context.Context, *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error)
	abortFn    func(context.Context, *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error)
	listMPFn   func(context.Context, *s3.ListMultipartUploadsInput) (*s3.ListMultipartUploadsOutput, error)
}

func (client *mockMultipartClient) CreateMultipartUpload(ctx context.Context, input *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	if client.createFn != nil {
		return client.createFn(ctx, input)
	}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-a")}, nil
}

func (client *mockMultipartClient) UploadPart(ctx context.Context, input *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if client.uploadFn != nil {
		return client.uploadFn(ctx, input)
	}
	return &s3.UploadPartOutput{ETag: aws.String("etag-a"), ChecksumSHA256: input.ChecksumSHA256}, nil
}

func (client *mockMultipartClient) CompleteMultipartUpload(ctx context.Context, input *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	if client.completeFn != nil {
		return client.completeFn(ctx, input)
	}
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (client *mockMultipartClient) AbortMultipartUpload(ctx context.Context, input *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	if client.abortFn != nil {
		return client.abortFn(ctx, input)
	}
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (client *mockMultipartClient) ListMultipartUploads(ctx context.Context, input *s3.ListMultipartUploadsInput, _ ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	if client.listMPFn != nil {
		return client.listMPFn(ctx, input)
	}
	return &s3.ListMultipartUploadsOutput{}, nil
}

func newMultipartTestBackend(client *mockMultipartClient, opts ...Option) *Backend {
	if client.mockS3Client == nil {
		client.mockS3Client = &mockS3Client{}
	}
	return NewWithClient(client, &mockPresigner{getURL: "https://presigned-get", putURL: "https://presigned-put"}, "test-bucket", opts...)
}

func TestMultipartInitBindsChecksumMetadataAndEncryption(t *testing.T) {
	var captured *s3.CreateMultipartUploadInput
	client := &mockMultipartClient{createFn: func(_ context.Context, input *s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
		captured = input
		return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-a")}, nil
	}}
	backend := newMultipartTestBackend(client, WithConfig(Config{SSE: "aws:kms", SSEKMSKeyID: "key-a"}))
	upload, err := backend.InitUpload(context.Background(), "tenant/staging/item", storage.ObjectMeta{
		ContentType: "text/plain", Custom: map[string]string{"operation": "abc"},
	})
	require.NoError(t, err)
	assert.Equal(t, storage.MultipartUpload{Key: "tenant/staging/item", UploadID: "upload-a"}, upload)
	assert.Equal(t, types.ChecksumAlgorithmSha256, captured.ChecksumAlgorithm)
	assert.Equal(t, types.ServerSideEncryptionAwsKms, captured.ServerSideEncryption)
	assert.Equal(t, "key-a", aws.ToString(captured.SSEKMSKeyId))
	assert.Equal(t, "abc", captured.Metadata["operation"])

	validated := newMultipartTestBackend(&mockMultipartClient{}, WithValidators(storage.MaxFileSize(1024)))
	_, err = validated.InitUpload(context.Background(), "tenant/staging/item", storage.ObjectMeta{})
	require.ErrorIs(t, err, storage.ErrValidation)
}

func TestMultipartPartSpoolsBoundsAndChecksums(t *testing.T) {
	payload := []byte("exact non-seekable source bytes")
	wantHash := sha256.Sum256(payload)
	wantChecksum := base64.StdEncoding.EncodeToString(wantHash[:])
	var captured []byte
	client := &mockMultipartClient{uploadFn: func(_ context.Context, input *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
		var err error
		captured, err = io.ReadAll(input.Body)
		require.NoError(t, err)
		assert.Equal(t, int64(len(payload)), aws.ToInt64(input.ContentLength))
		assert.Equal(t, wantChecksum, aws.ToString(input.ChecksumSHA256))
		return &s3.UploadPartOutput{ETag: aws.String("etag-a"), ChecksumSHA256: aws.String(wantChecksum)}, nil
	}}
	backend := newMultipartTestBackend(client)
	part, err := backend.UploadPart(context.Background(), storage.MultipartUpload{Key: "tenant/staging/item", UploadID: "upload-a"}, 1, io.NopCloser(bytes.NewBuffer(payload)))
	require.NoError(t, err)
	assert.Equal(t, payload, captured)
	assert.Equal(t, storage.PartInfo{PartNumber: 1, ETag: "etag-a", Size: int64(len(payload)), ChecksumSHA256: wantChecksum}, part)

	client.uploadFn = func(_ context.Context, input *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
		return &s3.UploadPartOutput{ETag: aws.String("etag"), ChecksumSHA256: aws.String("different")}, nil
	}
	_, err = backend.UploadPart(context.Background(), storage.MultipartUpload{Key: "tenant/staging/item", UploadID: "upload-a"}, 1, strings.NewReader("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestMultipartCompleteIsCanonicalAndRetrySafe(t *testing.T) {
	var captured *s3.CompleteMultipartUploadInput
	client := &mockMultipartClient{completeFn: func(_ context.Context, input *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
		captured = input
		return &s3.CompleteMultipartUploadOutput{}, nil
	}}
	backend := newMultipartTestBackend(client)
	upload := storage.MultipartUpload{Key: "tenant/staging/item", UploadID: "upload-a"}
	parts := []storage.PartInfo{
		{PartNumber: 2, ETag: "etag-b", Size: 1, ChecksumSHA256: "checksum-b"},
		{PartNumber: 1, ETag: "etag-a", Size: 1, ChecksumSHA256: "checksum-a"},
	}
	require.NoError(t, backend.CompleteUpload(context.Background(), upload, parts))
	assert.Equal(t, int32(1), aws.ToInt32(captured.MultipartUpload.Parts[0].PartNumber))
	assert.Equal(t, "checksum-a", aws.ToString(captured.MultipartUpload.Parts[0].ChecksumSHA256))

	client.completeFn = func(context.Context, *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
		return nil, &types.NoSuchUpload{}
	}
	client.headFn = func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
		return &s3.HeadObjectOutput{}, nil
	}
	require.NoError(t, backend.CompleteUpload(context.Background(), upload, parts))
}

func TestMultipartAbortIsIdempotentAndErrorsAreSafe(t *testing.T) {
	client := &mockMultipartClient{abortFn: func(context.Context, *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) {
		return nil, &types.NoSuchUpload{}
	}}
	backend := newMultipartTestBackend(client)
	upload := storage.MultipartUpload{Key: "tenant/staging/secret-token", UploadID: "upload-secret-token"}
	require.NoError(t, backend.AbortUpload(context.Background(), upload))

	client.abortFn = func(context.Context, *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) {
		return nil, errors.New("provider leaked secret-token")
	}
	err := backend.AbortUpload(context.Background(), upload)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestMultipartListIsBoundedPagedAndFiltersStaleUploads(t *testing.T) {
	before := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	client := &mockMultipartClient{listMPFn: func(_ context.Context, input *s3.ListMultipartUploadsInput) (*s3.ListMultipartUploadsOutput, error) {
		assert.Equal(t, int32(10), aws.ToInt32(input.MaxUploads))
		assert.Nil(t, input.Prefix, "custom path-style endpoints require the bounded MinIO fallback")
		return &s3.ListMultipartUploadsOutput{
			Uploads: []types.MultipartUpload{
				{Key: aws.String("another/staging/old"), UploadId: aws.String("foreign"), Initiated: aws.Time(before.Add(-time.Hour))},
				{Key: aws.String("tenant/staging/old"), UploadId: aws.String("old"), Initiated: aws.Time(before.Add(-time.Hour))},
				{Key: aws.String("tenant/staging/new"), UploadId: aws.String("new"), Initiated: aws.Time(before.Add(time.Hour))},
			},
			IsTruncated: aws.Bool(true), NextKeyMarker: aws.String("next"), NextUploadIdMarker: aws.String("next-id"),
		}, nil
	}}
	backend := newMultipartTestBackend(client, WithConfig(Config{Endpoint: "http://minio:9000", ForcePathStyle: true}))
	page, err := backend.ListMultipartUploads(context.Background(), "tenant/staging/", storage.MultipartUploadListOptions{
		MaxUploads: 10, InitiatedBefore: before,
	})
	require.NoError(t, err)
	require.Len(t, page.Uploads, 1)
	assert.Equal(t, "old", page.Uploads[0].Upload.UploadID)
	assert.True(t, page.Truncated)
	assert.Equal(t, "next", page.NextKeyMarker)
}

func TestMultipartListPreservesNativeAWSPrefix(t *testing.T) {
	client := &mockMultipartClient{listMPFn: func(_ context.Context, input *s3.ListMultipartUploadsInput) (*s3.ListMultipartUploadsOutput, error) {
		assert.Equal(t, "tenant/staging/", aws.ToString(input.Prefix))
		return &s3.ListMultipartUploadsOutput{}, nil
	}}
	backend := newMultipartTestBackend(client)
	_, err := backend.ListMultipartUploads(context.Background(), "tenant/staging/", storage.MultipartUploadListOptions{MaxUploads: 10})
	require.NoError(t, err)
}
