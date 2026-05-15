package app

import (
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/security/v2/netutil"
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
	Host string // bind address; defaults to "127.0.0.1" — set to "0.0.0.0" only when the network is strictly isolated.
	Port int
}

// Addr returns the listen address for the internal server.
//
// The default (empty Host) resolves to loopback. The internal server
// exposes /metrics without authentication, so binding to 0.0.0.0 by
// default would leak Prometheus metrics (route patterns, tenant labels,
// process fingerprinting) to anyone on the network. Operators who genuinely
// need 0.0.0.0 (Docker healthcheck through host networking) must opt in
// explicitly by setting Host to "0.0.0.0" — and must pair that with
// [Builder.AllowInternalNonLoopback] so the always-on validator accepts
// the configuration.
func (c InternalConfig) Addr() string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
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

// LogValue implements slog.LogValuer to keep service config logging useful
// without exposing TLS certificate or private-key file paths.
func (c BaseConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("server_addr", c.Server.Addr()),
		slog.String("internal_addr", c.Internal.Addr()),
		slog.String("environment", c.Environment),
		slog.String("log_level", c.LogLevel),
		slog.Group("tls",
			slog.Bool("enabled", c.TLS.Enabled()),
			slog.Bool("ca_cert_configured", c.TLS.CACert != ""),
			slog.Bool("cert_configured", c.TLS.Cert != ""),
			slog.Bool("key_configured", c.TLS.Key != ""),
		),
	)
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
			Host: config.Get("INTERNAL_HOST", ""),
			Port: internalPort,
		},
		Environment: config.Get("ENVIRONMENT", "production"),
		LogLevel:    config.Get("LOG_LEVEL", "info"),
		TLS:         netutil.LoadTLS(),
	}, nil
}

// IsDevelopment reports whether the service is running in development
// mode (per [config.IsDevelopment]'s string check on c.Environment).
//
// The kit's runtime no longer consults this — production-safe defaults
// are unconditional, and per-relaxation Without*() opt-outs are the
// supported escape hatches. The method is preserved for downstream
// consumers' own logic (feature flags, log verbosity, debug routes
// they choose to mount in their own code) so they can branch on the
// same string the kit used to read.
func (c BaseConfig) IsDevelopment() bool {
	return config.IsDevelopment(c.Environment)
}

// ValidateBase checks universal config fields: server port range,
// internal-port range, and TLS config.
func (c BaseConfig) ValidateBase() error {
	if err := config.ValidatePort("server", c.Server.Port); err != nil {
		return err
	}
	if err := config.ValidatePort("internal", c.Internal.Port); err != nil {
		return err
	}
	if err := c.TLS.Validate(); err != nil {
		return err
	}
	return nil
}
