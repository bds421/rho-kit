package sqldb

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// Deprecated: Use [Config] instead.
type MySQLConfig = Config

// Deprecated: Use [Config] with Options["sslmode"] instead.
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
	opts := make(map[string]string)
	if c.SSLMode != "" {
		opts["sslmode"] = c.SSLMode
	}
	return Config{
		Host:     c.Host,
		Port:     c.Port,
		User:     c.User,
		Password: c.Password,
		Name:     c.Name,
		LogLevel: c.LogLevel,
		Options:  opts,
	}
}

// Deprecated: DSN building is a driver concern.
func (c PostgresConfig) DSN(tlsEnabled ...bool) string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
		if len(tlsEnabled) > 0 && tlsEnabled[0] {
			sslMode = "verify-full"
		}
	}
	return fmt.Sprintf("host='%s' port=%d user='%s' password='%s' dbname='%s' sslmode='%s'",
		escapePostgresDSNValue(c.Host), c.Port, escapePostgresDSNValue(c.User),
		escapePostgresDSNValue(c.Password), escapePostgresDSNValue(c.Name),
		escapePostgresDSNValue(sslMode))
}

// LogValue implements slog.LogValuer to prevent accidental logging of credentials.
func (c PostgresConfig) LogValue() slog.Value {
	return c.ToConfig().LogValue()
}

// Deprecated: DSN building is a driver concern.
func (c Config) DSN(tlsOpt ...any) string {
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

// escapePostgresDSNValue escapes single quotes and backslashes for PostgreSQL DSN.
func escapePostgresDSNValue(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// Deprecated: Use [ParsePostgresDSN] instead.
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
		SSLMode:  cfg.Option("sslmode", ""),
		LogLevel: cfg.LogLevel,
	}, nil
}

// Deprecated: Use [Fields] instead.
type MySQLFields = Fields

// Deprecated: Use [Fields] instead.
type PostgresFields struct {
	Database     PostgresConfig
	DatabasePool PoolConfig
}

// Deprecated: Use [LoadFields] instead.
func LoadMySQLFields(envPrefix string, defaultMaxIdle, defaultMaxOpen int) (Fields, error) {
	return LoadFields(envPrefix, 3306, "mysql", defaultMaxIdle, defaultMaxOpen)
}

// Deprecated: Use [LoadFields] instead.
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
			SSLMode:  f.Database.Option("sslmode", ""),
			LogLevel: f.Database.LogLevel,
		},
		DatabasePool: f.DatabasePool,
	}, nil
}

// Deprecated: Use [Fields.Validate] instead.
func (f Fields) ValidateMySQL(envPrefix, environment string) error {
	return f.Validate(envPrefix, environment, "mysql")
}

// Deprecated: Use [Fields.Validate] instead.
func (f PostgresFields) ValidatePostgres(envPrefix, environment string) error {
	unified := Fields{
		Database:     f.Database.ToConfig(),
		DatabasePool: f.DatabasePool,
	}
	return unified.Validate(envPrefix, environment, "postgres")
}
