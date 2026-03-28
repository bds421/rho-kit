package gormpostgres

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

// Compile-time interface check.
var _ gormdb.Driver = PostgresDriver{}

// PostgresDriver implements [gormdb.Driver] for PostgreSQL. It constructs the
// DSN from the unified [sqldb.Config] and opens a GORM connection using the
// pgx/v5 driver.
type PostgresDriver struct{}

// Name returns "postgres".
func (PostgresDriver) Name() string { return "postgres" }

// Open creates a GORM database connection to PostgreSQL using the unified
// config. It merges clientTLS settings when non-nil and applies connection
// pool settings.
func (PostgresDriver) Open(
	cfg sqldb.Config,
	pool sqldb.PoolConfig,
	logger *slog.Logger,
	clientTLS *tls.Config,
) (*gorm.DB, error) {
	tlsEnabled := clientTLS != nil
	if tlsEnabled {
		logger.Info("database TLS enabled")
	}

	dsn := buildPostgresDSN(cfg, tlsEnabled)

	pgCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	if tlsEnabled {
		mergeTLS(pgCfg, clientTLS, cfg.Host)
	}

	options := buildTimezoneOptions(dsn, pgCfg)

	sqlDB := stdlib.OpenDB(*pgCfg, options...)

	logLevel := gormlogger.Warn
	if cfg.LogLevel == "info" {
		logLevel = gormlogger.Info
	}

	dialector := postgres.New(postgres.Config{Conn: sqlDB})
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		logger.Warn("database connection failed",
			"host", cfg.Host, "name", cfg.Name, "error", err)
		_ = sqlDB.Close()
		return nil, fmt.Errorf("connect to database %s@%s:%d/%s: connection failed",
			cfg.User, cfg.Host, cfg.Port, cfg.Name)
	}

	applyPool(sqlDB, pool)

	if err := ping(sqlDB, logger, cfg); err != nil {
		return nil, err
	}

	return db, nil
}

// buildPostgresDSN constructs a PostgreSQL keyword/value DSN from the unified Config.
func buildPostgresDSN(cfg sqldb.Config, tlsEnabled bool) string {
	sslMode := cfg.Option("sslmode", "disable")
	if sslMode == "disable" && tlsEnabled {
		sslMode = "verify-full"
	}
	return fmt.Sprintf(
		"host='%s' port=%d user='%s' password='%s' dbname='%s' sslmode='%s'",
		escapePostgresValue(cfg.Host),
		cfg.Port,
		escapePostgresValue(cfg.User),
		escapePostgresValue(cfg.Password),
		escapePostgresValue(cfg.Name),
		escapePostgresValue(sslMode),
	)
}

// escapePostgresValue escapes single quotes, backslashes, and control
// characters for PostgreSQL keyword/value DSN format.
func escapePostgresValue(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// mergeTLS merges caller-provided TLS settings into the parsed pgx config.
func mergeTLS(pgCfg *pgx.ConnConfig, clientTLS *tls.Config, host string) {
	var tlsCfg *tls.Config
	if pgCfg.TLSConfig != nil {
		tlsCfg = pgCfg.TLSConfig.Clone()
	} else {
		tlsCfg = &tls.Config{}
	}

	if clientTLS.RootCAs != nil {
		tlsCfg.RootCAs = clientTLS.RootCAs
	}
	if len(clientTLS.Certificates) > 0 {
		tlsCfg.Certificates = clientTLS.Certificates
	}
	if clientTLS.MinVersion != 0 {
		tlsCfg.MinVersion = clientTLS.MinVersion
	}
	if clientTLS.MaxVersion != 0 {
		tlsCfg.MaxVersion = clientTLS.MaxVersion
	}
	if clientTLS.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true
	}

	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = host
	}
	pgCfg.TLSConfig = tlsCfg
}

// buildTimezoneOptions extracts timezone configuration from the DSN and
// returns stdlib options that register the timezone codec after connect.
func buildTimezoneOptions(dsn string, pgCfg *pgx.ConnConfig) []stdlib.OptionOpenDB {
	result := timeZoneMatcher.FindStringSubmatch(dsn)
	if len(result) <= 2 {
		return nil
	}

	tz := result[2]
	pgCfg.RuntimeParams["timezone"] = tz

	return []stdlib.OptionOpenDB{
		stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			loc, tzErr := time.LoadLocation(tz)
			if tzErr != nil {
				return tzErr
			}
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name:  "timestamp",
				OID:   pgtype.TimestampOID,
				Codec: &pgtype.TimestampCodec{ScanLocation: loc},
			})
			return nil
		}),
	}
}

// applyPool configures the connection pool on the underlying sql.DB.
func applyPool(sqlDB interface {
	SetMaxIdleConns(int)
	SetMaxOpenConns(int)
	SetConnMaxLifetime(time.Duration)
	SetConnMaxIdleTime(time.Duration)
}, pool sqldb.PoolConfig) {
	sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
}

// ping verifies connectivity with a 5-second timeout.
func ping(sqlDB interface {
	PingContext(context.Context) error
	Close() error
}, logger *slog.Logger, cfg sqldb.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("ping database: %w", err)
	}

	logger.Info("database connected", "host", cfg.Host, "name", cfg.Name)
	return nil
}
