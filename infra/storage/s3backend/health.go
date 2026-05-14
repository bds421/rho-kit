package s3backend

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// HealthCheck returns a non-critical DependencyCheck for S3.
// It performs a HeadBucket probe with a 5-second timeout.
func HealthCheck(b *Backend) health.DependencyCheck {
	return healthCheck(b, false)
}

// CriticalHealthCheck returns a critical DependencyCheck for S3.
// An unhealthy S3 triggers HTTP 503 on the readiness endpoint.
func CriticalHealthCheck(b *Backend) health.DependencyCheck {
	return healthCheck(b, true)
}

func healthCheck(b *Backend, critical bool) health.DependencyCheck {
	if b == nil || b.client == nil {
		// Panicking at construction surfaces the wiring bug
		// immediately rather than leaking it as a nil-deref on the
		// first scrape.
		panic("s3backend: HealthCheck requires a fully-constructed Backend")
	}
	if b.bucket == "" {
		// An empty bucket would let HeadBucket reach the AWS API with
		// an empty Bucket value and return an unhelpful "bucket name
		// cannot be empty" error every scrape. Treat as a wiring bug
		// (L117).
		panic("s3backend: HealthCheck requires a Backend with a non-empty bucket")
	}
	return health.DependencyCheck{
		Name: health.OpaqueCheckName("s3", b.bucket),
		Check: func(ctx context.Context) string {
			if ctx == nil {
				ctx = context.Background()
			}
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			_, err := b.client.HeadBucket(checkCtx, &s3.HeadBucketInput{
				Bucket: aws.String(b.bucket),
			})
			if err != nil {
				slog.Warn("s3 health check failed", redact.String("bucket", b.bucket), redact.Error(err))
				return "unhealthy"
			}
			return "healthy"
		},
		Critical: critical,
	}
}
