package sftpbackend

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSFTPConfigReadsKnownHostsFile(t *testing.T) {
	t.Setenv("STORAGE_SFTP_HOST", "sftp.example.com")
	t.Setenv("STORAGE_SFTP_PORT", "22")
	t.Setenv("STORAGE_SFTP_USER", "svc")
	t.Setenv("APP_SFTP_PASSWORD", "strong-password-123")
	t.Setenv("STORAGE_SFTP_ROOT_PATH", "/uploads")
	t.Setenv("STORAGE_SFTP_KNOWN_HOSTS_FILE", "/etc/ssh/ssh_known_hosts")

	cfg, err := LoadSFTPConfig("APP", "production")
	require.NoError(t, err)
	assert.Equal(t, "/etc/ssh/ssh_known_hosts", cfg.KnownHostsFile)
}

func TestSFTPConfigLogValueDoesNotExposeCredentialFilePaths(t *testing.T) {
	t.Parallel()

	cfg := SFTPConfig{
		Host:           "tenant-sftp.example.com",
		Port:           22,
		User:           "tenant-upload-user",
		Password:       "strong-password-123",
		KeyFile:        "/var/run/secrets/sftp/id_ed25519",
		RootPath:       "/customers/tenant-prod/uploads",
		KnownHostsFile: "/var/run/secrets/sftp/known_hosts",
	}

	rendered := cfg.LogValue().String()
	for _, secret := range []string{
		cfg.Host,
		cfg.User,
		cfg.Password,
		cfg.KeyFile,
		cfg.RootPath,
		cfg.KnownHostsFile,
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue exposed resource detail %q: %s", secret, rendered)
		}
	}
	assert.Contains(t, rendered, "host_configured=true")
	assert.Contains(t, rendered, "user_configured=true")
	assert.Contains(t, rendered, "password_configured=true")
	assert.Contains(t, rendered, "key_file_configured=true")
	assert.Contains(t, rendered, "root_path_configured=true")
	assert.Contains(t, rendered, "known_hosts_file_configured=true")
}

func TestSFTPConfigValidateRequiresKnownHostsFile(t *testing.T) {
	t.Parallel()

	cfg := SFTPConfig{
		Host:     "sftp.example.com",
		Port:     22,
		User:     "svc",
		Password: "strong-password-123",
		RootPath: "/uploads",
	}

	err := cfg.Validate("production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KNOWN_HOSTS")
}

func TestSFTPConfigValidateAllowsPasswordProvider(t *testing.T) {
	t.Parallel()

	cfg := SFTPConfig{
		Host: "sftp.example.com",
		Port: 22,
		User: "svc",
		PasswordProvider: func(context.Context) (string, error) {
			return "strong-password-123", nil
		},
		RootPath:       "/uploads",
		KnownHostsFile: "/etc/ssh/ssh_known_hosts",
	}

	require.NoError(t, cfg.Validate("production"))
}

func TestSFTPConfigValidateRejectsMultipleAuthSources(t *testing.T) {
	t.Parallel()

	cfg := SFTPConfig{
		Host: "sftp.example.com",
		Port: 22,
		User: "svc",
		PasswordProvider: func(context.Context) (string, error) {
			return "strong-password-123", nil
		},
		Password:       "another-strong-password-123",
		RootPath:       "/uploads",
		KnownHostsFile: "/etc/ssh/ssh_known_hosts",
	}

	err := cfg.Validate("production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestSFTPConfigValidateRequiresCleanAbsoluteRoot(t *testing.T) {
	t.Parallel()

	base := SFTPConfig{
		Host:           "sftp.example.com",
		Port:           22,
		User:           "svc",
		Password:       "strong-password-123",
		KnownHostsFile: "/etc/ssh/ssh_known_hosts",
	}

	cfg := base
	cfg.RootPath = "uploads"
	err := cfg.Validate("production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")

	cfg = base
	cfg.RootPath = "/uploads/../secret-token"
	err = cfg.Validate("production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clean")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSFTPConfigKnownHostsErrorDoesNotExposePath(t *testing.T) {
	t.Parallel()

	_, err := New(SFTPConfig{
		Host:           "sftp.example.com",
		Port:           22,
		User:           "svc",
		Password:       "strong-password-123",
		RootPath:       "/uploads",
		KnownHostsFile: "/tmp/secret-token-known-hosts",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial connect")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "known_hosts")
}

func TestNewLazyConnectValidatesConfig(t *testing.T) {
	t.Parallel()

	_, err := New(SFTPConfig{
		Host:           "sftp.example.com",
		Port:           0,
		User:           "svc",
		Password:       "strong-password-123",
		RootPath:       "/uploads",
		KnownHostsFile: "/etc/ssh/ssh_known_hosts",
	}, WithLazyConnect())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port")

	_, err = New(SFTPConfig{
		Host:     "sftp.example.com",
		Port:     22,
		User:     "svc",
		Password: "strong-password-123",
		RootPath: "/uploads",
	}, WithLazyConnect())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KNOWN_HOSTS")

	_, err = New(SFTPConfig{
		Host:           "sftp.example.com",
		Port:           22,
		User:           "svc",
		Password:       "strong-password-123",
		RootPath:       "/uploads/../data",
		KnownHostsFile: "/etc/ssh/ssh_known_hosts",
	}, WithLazyConnect())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clean")

}
