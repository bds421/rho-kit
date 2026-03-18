package s3backend

import (
	"context"
	"fmt"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/infra/storage"
)

// Compile-time interface compliance check.
var _ storage.Copier = (*S3Backend)(nil)

// Copy performs a server-side copy within the same bucket using S3 CopyObject.
func (b *S3Backend) Copy(ctx context.Context, srcKey, dstKey string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Copy")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.String("storage.src_key", srcKey),
		attribute.String("storage.dst_key", dstKey),
	)

	if err := storage.ValidateKey(srcKey); err != nil {
		return err
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return err
	}

	// CopySource format: "bucket/key" (URL-encoded).
	copySource := b.bucket + "/" + url.PathEscape(srcKey)

	start := now()
	_, err := b.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(b.bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(dstKey),
	})
	b.metrics.observeOp(b.instance, "copy", start, err)

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("s3backend: copy %q → %q: %w", srcKey, dstKey, err)
	}
	return nil
}
