package s3backend

import (
	"context"
	"fmt"
	"iter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/infra/storage"
)

// Compile-time interface compliance check.
var _ storage.Lister = (*S3Backend)(nil)

// List returns an iterator over objects in the bucket whose keys start with prefix.
// Pagination is handled internally — the iterator fetches pages lazily as the
// caller consumes results.
func (b *S3Backend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	if prefix != "" {
		if err := storage.ValidatePrefix(prefix); err != nil {
			return func(yield func(storage.ObjectInfo, error) bool) {
				yield(storage.ObjectInfo{}, fmt.Errorf("s3backend: %w", err))
			}
		}
	}

	return func(yield func(storage.ObjectInfo, error) bool) {
		ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.List")
		defer span.End()
		span.SetAttributes(
			attribute.String("storage.bucket", b.bucket),
			attribute.String("storage.prefix", prefix),
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
				span.SetStatus(codes.Error, err.Error())
				yield(storage.ObjectInfo{}, fmt.Errorf("s3backend: list prefix %q: %w", prefix, err))
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
