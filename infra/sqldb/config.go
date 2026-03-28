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

// Config holds database connection settings for any SQL driver.
// Driver-specific settings (e.g. PostgreSQL sslmode, MySQL charset) go in
// the Options map. The Driver implementation reads the options it needs
// and ignores the rest.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	LogLevel string            // "info" for verbose SQL logging, default "warn"
	Options  map[string]string // driver-specific options (e.g. "sslmode", "charset", "tls")
}

// Option returns the value for the given driver-specific option key, or
// the fallback if not set. Keys are case-sensitive.
func (c Config) Option(key, fallback string) string {
	if v, ok := c.Options[key]; ok {
		return v
	}
	return fallback
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

// ParsePostgresDSN parses a PostgreSQL connection URI into a [Config].
// Accepted schemes: "postgres", "postgresql".
// Format: postgres://user:password@host:port/dbname?sslmode=require
//
// The password is automatically percent-decoded. Port defaults to 5432 if omitted.
// The sslmode query parameter is extracted if present.
// LogLevel is not part of the DSN and must be set separately.
func ParsePostgresDSN(rawURL string) (Config, error) {
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

// ParseMySQLDSN parses a MySQL/MariaDB connection URI into a [Config].
// Format: mysql://user:password@host:port/dbname
//
// The password is automatically percent-decoded. Port defaults to 3306 if omitted.
// LogLevel is not part of the DSN and must be set separately.
func ParseMySQLDSN(rawURL string) (Config, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Config{}, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if u.Scheme != "mysql" {
		return Config{}, fmt.Errorf("DATABASE_URL scheme must be mysql, got %q", u.Scheme)
	}

	port := 3306
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

	return Config{
		Host:     u.Hostname(),
		Port:     port,
		User:     user,
		Password: password,
		Name:     strings.TrimPrefix(u.Path, "/"),
	}, nil
}

// PoolConfig holds connection pool tuning parameters for *sql.DB.
// Services create their own GORM/sql setup but share pool sizing via this struct.
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
// Embed this in service configs that use any SQL database.
type Fields struct {
	Database     Config
	DatabasePool PoolConfig
}

// LoadFields reads database config from environment variables.
//
// If DATABASE_URL is set, it is parsed as a connection URI and takes
// precedence over individual environment variables. Pool config and log
// level are always read from their own env vars regardless of source.
//
// envPrefix determines the per-service env var names: e.g. "BACKEND" reads
// BACKEND_DB_USER, BACKEND_DB_PASSWORD, BACKEND_DB_NAME.
// defaultPort is the driver-specific default port (3306 for MySQL, 5432 for
// Postgres). driver is "mysql" or "postgres" and determines how DATABASE_URL
// is parsed. defaultMaxIdle and defaultMaxOpen set the pool size defaults.
func LoadFields(envPrefix string, defaultPort int, driver string, defaultMaxIdle, defaultMaxOpen int) (Fields, error) {
	dbPool, err := LoadPool(defaultMaxIdle, defaultMaxOpen)
	if err != nil {
		return Fields{}, err
	}

	// DATABASE_URL takes precedence when set.
	if dsnURL := config.GetSecret("DATABASE_URL", ""); dsnURL != "" {
		var cfg Config
		var parseErr error
		switch driver {
		case "mysql":
			cfg, parseErr = ParseMySQLDSN(dsnURL)
		default:
			cfg, parseErr = ParsePostgresDSN(dsnURL)
		}
		if parseErr != nil {
			return Fields{}, parseErr
		}
		cfg.LogLevel = config.Get("DB_LOG_LEVEL", "warn")
		return Fields{Database: cfg, DatabasePool: dbPool}, nil
	}

	// Fallback: individual env vars.
	p := &config.Parser{}
	dbPort := p.Int("DB_PORT", defaultPort)
	if err := p.Err(); err != nil {
		return Fields{}, err
	}

	opts := make(map[string]string)
	if driver == "postgres" {
		if sslMode := config.Get("DB_SSL_MODE", ""); sslMode != "" {
			opts["sslmode"] = strings.ToLower(sslMode)
		}
	}

	return Fields{
		Database: Config{
			Host:     config.Get("DB_HOST", "localhost"),
			Port:     dbPort,
			User:     config.Get(envPrefix+"_DB_USER", ""),
			Password: config.GetSecret(envPrefix+"_DB_PASSWORD", ""),
			Name:     config.Get(envPrefix+"_DB_NAME", ""),
			LogLevel: config.Get("DB_LOG_LEVEL", "warn"),
			Options:  opts,
		},
		DatabasePool: dbPool,
	}, nil
}

// Validate checks that all required database fields are present.
// In non-development environments, it also rejects weak credentials.
// driver should be "mysql" or "postgres" for driver-specific validation
// (e.g. SSLMode validation for Postgres).
func (f Fields) Validate(envPrefix, environment, driver string) error {
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
	if driver == "postgres" {
		if err := validatePostgresSSLMode(f.Database.Option("sslmode", "")); err != nil {
			return err
		}
	}
	if !config.IsDevelopment(environment) {
		if err := config.RejectWeakCredential(envPrefix+"_DB_PASSWORD", f.Database.Password); err != nil {
			return fmt.Errorf("%w (environment: %s)", err, environment)
		}
	}
	return nil
}

// validateDatabaseHost rejects host values containing characters that could
// break DSN parsing (e.g. ')' in MySQL DSN or '\x00' in any DSN).
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
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("DB_SSL_MODE must be one of disable, allow, prefer, require, verify-ca, verify-full")
	}
}
