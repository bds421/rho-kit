package app

import (
	"fmt"

	"github.com/bds421/rho-kit/core/config"
	"github.com/bds421/rho-kit/security/netutil"
)

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string
	Port int
}

// Addr returns the host:port string for the HTTP server.
func (c ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// InternalConfig holds settings for the internal ops HTTP server (health, ready, metrics).
type InternalConfig struct {
	Host string // bind address; defaults to "0.0.0.0" for Docker healthcheck access
	Port int
}

// Addr returns the listen address for the internal server.
func (c InternalConfig) Addr() string {
	host := c.Host
	if host == "" {
		host = "0.0.0.0"
	}
	return fmt.Sprintf("%s:%d", host, c.Port)
}

// BaseConfig holds the universal configuration fields shared by every service:
// HTTP server, internal ops server, environment mode, log level, and TLS settings.
type BaseConfig struct {
	Server      ServerConfig
	Internal    InternalConfig
	Environment string
	LogLevel    string // "debug", "info", "warn", "error"; default "info"
	TLS         netutil.TLSConfig
}

// LoadBaseConfig reads the universal config fields from environment variables.
// defaultServerPort is the only value that varies per service (e.g. 8080 for backend,
// 8084 for file-copier).
func LoadBaseConfig(defaultServerPort int) (BaseConfig, error) {
	p := &config.Parser{}
	serverPort := p.Int("SERVER_PORT", defaultServerPort)
	internalPort := p.Int("INTERNAL_PORT", 9090)
	if err := p.Err(); err != nil {
		return BaseConfig{}, err
	}

	return BaseConfig{
		Server: ServerConfig{
			Host: config.Get("SERVER_HOST", "0.0.0.0"),
			Port: serverPort,
		},
		Internal: InternalConfig{
			Port: internalPort,
		},
		Environment: config.Get("ENVIRONMENT", "production"),
		LogLevel:    config.Get("LOG_LEVEL", "info"),
		TLS:         netutil.LoadTLS(),
	}, nil
}

// IsDevelopment reports whether the service is running in development mode.
func (c BaseConfig) IsDevelopment() bool {
	return config.IsDevelopment(c.Environment)
}

// ValidateBase checks universal config fields: server port range and TLS config.
func (c BaseConfig) ValidateBase() error {
	if err := config.ValidatePort("server", c.Server.Port); err != nil {
		return err
	}
	if err := c.TLS.Validate(); err != nil {
		return err
	}
	return nil
}
