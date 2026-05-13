package s3backend

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Copier = (*Backend)(nil)

// Copy performs a server-side copy within the same bucket using S3 CopyObject.
func (b *Backend) Copy(ctx context.Context, srcKey, dstKey string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Copy")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.Int("storage.src_key_len", len(srcKey)),
		attribute.Int("storage.dst_key_len", len(dstKey)),
	)

	if err := storage.ValidateKey(srcKey); err != nil {
		return err
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return err
	}

	// CopySource format: "bucket/key" (URL-encoded).
	copySource := b.bucket + "/" + url.PathEscape(srcKey)

	input := &s3.CopyObjectInput{
		Bucket:     aws.String(b.bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(dstKey),
	}
	if err := applyCopySSE(input, b.cfg); err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}

	start := now()
	_, err := b.client.CopyObject(ctx, input)
	b.metrics.observeOp(b.instance, "copy", start, err)

	if err != nil {
		opErr := storage.WrapSafe("s3backend: copy failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}
