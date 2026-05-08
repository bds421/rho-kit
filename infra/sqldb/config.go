package sqldb

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/config"
)

// Config holds PostgreSQL connection settings. v2 dropped MySQL/MariaDB
// support — pgx + sqlc is the canonical data path now and there is no
// reason to abstract over a database the kit no longer ships.
//
// Driver-specific tuning (sslmode, application_name, statement_timeout)
// goes in Options. The driver layer reads what it needs and ignores
// the rest, so adding a new pg-side knob is a one-line key/value pair.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	LogLevel string            // "info" for verbose SQL logging, default "warn"
	Options  map[string]string // pg-specific options (e.g. "sslmode", "application_name")
}

// Option returns the value for the given option key, or the fallback if
// not set. Keys are case-sensitive.
func (c Config) Option(key, fallback string) string {
	if v, ok := c.Options[key]; ok {
		return v
	}
	return fallback
}

// IsTLSEnabled reports whether the configured options *require* a TLS
// connection that fails closed on handshake error: sslmode in
// {require, verify-ca, verify-full}. "prefer" and "allow" silently
// degrade to plaintext and are NOT considered enabled.
//
// Production-validation paths use this helper so the kit stays the
// single source of truth for what counts as a hardened TLS setup.
func (c Config) IsTLSEnabled() bool {
	switch strings.ToLower(c.Option("sslmode", "")) {
	case "require", "verify-ca", "verify-full":
		return true
	}
	return false
}

// IsTLSAttempted is a softer companion to [Config.IsTLSEnabled] that
// also returns true for modes which *attempt* TLS but do not fail
// closed (Postgres "prefer" / "allow"). Use only for telemetry — never
// for production gating, because the connection can still end up in
// plaintext.
func (c Config) IsTLSAttempted() bool {
	if c.IsTLSEnabled() {
		return true
	}
	switch strings.ToLower(c.Option("sslmode", "")) {
	case "allow", "prefer":
		return true
	}
	return false
}

// LogValue implements slog.LogValuer to prevent accidental logging of credentials.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", c.Host),
		slog.Int("port", c.Port),
		slog.String("user", c.User),
		slog.String("name", c.Name),
		slog.String("password", "[REDACTED]"),
	)
}

// ParseDSN parses a PostgreSQL connection URI into a [Config].
// Accepted schemes: "postgres", "postgresql".
// Format: postgres://user:password@host:port/dbname?sslmode=require
//
// The password is automatically percent-decoded. Port defaults to 5432
// if omitted. The sslmode query parameter is extracted into Options.
// LogLevel is not part of the DSN and must be set separately.
func ParseDSN(rawURL string) (Config, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Config{}, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return Config{}, fmt.Errorf("DATABASE_URL scheme must be postgres or postgresql, got %q", u.Scheme)
	}

	port := 5432
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return Config{}, fmt.Errorf("invalid port in DATABASE_URL: %w", err)
		}
	}

	var user, password string
	if u.User != nil {
		user = u.User.Username()
		password, _ = u.User.Password()
	}

	var opts map[string]string
	if sslMode := u.Query().Get("sslmode"); sslMode != "" {
		opts = map[string]string{"sslmode": sslMode}
	}

	return Config{
		Host:     u.Hostname(),
		Port:     port,
		User:     user,
		Password: password,
		Name:     strings.TrimPrefix(u.Path, "/"),
		Options:  opts,
	}, nil
}

