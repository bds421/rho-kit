package s3backend

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// maxPresignTTL is the maximum allowed TTL for presigned URLs.
// AWS limits STS-based credentials to 12 hours and IAM to 7 days.
// We use 1 hour as a sane default to limit exposure of unauthenticated URLs.
const maxPresignTTL = 1 * time.Hour

// PresignGetURL generates a pre-signed GET URL valid for the given TTL.
func (b *Backend) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.PresignGet")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("s3backend: presign TTL must be positive")
	}
	if ttl > maxPresignTTL {
		return "", fmt.Errorf("s3backend: presign TTL exceeds maximum")
	}

	start := now()
	req, err := b.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	b.metrics.observeOp(b.instance, "presign_get", start, err)
	if err != nil {
		opErr := storage.WrapSafe("s3backend: presign get failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return "", opErr
	}
	return req.URL, nil
}

// PresignPutURL generates a pre-signed PUT URL valid for the given TTL.
// The caller is responsible for sending the correct Content-Type header.
//
// The configured server-side encryption policy (Config.SSE / SSEKMSKeyID)
// is signed into the presigned URL so the client must echo the matching
// x-amz-server-side-encryption (and x-amz-server-side-encryption-aws-kms-
// key-id where applicable) header on PUT — without that, S3 rejects the
// upload. This stops clients from bypassing the bucket's encryption
// policy via direct uploads.
//
// When meta.Size > 0 it is signed as Content-Length, so the URL holder
// cannot upload a body larger than the caller authorized. A meta.Size of 0
// means unknown and leaves the upload unbounded up to S3's single-PUT limit.
func (b *Backend) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.PresignPut")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("s3backend: presign TTL must be positive")
	}
	if ttl > maxPresignTTL {
		return "", fmt.Errorf("s3backend: presign TTL exceeds maximum")
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return "", err
	}

	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
		Metadata:    storage.CloneCustomMeta(meta.Custom),
	}
	// When the caller declares a Size, sign it as Content-Length so the URL
	// holder cannot upload a body larger than authorized (SigV4 signs the
	// Content-Length header once it is set on the input). This mirrors the
	// Put path. Size == 0 means unknown and leaves the upload unbounded up
	// to S3's single-PUT limit.
	if meta.Size > 0 {
		input.ContentLength = aws.Int64(meta.Size)
	}
	if err := applySSE(input, b.cfg); err != nil {
		return "", err
	}

	start := now()
	req, err := b.presigner.PresignPutObject(ctx, input, s3.WithPresignExpires(ttl))
	b.metrics.observeOp(b.instance, "presign_put", start, err)
	if err != nil {
		opErr := storage.WrapSafe("s3backend: presign put failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return "", opErr
	}
	return req.URL, nil
}
