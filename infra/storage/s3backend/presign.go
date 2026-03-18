package s3backend

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bds421/rho-kit/infra/storage"
)

// maxPresignTTL is the maximum allowed TTL for presigned URLs.
// AWS limits STS-based credentials to 12 hours and IAM to 7 days.
// We use 1 hour as a sane default to limit exposure of unauthenticated URLs.
const maxPresignTTL = 1 * time.Hour

// PresignGetURL generates a pre-signed GET URL valid for the given TTL.
func (b *S3Backend) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("s3backend: presign TTL must be positive")
	}
	if ttl > maxPresignTTL {
		return "", fmt.Errorf("s3backend: presign TTL %v exceeds maximum %v", ttl, maxPresignTTL)
	}

	req, err := b.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3backend: presign get %q: %w", key, err)
	}
	return req.URL, nil
}

// PresignPutURL generates a pre-signed PUT URL valid for the given TTL.
// The caller is responsible for sending the correct Content-Type header.
func (b *S3Backend) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("s3backend: presign TTL must be positive")
	}
	if ttl > maxPresignTTL {
		return "", fmt.Errorf("s3backend: presign TTL %v exceeds maximum %v", ttl, maxPresignTTL)
	}

	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	req, err := b.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3backend: presign put %q: %w", key, err)
	}
	return req.URL, nil
}