// PoolConfig holds connection pool tuning parameters.
type PoolConfig struct {
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPool returns sensible defaults for most services.
func DefaultPool() PoolConfig {
	return PoolConfig{
		MaxIdleConns:    10,
		MaxOpenConns:    100,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 5 * time.Minute,
	}
}

// LoadPool reads pool tuning from environment variables, falling back to
// the provided defaults.
//
// Environment variables:
//   - DB_POOL_MAX_IDLE_CONNS   (default: defaultMaxIdle)
//   - DB_POOL_MAX_OPEN_CONNS   (default: defaultMaxOpen)
//   - DB_POOL_CONN_MAX_LIFETIME_MIN (default: 60)
//   - DB_POOL_CONN_MAX_IDLE_TIME_MIN (default: 5)
func LoadPool(defaultMaxIdle, defaultMaxOpen int) (PoolConfig, error) {
	p := &config.Parser{}
	maxIdle := p.Int("DB_POOL_MAX_IDLE_CONNS", defaultMaxIdle)
	maxOpen := p.Int("DB_POOL_MAX_OPEN_CONNS", defaultMaxOpen)
	lifetimeMin := p.Int("DB_POOL_CONN_MAX_LIFETIME_MIN", 60)
	idleMin := p.Int("DB_POOL_CONN_MAX_IDLE_TIME_MIN", 5)
	if err := p.Err(); err != nil {
		return PoolConfig{}, err
	}
	return PoolConfig{
		MaxIdleConns:    maxIdle,
		MaxOpenConns:    maxOpen,
		ConnMaxLifetime: time.Duration(lifetimeMin) * time.Minute,
		ConnMaxIdleTime: time.Duration(idleMin) * time.Minute,
	}, nil
}

// Fields holds database connection and pool configuration.
// Embed in service configs that use a SQL database.
type Fields struct {
	Database     Config
	DatabasePool PoolConfig
}

// LoadFields reads PostgreSQL config from environment variables.
//
// If DATABASE_URL is set, it is parsed as a connection URI and takes
// precedence over individual environment variables. Pool config and log
// level are always read from their own env vars regardless of source.
//
// envPrefix determines the per-service env var names: e.g. "BACKEND"
// reads BACKEND_DB_USER, BACKEND_DB_PASSWORD, BACKEND_DB_NAME.
func LoadFields(envPrefix string, defaultMaxIdle, defaultMaxOpen int) (Fields, error) {
	dbPool, err := LoadPool(defaultMaxIdle, defaultMaxOpen)
	if err != nil {
		return Fields{}, err
	}

	if dsnURL := config.MustGetSecret("DATABASE_URL", ""); dsnURL != "" {
		cfg, parseErr := ParseDSN(dsnURL)
		if parseErr != nil {
			return Fields{}, parseErr
		}
		cfg.LogLevel = config.Get("DB_LOG_LEVEL", "warn")
		return Fields{Database: cfg, DatabasePool: dbPool}, nil
	}

	p := &config.Parser{}
	dbPort := p.Int("DB_PORT", 5432)
	if err := p.Err(); err != nil {
		return Fields{}, err
	}

	opts := make(map[string]string)
	if sslMode := config.Get("DB_SSL_MODE", ""); sslMode != "" {
		opts["sslmode"] = strings.ToLower(sslMode)
	}

	return Fields{
		Database: Config{
			Host:     config.Get("DB_HOST", "localhost"),
			Port:     dbPort,
			User:     config.Get(envPrefix+"_DB_USER", ""),
			Password: config.MustGetSecret(envPrefix+"_DB_PASSWORD", ""),
			Name:     config.Get(envPrefix+"_DB_NAME", ""),
			LogLevel: config.Get("DB_LOG_LEVEL", "warn"),
			Options:  opts,
		},
		DatabasePool: dbPool,
	}, nil
}

// Validate checks that all required database fields are present and
// the postgres-specific TLS setting is hardened. In non-development
// environments, it also rejects weak credentials.
func (f Fields) Validate(envPrefix string) error {
	if err := config.ValidatePort("database", f.Database.Port); err != nil {
		return err
	}
	if f.Database.Host == "" {
		return fmt.Errorf("DB_HOST is required")
	}
	if err := validateDatabaseHost(f.Database.Host); err != nil {
		return err
	}
	if f.Database.User == "" {
		return fmt.Errorf("%s_DB_USER is required", envPrefix)
	}
	if f.Database.Password == "" {
		return fmt.Errorf("%s_DB_PASSWORD is required", envPrefix)
	}
	if f.Database.Name == "" {
		return fmt.Errorf("%s_DB_NAME is required", envPrefix)
	}
	sslMode := f.Database.Option("sslmode", "")
	if err := validatePostgresSSLMode(sslMode); err != nil {
		return err
	}
	// TLS is unconditional. Empty or "disable" fails loud rather than
	// silently shipping credentials and queries on the wire.
	normalized := strings.ToLower(sslMode)
	if normalized == "" || normalized == "disable" {
		return fmt.Errorf("%s_DB_SSL_MODE must be set to require/verify-ca/verify-full (got %q)", envPrefix, sslMode)
	}
	return config.RejectWeakCredential(envPrefix+"_DB_PASSWORD", f.Database.Password)
}

// validateDatabaseHost rejects host values containing characters that
// could break DSN parsing.
func validateDatabaseHost(host string) error {
	if net.ParseIP(host) != nil {
		return nil
	}
	for _, c := range host {
		if c == ')' || c == '/' || c == '\'' || c == '\\' || c == '\x00' || c == '@' || c == '\n' || c == '\r' {
			return fmt.Errorf("DB_HOST contains invalid character %q", c)
		}
	}
	return nil
}

func validatePostgresSSLMode(mode string) error {
	if mode == "" {
		return nil
	}
	switch strings.ToLower(mode) {
	case "require", "verify-ca", "verify-full":
		return nil
	case "disable":
		// Reported separately by the caller with full context.
		return nil
	case "allow", "prefer":
		// Both modes silently degrade to plaintext on TLS handshake error.
		return fmt.Errorf("DB_SSL_MODE=%q admits a plaintext fallback on TLS handshake error; use require, verify-ca, or verify-full", mode)
	default:
		return fmt.Errorf("DB_SSL_MODE must be require, verify-ca, or verify-full (got %q)", mode)
	}
}
