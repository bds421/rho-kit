//go:build integration

package storagetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bds421/rho-kit/infra/storage/s3backend/v2"
)

// StartMinIO starts a real S3-compatible endpoint for multipart conformance.
// The container is scoped to the calling test and terminated automatically.
func StartMinIO(t *testing.T, bucket string) s3backend.Config {
	return startMinIO(t, bucket, "minio/minio:RELEASE.2025-09-07T16-13-09Z")
}

func startMinIO(t *testing.T, bucket, image string) s3backend.Config {
	t.Helper()
	ctx := context.Background()
	const (
		accessKey = "rho-kit-minio-access"
		secretKey = "rho-kit-minio-secret-key-123456789"
	)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"MINIO_ROOT_USER": accessKey, "MINIO_ROOT_PASSWORD": secretKey},
			Cmd:        []string{"server", "/data", "--console-address", ":9001"},
			WaitingFor: wait.ForHTTP("/minio/health/live").WithPort("9000/tcp"),
		},
	})
	if err != nil {
		t.Fatalf("storagetest: start MinIO: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("storagetest: terminate MinIO: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("storagetest: get MinIO host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("storagetest: get MinIO port: %v", err)
	}
	return s3backend.Config{
		Region: "us-east-1", Bucket: bucket, Endpoint: fmt.Sprintf("http://%s:%s", host, port.Port()),
		ForcePathStyle: true, AllowInsecureEndpoint: true, AccessKeyID: accessKey, SecretAccessKey: secretKey,
	}
}
