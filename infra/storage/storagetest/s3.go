//go:build integration

package storagetest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bds421/rho-kit/infra/storage/s3backend"
)

var (
	s3Once    sync.Once
	s3Config  s3backend.S3Config
	s3InitErr error
)

// StartS3 returns an S3Config pointing at a shared LocalStack container.
// The container is started on the first call and reused within the same
// test process (Ryuk terminates it when the process exits).
//
// Each test should use a unique bucket name to ensure isolation.
func StartS3(t *testing.T, bucket string) s3backend.S3Config {
	t.Helper()

	s3Once.Do(func() {
		ctx := context.Background()

		req := testcontainers.ContainerRequest{
			Image:        "localstack/localstack:3",
			ExposedPorts: []string{"4566/tcp"},
			Env:          map[string]string{"SERVICES": "s3"},
			WaitingFor:   wait.ForHTTP("/_localstack/health").WithPort("4566/tcp"),
		}
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			s3InitErr = fmt.Errorf("start localstack container: %w", err)
			return
		}

		host, err := container.Host(ctx)
		if err != nil {
			s3InitErr = fmt.Errorf("get localstack host: %w", err)
			return
		}
		port, err := container.MappedPort(ctx, "4566/tcp")
		if err != nil {
			s3InitErr = fmt.Errorf("get localstack port: %w", err)
			return
		}

		s3Config = s3backend.S3Config{
			Region:          "us-east-1",
			Endpoint:        fmt.Sprintf("http://%s:%s", host, port.Port()),
			ForcePathStyle:  true,
			AccessKeyID:     "test",
			SecretAccessKey: "test",
		}
	})

	if s3InitErr != nil {
		t.Fatalf("storagetest: localstack setup: %v", s3InitErr)
	}

	cfg := s3Config
	cfg.Bucket = bucket
	return cfg
}
