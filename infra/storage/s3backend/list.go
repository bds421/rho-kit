package s3backend

import (
	"context"
	"iter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Lister = (*Backend)(nil)

// List returns an iterator over objects in the bucket whose keys start with prefix.
// Pagination is handled internally — the iterator fetches pages lazily as the
// caller consumes results.
func (b *Backend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, redact.WrapError("s3backend", err))
		}
	}
	if err := storage.ValidateListOptions(opts); err != nil {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, redact.WrapError("s3backend", err))
		}
	}

	return func(yield func(storage.ObjectInfo, error) bool) {
		ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.List")
		defer span.End()
		span.SetAttributes(
			attribute.String("storage.bucket", b.bucket),
			attribute.Int("storage.prefix_len", len(prefix)),
		)

		input := &s3.ListObjectsV2Input{
			Bucket: aws.String(b.bucket),
		}
		if prefix != "" {
			input.Prefix = aws.String(prefix)
		}
		if opts.StartAfter != "" {
			input.StartAfter = aws.String(opts.StartAfter)
		}
		if opts.MaxKeys > 0 {
			input.MaxKeys = aws.Int32(int32(min(opts.MaxKeys, 1000)))
		}

		count := 0

		for {
			start := now()
			out, err := b.client.ListObjectsV2(ctx, input)
			b.metrics.observeOp(b.instance, "list", start, err)

			if err != nil {
				opErr := storage.WrapSafe("s3backend: list failed", err)
				span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
				yield(storage.ObjectInfo{}, opErr)
				return
			}

			for _, obj := range out.Contents {
				info := storage.ObjectInfo{
					Key:  aws.ToString(obj.Key),
					Size: aws.ToInt64(obj.Size),
				}
				if obj.LastModified != nil {
					info.ModTime = *obj.LastModified
				}

				count++
				if !yield(info, nil) {
					return
				}

				if opts.MaxKeys > 0 && count >= opts.MaxKeys {
					return
				}
			}

			if !aws.ToBool(out.IsTruncated) {
				return
			}

			input.ContinuationToken = out.NextContinuationToken
		}
	}
}
