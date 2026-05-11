package sftpbackend

import (
	"fmt"
	"log/slog"
	"path"

	"github.com/bds421/rho-kit/core/v2/config"
)

// SFTPConfig holds SFTP connection settings.
type SFTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string // mutually exclusive with KeyFile
	KeyFile  string // path to SSH private key
	RootPath string // base path on the remote server

	// KnownHostsFile is the path to an OpenSSH known_hosts file for SSH
	// host key verification.
	// Example: "/etc/ssh/ssh_known_hosts" or "~/.ssh/known_hosts".
	KnownHostsFile string
}

// LogValue implements slog.LogValuer to prevent logging credentials.
func (c SFTPConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("host_configured", c.Host != ""),
		slog.Int("port", c.Port),
		slog.Bool("user_configured", c.User != ""),
		slog.Bool("password_configured", c.Password != ""),
		slog.Bool("key_file_configured", c.KeyFile != ""),
		slog.Bool("root_path_configured", c.RootPath != ""),
		slog.Bool("known_hosts_file_configured", c.KnownHostsFile != ""),
	)
}

// LoadSFTPConfig reads SFTP settings from environment variables.
//
// Environment variables:
//   - STORAGE_SFTP_HOST (required)
//   - STORAGE_SFTP_PORT (default 22)
//   - STORAGE_SFTP_USER (required)
//   - {envPrefix}_SFTP_PASSWORD (supports _FILE suffix; mutually exclusive with KeyFile)
//   - STORAGE_SFTP_KEY_FILE (path to SSH private key)
//   - STORAGE_SFTP_ROOT_PATH (default "/")
//   - STORAGE_SFTP_KNOWN_HOSTS_FILE (required; OpenSSH known_hosts path)
func LoadSFTPConfig(envPrefix, environment string) (SFTPConfig, error) {
	p := &config.Parser{}
	port := p.Int("STORAGE_SFTP_PORT", 22)
	if err := p.Err(); err != nil {
		return SFTPConfig{}, err
	}

	cfg := SFTPConfig{
		Host:           config.Get("STORAGE_SFTP_HOST", ""),
		Port:           port,
		User:           config.Get("STORAGE_SFTP_USER", ""),
		Password:       config.MustGetSecret(envPrefix+"_SFTP_PASSWORD", ""),
		KeyFile:        config.Get("STORAGE_SFTP_KEY_FILE", ""),
		RootPath:       config.Get("STORAGE_SFTP_ROOT_PATH", "/"),
		KnownHostsFile: config.Get("STORAGE_SFTP_KNOWN_HOSTS_FILE", ""),
	}

	if err := cfg.Validate(environment); err != nil {
		return SFTPConfig{}, err
	}

	return cfg, nil
}

// Validate checks that required SFTP fields are present.
func (c SFTPConfig) Validate(environment string) error {
	if c.Host == "" {
		return fmt.Errorf("STORAGE_SFTP_HOST is required")
	}
	if err := config.ValidatePort("sftp", c.Port); err != nil {
		return err
	}
	if c.User == "" {
		return fmt.Errorf("STORAGE_SFTP_USER is required")
	}
	if c.Password == "" && c.KeyFile == "" {
		return fmt.Errorf("either SFTP_PASSWORD or STORAGE_SFTP_KEY_FILE is required")
	}
	if c.Password != "" && c.KeyFile != "" {
		return fmt.Errorf("SFTP_PASSWORD and STORAGE_SFTP_KEY_FILE are mutually exclusive")
	}
	if c.Password != "" {
		if err := config.RejectWeakCredential("SFTP_PASSWORD", c.Password); err != nil {
			return err
		}
	}
	if c.RootPath == "" {
		return fmt.Errorf("STORAGE_SFTP_ROOT_PATH must not be empty")
	}
	if !path.IsAbs(c.RootPath) {
		return fmt.Errorf("STORAGE_SFTP_ROOT_PATH must be absolute")
	}
	if cleaned := path.Clean(c.RootPath); cleaned != c.RootPath {
		return fmt.Errorf("STORAGE_SFTP_ROOT_PATH must be clean")
	}
	if c.KnownHostsFile == "" {
		return fmt.Errorf("STORAGE_SFTP_KNOWN_HOSTS_FILE is required")
	}
	_ = environment // accepted for API compatibility; no longer consulted
	return nil
}
