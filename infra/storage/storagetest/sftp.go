//go:build integration

package storagetest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bds421/rho-kit/infra/storage/sftpbackend"
)

var (
	sftpOnce    sync.Once
	sftpConfig  sftpbackend.SFTPConfig
	sftpInitErr error
)

// StartSFTP returns an SFTPConfig pointing at a shared atmoz/sftp container.
// The container is started on the first call and reused within the same
// test process.
//
// The container creates a user "testuser" with password "testpass" and
// home directory "/home/testuser/upload".
func StartSFTP(t *testing.T) sftpbackend.SFTPConfig {
	t.Helper()

	sftpOnce.Do(func() {
		ctx := context.Background()

		req := testcontainers.ContainerRequest{
			Image:        "atmoz/sftp:latest",
			ExposedPorts: []string{"22/tcp"},
			Cmd:          []string{"testuser:testpass:::upload"},
			WaitingFor:   wait.ForListeningPort("22/tcp"),
		}
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			sftpInitErr = fmt.Errorf("start sftp container: %w", err)
			return
		}

		host, err := container.Host(ctx)
		if err != nil {
			sftpInitErr = fmt.Errorf("get sftp host: %w", err)
			return
		}
		port, err := container.MappedPort(ctx, "22/tcp")
		if err != nil {
			sftpInitErr = fmt.Errorf("get sftp port: %w", err)
			return
		}

		sftpConfig = sftpbackend.SFTPConfig{
			Host:                      host,
			Port:                      port.Int(),
			User:                      "testuser",
			Password:                  "testpass",
			RootPath:                  "/home/testuser/upload",
			InsecureSkipHostKeyVerify: true,
		}
	})

	if sftpInitErr != nil {
		t.Fatalf("storagetest: sftp setup: %v", sftpInitErr)
	}

	return sftpConfig
}
