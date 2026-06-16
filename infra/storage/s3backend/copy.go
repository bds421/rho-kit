package s3backend

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
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

	// CopySource format: "bucket/key" (URL-encoded). url.PathEscape leaves
	// "+" literal (it is allowed in path segments), but S3 URL-decodes the
	// x-amz-copy-source header and treats "+" as a space — so a key with a
	// "+" would copy the wrong object (or fail with NoSuchKey). Encode "+"
	// as %2B explicitly, matching AWS CopyObject URL-encoding guidance.
	copySource := b.bucket + "/" + strings.ReplaceAll(url.PathEscape(srcKey), "+", "%2B")

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
		// A missing source must surface as storage.ErrObjectNotFound so
		// portable callers (e.g. errors.Is around storage.Move) behave the
		// same as membackend / localbackend. S3's CopyObject deserializer
		// models only ObjectNotInActiveTierError, so NoSuchKey / NotFound
		// arrive as a generic smithy.APIError — match the code as well as
		// the typed shape. NotFound carries no key/secret, so it is safe to
		// keep control flow out of the trace, like Get.
		if isCopySourceNotFound(err) {
			return fmt.Errorf("s3backend: copy: %w", storage.ErrObjectNotFound)
		}
		opErr := storage.WrapSafe("s3backend: copy failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// isCopySourceNotFound reports whether a CopyObject error indicates the
// source object does not exist. It matches both the typed not-found shapes
// (handled by isS3NotFound) and the generic smithy.APIError codes that S3
// returns for CopyObject, where NoSuchKey is not modeled as a typed error.
func isCopySourceNotFound(err error) bool {
	if isS3NotFound(err) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}
