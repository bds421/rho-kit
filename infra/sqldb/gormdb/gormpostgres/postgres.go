// Package gormpostgres provides a GORM connection factory for PostgreSQL
// using the pgx/v5 driver. Import this package only when your service
// uses PostgreSQL — it does not pull in MySQL dependencies.
package gormpostgres

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// Option configures the PostgreSQL connection.
type Option func(*pgOpts)

type pgOpts struct {
	tlsConfig *tls.Config
}

var timeZoneMatcher = regexp.MustCompile(`(time_zone|TimeZone|timezone)=(.*?)($|&| )`)

// WithTLS enables mTLS for the PostgreSQL connection.
func WithTLS(cfg *tls.Config) Option {
	return func(o *pgOpts) { o.tlsConfig = cfg }
}

// New opens a GORM database connection to PostgreSQL with connection pooling.
func New(cfg sqldb.PostgresConfig, poolCfg sqldb.PoolConfig, logger *slog.Logger, opts ...Option) (*gorm.DB, error) {
	var o pgOpts
	for _, opt := range opts {
		opt(&o)
	}

	tlsEnabled := o.tlsConfig != nil
	if tlsEnabled {
		logger.Info("database TLS enabled")
	}

	logLevel := gormlogger.Warn
	if cfg.LogLevel == "info" {
		logLevel = gormlogger.Info
	}

	dsn := cfg.PostgresDSN(tlsEnabled)

	pgCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	if tlsEnabled {
		var tlsCfg *tls.Config
		if pgCfg.TLSConfig != nil {
			tlsCfg = pgCfg.TLSConfig.Clone()
		} else {
			tlsCfg = &tls.Config{}
		}

		// Merge caller-provided TLS settings into the parsed config.
		if o.tlsConfig != nil {
			if o.tlsConfig.RootCAs != nil {
				tlsCfg.RootCAs = o.tlsConfig.RootCAs
			}
			if len(o.tlsConfig.Certificates) > 0 {
				tlsCfg.Certificates = o.tlsConfig.Certificates
			}
			if o.tlsConfig.MinVersion != 0 {
				tlsCfg.MinVersion = o.tlsConfig.MinVersion
			}
			if o.tlsConfig.MaxVersion != 0 {
				tlsCfg.MaxVersion = o.tlsConfig.MaxVersion
			}
			if o.tlsConfig.InsecureSkipVerify {
				tlsCfg.InsecureSkipVerify = true
			}
		}

		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = cfg.Host
		}
		pgCfg.TLSConfig = tlsCfg
	}

	// Preserve time zone handling from the gorm.io/driver/postgres dialector.
	var options []stdlib.OptionOpenDB
	if result := timeZoneMatcher.FindStringSubmatch(dsn); len(result) > 2 {
		pgCfg.RuntimeParams["timezone"] = result[2]
		options = append(options, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			loc, tzErr := time.LoadLocation(result[2])
			if tzErr != nil {
				return tzErr
			}
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name:  "timestamp",
				OID:   pgtype.TimestampOID,
				Codec: &pgtype.TimestampCodec{ScanLocation: loc},
			})
			return nil
		}))
	}

	sqlDB := stdlib.OpenDB(*pgCfg, options...)
	dialector := postgres.New(postgres.Config{Conn: sqlDB})
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		logger.Warn("database connection failed", "host", cfg.Host, "name", cfg.Name, "error", err)
		_ = sqlDB.Close()
		return nil, fmt.Errorf("connect to database %s@%s:%d/%s: connection failed", cfg.User, cfg.Host, cfg.Port, cfg.Name)
	}

	sqlDB.SetMaxIdleConns(poolCfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(poolCfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(poolCfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(poolCfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	logger.Info("database connected", "host", cfg.Host, "name", cfg.Name)

	return db, nil
}
