//go:build integration

package storagetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage/s3backend/v2"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestS3MultipartConformanceAgainstMinIO(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := StartMinIO(t, "rho-kit-multipart")
	createS3Bucket(t, ctx, cfg)
	backend, err := s3backend.NewContext(ctx, cfg)
	require.NoError(t, err)
	runS3MultipartConformance(t, ctx, backend, "tenant/staging/item")
}

// TestS3MultipartConformanceAgainstAWS runs the same contract against an
// existing AWS bucket when explicitly configured. It deliberately uses the
// default credential chain so CI can supply workload identity without static
// credentials. Required variables are RHO_KIT_TEST_AWS_S3_REGION and
// RHO_KIT_TEST_AWS_S3_BUCKET; the optional prefix defaults to rho-kit-tests.
func TestS3MultipartConformanceAgainstAWS(t *testing.T) {
	t.Parallel()
	region, bucket := os.Getenv("RHO_KIT_TEST_AWS_S3_REGION"), os.Getenv("RHO_KIT_TEST_AWS_S3_BUCKET")
	if region == "" || bucket == "" {
		t.Skip("AWS S3 multipart conformance is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	backend, err := s3backend.NewContext(ctx, s3backend.Config{
		Region: region, Bucket: bucket, UseDefaultCredentials: true, SSE: "AES256",
	})
	require.NoError(t, err)
	prefix := strings.Trim(os.Getenv("RHO_KIT_TEST_AWS_S3_PREFIX"), "/")
	if prefix == "" {
		prefix = "rho-kit-tests"
	}
	key := fmt.Sprintf("%s/multipart/%d/item", prefix, time.Now().UnixNano())
	runS3MultipartConformance(t, ctx, backend, key)
}

func runS3MultipartConformance(t *testing.T, ctx context.Context, backend *s3backend.Backend, key string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := backend.Delete(cleanupCtx, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
			t.Errorf("delete conformance object: %v", err)
		}
	})

	uploader, ok := storage.AsMultipartUploader(backend)
	require.True(t, ok)
	lister, ok := storage.AsMultipartUploadLister(backend)
	require.True(t, ok)
	upload, err := uploader.InitUpload(ctx, key, storage.ObjectMeta{ContentType: "application/octet-stream"})
	require.NoError(t, err)

	first := bytes.Repeat([]byte("a"), 5<<20)
	second := []byte("bounded-final-part")
	part1, err := uploader.UploadPart(ctx, upload, 1, io.LimitReader(bytes.NewReader(first), int64(len(first))))
	require.NoError(t, err)
	prefix := key[:strings.LastIndex(key, "/")+1]
	page, err := lister.ListMultipartUploads(ctx, prefix, storage.MultipartUploadListOptions{MaxUploads: storage.MaxMultipartUploadPageSize})
	require.NoError(t, err)
	require.Len(t, page.Uploads, 1)
	require.Equal(t, upload, page.Uploads[0].Upload)
	part2, err := uploader.UploadPart(ctx, upload, 2, io.LimitReader(bytes.NewReader(second), int64(len(second))))
	require.NoError(t, err)
	require.NotEmpty(t, part1.ChecksumSHA256)
	require.NotEmpty(t, part2.ChecksumSHA256)
	require.NoError(t, uploader.CompleteUpload(ctx, upload, []storage.PartInfo{part2, part1}))
	require.NoError(t, uploader.CompleteUpload(ctx, upload, []storage.PartInfo{part1, part2}), "completion retry must be idempotent")

	reader, _, err := backend.Get(ctx, upload.Key)
	require.NoError(t, err)
	retained, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, append(first, second...), retained)

	orphan, err := uploader.InitUpload(ctx, prefix+"orphan", storage.ObjectMeta{})
	require.NoError(t, err)
	require.NoError(t, uploader.AbortUpload(ctx, orphan))
	require.NoError(t, uploader.AbortUpload(ctx, orphan), "abort retry must be idempotent")
}

func createS3Bucket(t *testing.T, ctx context.Context, cfg s3backend.Config) {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region), awsconfig.WithBaseEndpoint(cfg.Endpoint),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	require.NoError(t, err)
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) { options.UsePathStyle = true })
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &cfg.Bucket})
	require.NoError(t, err)
}
