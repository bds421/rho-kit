package sqldb

import (
	"log/slog"
)

// MySQLConfig is an alias for [Config].
//
// Deprecated: Use [Config] instead.
type MySQLConfig = Config

// PostgresConfig holds PostgreSQL connection settings.
//
// Deprecated: Use [Config] with the SSLMode field instead.
type PostgresConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string // "disable", "require", "verify-ca", "verify-full"
	LogLevel string // "info" for verbose SQL logging, default "warn"
}

// ToConfig converts a PostgresConfig to the unified Config type.
func (c PostgresConfig) ToConfig() Config {
	return Config{
		Host:     c.Host,
		Port:     c.Port,
		User:     c.User,
		Password: c.Password,
		Name:     c.Name,
		SSLMode:  c.SSLMode,
		LogLevel: c.LogLevel,
	}
}

// DSN returns a PostgreSQL data source name (keyword/value format).
// When tlsEnabled is true and SSLMode is empty, it defaults to "verify-full".
func (c PostgresConfig) DSN(tlsEnabled ...bool) string {
	return c.ToConfig().PostgresDSN(tlsEnabled...)
}

// LogValue implements slog.LogValuer to prevent accidental logging of credentials.
func (c PostgresConfig) LogValue() slog.Value {
	return c.ToConfig().LogValue()
}

// ParsePostgresDSNCompat parses a PostgreSQL connection URI into a
// [PostgresConfig].
//
// Deprecated: Use [ParsePostgresDSN] which returns the unified [Config]
// type instead.
func ParsePostgresDSNCompat(rawURL string) (PostgresConfig, error) {
	cfg, err := ParsePostgresDSN(rawURL)
	if err != nil {
		return PostgresConfig{}, err
	}
	return PostgresConfig{
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.User,
		Password: cfg.Password,
		Name:     cfg.Name,
		SSLMode:  cfg.SSLMode,
		LogLevel: cfg.LogLevel,
	}, nil
}

// MySQLFields is an alias for [Fields].
//
// Deprecated: Use [Fields] instead.
type MySQLFields = Fields

// PostgresFields holds PostgreSQL connection and pool configuration.
//
// Deprecated: Use [Fields] instead.
type PostgresFields struct {
	Database     PostgresConfig
	DatabasePool PoolConfig
}

// Deprecated: Use [LoadFields] with appropriate driver defaults instead.
// Example: LoadFields(envPrefix, 3306, "mysql", maxIdle, maxOpen)
func LoadMySQLFields(envPrefix string, defaultMaxIdle, defaultMaxOpen int) (Fields, error) {
	return LoadFields(envPrefix, 3306, "mysql", defaultMaxIdle, defaultMaxOpen)
}

// Deprecated: Use [LoadFields] with appropriate driver defaults instead.
// Example: LoadFields(envPrefix, 5432, "postgres", maxIdle, maxOpen)
func LoadPostgresFields(envPrefix string, defaultMaxIdle, defaultMaxOpen int) (PostgresFields, error) {
	f, err := LoadFields(envPrefix, 5432, "postgres", defaultMaxIdle, defaultMaxOpen)
	if err != nil {
		return PostgresFields{}, err
	}
	return PostgresFields{
		Database: PostgresConfig{
			Host:     f.Database.Host,
			Port:     f.Database.Port,
			User:     f.Database.User,
			Password: f.Database.Password,
			Name:     f.Database.Name,
			SSLMode:  f.Database.SSLMode,
			LogLevel: f.Database.LogLevel,
		},
		DatabasePool: f.DatabasePool,
	}, nil
}

// ValidateMySQL checks that all required MySQL/MariaDB fields are present.
//
// Deprecated: Use [Fields.Validate] with driver "mysql" instead.
func (f Fields) ValidateMySQL(envPrefix, environment string) error {
	return f.Validate(envPrefix, environment, "mysql")
}

// ValidatePostgres checks that all required PostgreSQL fields are present.
//
// Deprecated: Use [Fields.Validate] with driver "postgres" instead.
func (f PostgresFields) ValidatePostgres(envPrefix, environment string) error {
	unified := Fields{
		Database:     f.Database.ToConfig(),
		DatabasePool: f.DatabasePool,
	}
	return unified.Validate(envPrefix, environment, "postgres")
}

