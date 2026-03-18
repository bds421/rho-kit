// Package s3backend provides an AWS S3 implementation of [storage.Storage].
//
// It uses the AWS SDK v2 and supports any S3-compatible endpoint (including
// MinIO and LocalStack for development). Pre-signed URL generation is available
// via the [storage.PresignedStore] interface.
//
// All operations are instrumented with Prometheus metrics and OpenTelemetry traces.
package s3backend
