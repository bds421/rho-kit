package s3backend

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bds421/rho-kit/observability/health"
)

// HealthCheck returns a non-critical DependencyCheck for S3.
// It performs a HeadBucket probe with a 5-second timeout.
func HealthCheck(b *S3Backend) health.DependencyCheck {
	return healthCheck(b, false)
}

// CriticalHealthCheck returns a critical DependencyCheck for S3.
// An unhealthy S3 triggers HTTP 503 on the readiness endpoint.
func CriticalHealthCheck(b *S3Backend) health.DependencyCheck {
	return healthCheck(b, true)
}

func healthCheck(b *S3Backend, critical bool) health.DependencyCheck {
	return health.DependencyCheck{
		Name: fmt.Sprintf("s3:%s", b.bucket),
		Check: func(ctx context.Context) string {
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			_, err := b.client.HeadBucket(checkCtx, &s3.HeadBucketInput{
				Bucket: aws.String(b.bucket),
			})
			if err != nil {
				slog.Warn("s3 health check failed", "bucket", b.bucket, "error", err)
				return "unhealthy"
			}
			return "healthy"
		},
		Critical: critical,
	}
}
