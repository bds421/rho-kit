package sqldb

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// Config holds PostgreSQL connection settings. pgx + sqlc is the
// canonical data path; the kit ships PostgreSQL only and does not
// abstract over alternative SQL engines.
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
		slog.Bool("host_configured", c.Host != ""),
		slog.Int("port", c.Port),
		slog.Bool("user_configured", c.User != ""),
		slog.Bool("name_configured", c.Name != ""),
		slog.Bool("password_configured", c.Password != ""),
		slog.String("log_level", c.LogLevel),
		slog.Bool("options_configured", len(c.Options) > 0),
		slog.Bool("tls_enabled", c.IsTLSEnabled()),
		slog.Bool("tls_attempted", c.IsTLSAttempted()),
	)
}

// ParseDSN parses a PostgreSQL connection URI into a [Config].
// Accepted schemes: "postgres", "postgresql".
// Format: postgres://user:password@host:port/dbname?sslmode=verify-full
//
// The password is automatically percent-decoded. Port defaults to 5432
// if omitted. All non-empty query parameters are copied into Options (repeated keys are rejected).
// LogLevel is not part of the DSN and must be set separately.
func ParseDSN(rawURL string) (Config, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return Config{}, fmt.Errorf("sqldb: invalid port in DATABASE_URL")
		}
		return Config{}, fmt.Errorf("sqldb: DATABASE_URL is invalid")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return Config{}, fmt.Errorf("sqldb: DATABASE_URL scheme must be postgres or postgresql")
	}
	if u.Host == "" {
		return Config{}, fmt.Errorf("sqldb: DATABASE_URL host is required")
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return Config{}, fmt.Errorf("sqldb: DATABASE_URL database name is required")
	}

	port := 5432
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return Config{}, redact.WrapError("sqldb: invalid port in DATABASE_URL", err)
		}
	}

	var user, password string
	if u.User != nil {
		user = u.User.Username()
		password, _ = u.User.Password()
	}

	opts := make(map[string]string)
	query := u.Query()
	for key, values := range query {
		if len(values) > 1 {
			return Config{}, fmt.Errorf("sqldb: DATABASE_URL query parameter %q must not be repeated", key)
		}
		if len(values) == 1 && values[0] != "" {
			opts[key] = values[0]
		}
	}
	if len(opts) == 0 {
		opts = nil
	}

	return Config{
		Host:     u.Hostname(),
		Port:     port,
		User:     user,
		Password: password,
		Name:     dbName,
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
		return fmt.Errorf("sqldb: DB_HOST is required")
	}
	if err := validateDatabaseHost(f.Database.Host); err != nil {
		return err
	}
	if f.Database.User == "" {
		return fmt.Errorf("sqldb: %s_DB_USER is required", envPrefix)
	}
	if f.Database.Password == "" {
		return fmt.Errorf("sqldb: %s_DB_PASSWORD is required", envPrefix)
	}
	if f.Database.Name == "" {
		return fmt.Errorf("sqldb: %s_DB_NAME is required", envPrefix)
	}
	sslMode := f.Database.Option("sslmode", "")
	if err := validatePostgresSSLMode(sslMode); err != nil {
		return err
	}
	// TLS is unconditional. Empty or "disable" fails loud rather than
	// silently shipping credentials and queries on the wire.
	normalized := strings.ToLower(sslMode)
	if normalized == "" || normalized == "disable" {
		return fmt.Errorf("sqldb: %s_DB_SSL_MODE must be set to require/verify-ca/verify-full", envPrefix)
	}
	return config.RejectWeakCredential(envPrefix+"_DB_PASSWORD", f.Database.Password)
}

// validateDatabaseHost rejects host values that are neither a valid IP nor a
// hostname built only from RFC 1123 label characters (letters, digits, '-',
// '.', and '_' for internal DNS names). An allowlist is used rather than a
// character blocklist because the blocklist previously admitted whitespace,
// '=' and ':' — characters that let a consumer hand-building a libpq keyword
// DSN from Config.Host smuggle extra connection keywords
// (e.g. "localhost sslmode=disable"). The error never echoes the value, which
// may carry secrets.
func validateDatabaseHost(host string) error {
	if net.ParseIP(host) != nil {
		return nil
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '.', c == '_':
			// allowed RFC 1123 hostname character
		default:
			return fmt.Errorf("sqldb: DB_HOST contains invalid character")
		}
	}
	return nil
}

func validatePostgresSSLMode(mode string) error {
	if mode == "" {
		return nil
	}
	switch strings.ToLower(mode) {
	case "verify-ca", "verify-full":
		return nil
	case "require":
		// Aligns with infra/sqldb/pgx's FR-079 default: `require`
		// admits MITM because libpq does not verify server identity.
		// Operators on a closed network can opt back in at the dial
		// layer via pgx.Config.AllowSSLModeRequire; the preflight
		// remains strict so the policy is loud at config-load time.
		return fmt.Errorf("sqldb: DB_SSL_MODE=require admits MITM (no server identity verification); use verify-ca or verify-full, or opt in at dial time via pgx.Config.AllowSSLModeRequire on a closed network")
	case "disable":
		// Reported separately by the caller with full context.
		return nil
	case "allow", "prefer":
		// Both modes silently degrade to plaintext on TLS handshake error.
		return fmt.Errorf("sqldb: DB_SSL_MODE admits a plaintext fallback on TLS handshake error; use verify-ca or verify-full")
	default:
		return fmt.Errorf("sqldb: DB_SSL_MODE must be verify-ca or verify-full")
	}
}
