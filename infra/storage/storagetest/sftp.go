//go:build integration

package storagetest

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bds421/rho-kit/infra/storage/sftpbackend/v2"
)

const (
	sftpTestUser     = "testuser"
	sftpTestPassword = "strong-test-password-123"
)

var (
	sftpOnce    sync.Once
	sftpConfig  sftpbackend.Config
	sftpInitErr error
)

// StartSFTP returns an SFTPConfig pointing at a shared atmoz/sftp container.
// The container is started on the first call and reused within the same
// test process.
//
// The container creates a test user with a strong password and home directory
// "/home/testuser/upload".
func StartSFTP(t *testing.T) sftpbackend.Config {
	t.Helper()

	sftpOnce.Do(func() {
		ctx := context.Background()

		req := testcontainers.ContainerRequest{
			Image:        "atmoz/sftp:latest",
			ExposedPorts: []string{"22/tcp"},
			Cmd:          []string{sftpTestUser + ":" + sftpTestPassword + ":::upload"},
			WaitingFor:   wait.ForListeningPort("22/tcp"),
		}
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			sftpInitErr = fmt.Errorf("start sftp container: %w", err) // kit:ok-fmt-errorf-wrap
			return
		}

		host, err := container.Host(ctx)
		if err != nil {
			sftpInitErr = fmt.Errorf("get sftp host: %w", err) // kit:ok-fmt-errorf-wrap
			return
		}
		port, err := container.MappedPort(ctx, "22/tcp")
		if err != nil {
			sftpInitErr = fmt.Errorf("get sftp port: %w", err) // kit:ok-fmt-errorf-wrap
			return
		}
		portNum, err := strconv.Atoi(port.Port())
		if err != nil {
			sftpInitErr = fmt.Errorf("parse sftp port %q: %w", port.Port(), err) // kit:ok-fmt-errorf-wrap
			return
		}

		knownHostsFile, err := writeKnownHostsFile(host, port.Port())
		if err != nil {
			sftpInitErr = err
			return
		}

		sftpConfig = sftpbackend.Config{
			Host:           host,
			Port:           portNum,
			User:           sftpTestUser,
			Password:       sftpTestPassword,
			RootPath:       "/home/testuser/upload",
			KnownHostsFile: knownHostsFile,
		}
	})

	if sftpInitErr != nil {
		t.Fatalf("storagetest: sftp setup: %v", sftpInitErr)
	}

	return sftpConfig
}

func writeKnownHostsFile(host, port string) (string, error) {
	addr := net.JoinHostPort(host, port)
	var hostKey ssh.PublicKey
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: sftpTestUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(sftpTestPassword),
		},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = key
			return nil
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("capture sftp host key: %w", err) // kit:ok-fmt-errorf-wrap
	}
	_ = client.Close()
	if hostKey == nil {
		return "", fmt.Errorf("capture sftp host key: server did not present a key")
	}

	// O_EXCL + random name under os.TempDir so a co-tenant cannot
	// pre-create a predictable path (symlink clobber) (review-18).
	f, err := os.CreateTemp("", "rho-kit-sftp-known-hosts-*")
	if err != nil {
		return "", fmt.Errorf("create sftp known_hosts file: %w", err) // kit:ok-fmt-errorf-wrap
	}
	path := f.Name()
	line := knownhosts.Line([]string{addr}, hostKey) + "\n"
	if _, err := f.Write([]byte(line)); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write sftp known_hosts file: %w", err) // kit:ok-fmt-errorf-wrap
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("chmod sftp known_hosts file: %w", err) // kit:ok-fmt-errorf-wrap
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close sftp known_hosts file: %w", err) // kit:ok-fmt-errorf-wrap
	}
	return path, nil
}
