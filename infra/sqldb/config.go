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

// Config holds database connection settings for any SQL driver (MySQL,
// MariaDB, PostgreSQL). Driver-specific fields like SSLMode are ignored
// by drivers that do not use them.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string // PostgreSQL only: "disable", "require", "verify-ca", "verify-full"
	LogLevel string // "info" for verbose SQL logging, default "warn"
}

// MySQLDSN returns a MySQL/MariaDB data source name.
// Special characters in user/password are escaped to prevent DSN parsing errors.
// url.QueryEscape is used instead of url.PathEscape because PathEscape does not
// encode '@' or '/' which are DSN delimiters.
// The optional first argument can be a bool (true enables tls=custom) or
// a string TLS config name registered via mysql.RegisterTLSConfig.
func (c Config) MySQLDSN(tlsOpt ...any) string {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&clientFoundRows=true",
		url.QueryEscape(c.User), url.QueryEscape(c.Password), c.Host, c.Port, url.QueryEscape(c.Name))
	if len(tlsOpt) > 0 {
		switch v := tlsOpt[0].(type) {
		case bool:
			if v {
				dsn += "&tls=custom"
			}
		case string:
			if v != "" {
				dsn += "&tls=" + url.QueryEscape(v)
			}
		}
	}
	return dsn
}

// DSN returns the MySQL/MariaDB data source name (alias for [MySQLDSN]).
// The optional first argument can be a bool (true enables tls=custom) or
// a string TLS config name registered via mysql.RegisterTLSConfig.
func (c Config) DSN(tlsOpt ...any) string {
	return c.MySQLDSN(tlsOpt...)
}

// PostgresDSN returns a PostgreSQL data source name (keyword/value format).
// When tlsEnabled is true and SSLMode is empty, it defaults to "verify-full".
func (c Config) PostgresDSN(tlsEnabled ...bool) string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
		if len(tlsEnabled) > 0 && tlsEnabled[0] {
			sslMode = "verify-full"
		}
	}
	return fmt.Sprintf("host='%s' port=%d user='%s' password='%s' dbname='%s' sslmode='%s'",
		escapePostgresDSNValue(c.Host), c.Port, escapePostgresDSNValue(c.User), escapePostgresDSNValue(c.Password), escapePostgresDSNValue(c.Name), escapePostgresDSNValue(sslMode))
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

// escapePostgresDSNValue escapes single quotes and backslashes in a value
// for use inside single-quoted PostgreSQL keyword/value DSN parameters.
// Also strips null bytes (which terminate C strings in libpq) and replaces
// newlines (which break keyword/value parsing).
func escapePostgresDSNValue(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
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

	return Config{
		Host:     u.Hostname(),
		Port:     port,
		User:     user,
		Password: password,
		Name:     strings.TrimPrefix(u.Path, "/"),
		SSLMode:  u.Query().Get("sslmode"),
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

	sslMode := ""
	if driver == "postgres" {
		sslMode = config.Get("DB_SSL_MODE", "")
		if sslMode != "" {
			sslMode = strings.ToLower(sslMode)
		}
	}

	return Fields{
		Database: Config{
			Host:     config.Get("DB_HOST", "localhost"),
			Port:     dbPort,
			User:     config.Get(envPrefix+"_DB_USER", ""),
			Password: config.GetSecret(envPrefix+"_DB_PASSWORD", ""),
			Name:     config.Get(envPrefix+"_DB_NAME", ""),
			SSLMode:  sslMode,
			LogLevel: config.Get("DB_LOG_LEVEL", "warn"),
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
		if err := validatePostgresSSLMode(f.Database.SSLMode); err != nil {
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
