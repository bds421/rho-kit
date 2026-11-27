package storagetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bds421/rho-kit/infra/storage/s3backend/v2"
)

// NewS3Bucket creates a bucket of its own for the test on the endpoint cfg
// names and returns cfg with Bucket set to it. The name is cfg.Bucket (or
// "storagetest") plus a random suffix. When the test ends the bucket's
// objects are deleted, then the bucket, so a shared, long-lived S3 keeps
// no bucket per run. cfg's static keys are used when set, otherwise the
// default credential chain.
func NewS3Bucket(t *testing.T, cfg s3backend.Config) s3backend.Config {
	t.Helper()
	prefix := cfg.Bucket
	if prefix == "" {
		prefix = "storagetest"
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:]) // crypto/rand.Read never fails
	cfg.Bucket = prefix + "-" + hex.EncodeToString(suffix[:])

	client := s3Client(t, cfg)
	if _, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: &cfg.Bucket}); err != nil {
		t.Fatalf("storagetest: create bucket %s: %v", cfg.Bucket, err)
	}
	t.Cleanup(func() { dropS3Bucket(t, client, cfg.Bucket) })
	return cfg
}

func s3Client(t *testing.T, cfg s3backend.Config) *s3.Client {
	t.Helper()
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.Endpoint != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(cfg.Endpoint))
	}
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(t.Context(), opts...)
	if err != nil {
		t.Fatalf("storagetest: S3 client config: %v", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = cfg.ForcePathStyle })
}

func dropS3Bucket(t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	// The test's context has ended by now; the cleanup still needs one.
	ctx := context.WithoutCancel(t.Context())
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: &bucket})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Errorf("storagetest: list bucket %s: %v", bucket, err)
			return
		}
		for _, o := range page.Contents {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: o.Key}); err != nil {
				t.Errorf("storagetest: delete %s/%s: %v", bucket, *o.Key, err)
			}
		}
	}
	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &bucket}); err != nil {
		t.Errorf("storagetest: delete bucket %s: %v", bucket, err)
	}
}
