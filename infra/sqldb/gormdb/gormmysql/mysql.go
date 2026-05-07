// Package gormmysql provides a GORM connection factory for MySQL/MariaDB
// using the go-sql-driver/mysql driver. Import this package only when your
// service uses MySQL or MariaDB — it does not pull in PostgreSQL dependencies.
package gormmysql

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// registerTLSConfigDedup registers cfg in the MySQL driver's global TLS map,
// reusing an existing registration when an identical config has been seen
// before. Returns the key under which the config is registered.
//
// Each call increments the per-fingerprint refcount; pair with [ReleaseTLS]
// to deregister when the last connection using the config closes.
func registerTLSConfigDedup(cfg *tls.Config) (string, error) {
	fp := tlsFingerprint(cfg)
	tlsRegistryMu.Lock()
	defer tlsRegistryMu.Unlock()
	if entry, ok := tlsRegistry[fp]; ok {
		entry.refCount++
		return entry.key, nil
	}
	key := fmt.Sprintf("custom-%d", tlsConfigCounter.Add(1))
	if err := mysqldriver.RegisterTLSConfig(key, cfg); err != nil {
		return "", err
	}
	tlsRegistry[fp] = &tlsRegEntry{key: key, refCount: 1}
	return key, nil
}

// ReleaseTLS deregisters cfg from the MySQL driver's TLS registry once the
// last connection using an equivalent config has closed. Each successful
// [New] / [MySQLDriver.Open] call with cfg adds one reference; ReleaseTLS
// removes one. Calling with a cfg that was never registered is a no-op.
//
// Long-running services that open one connection for their lifetime need
// not call ReleaseTLS — process exit reclaims the registry entry. The
// helper exists for tests that recycle connections, multi-tenant services
// that rotate TLS material per tenant, and hot-reload paths.
//
// Equivalence is by content fingerprint (cert chain, root CAs, ServerName,
// InsecureSkipVerify), not pointer identity, so callers can construct a
// fresh *tls.Config equivalent to the one they passed to WithTLS and the
// release will still hit the right entry.
func ReleaseTLS(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	fp := tlsFingerprint(cfg)
	tlsRegistryMu.Lock()
	defer tlsRegistryMu.Unlock()
	entry, ok := tlsRegistry[fp]
	if !ok {
		return
	}
	entry.refCount--
	if entry.refCount > 0 {
		return
	}
	mysqldriver.DeregisterTLSConfig(entry.key)
	delete(tlsRegistry, fp)
}

// tlsFingerprint hashes the security-relevant fields of a tls.Config so
// content-equal configs produce the same fingerprint. Only fields that
// change the connection's trust boundary are included; cosmetic fields
// like SessionTicketsDisabled are intentionally ignored.
func tlsFingerprint(cfg *tls.Config) string {
	h := sha256.New()
	if cfg.RootCAs != nil {
		// Subjects() includes the DER-encoded distinguished names of every
		// root CA — a stable fingerprint of the trust pool.
		for _, s := range cfg.RootCAs.Subjects() { //nolint:staticcheck // simple stable hash, x509.CertPool exposes nothing better
			h.Write(s)
		}
	}
	for _, c := range cfg.Certificates {
		for _, der := range c.Certificate {
			h.Write(der)
		}
	}
	if cfg.ServerName != "" {
		h.Write([]byte(cfg.ServerName))
	}
	if cfg.InsecureSkipVerify {
		h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// tlsConfigCounter generates unique TLS config names to prevent global
// map overwrite when multiple connections use different TLS configs.
var tlsConfigCounter atomic.Uint64

// tlsRegEntry tracks a TLS config registered with the MySQL driver. The
// refCount field is updated under tlsRegistryMu and decremented by
// [ReleaseTLS]; the entry is dropped (and the driver entry deregistered)
// when the count reaches zero.
type tlsRegEntry struct {
	key      string
	refCount int
}

// tlsRegistry deduplicates TLS config registrations by content fingerprint.
// The MySQL driver's RegisterTLSConfig writes to a global map; without dedup,
// every Open with TLS leaks a registry entry on connection close. By hashing
// the cert/RootCA material we reuse the same entry across connections that
// share TLS settings (the common case: every reconnect after a transient
// failure).
//
// Reference counting is necessary because dedup means several callers share
// one driver entry: a naive deregister-on-close would yank the entry while
// other connections still rely on it.
var (
	tlsRegistryMu sync.Mutex
	tlsRegistry   = map[string]*tlsRegEntry{} // fingerprint → entry
)

// Option configures the MySQL connection.
type Option func(*dbOpts)

type dbOpts struct {
	tlsConfig *tls.Config
}

// WithTLS enables mTLS for the MySQL/MariaDB connection.
func WithTLS(cfg *tls.Config) Option {
	return func(o *dbOpts) { o.tlsConfig = cfg }
}

// New opens a GORM database connection to MySQL/MariaDB with connection pooling.
//
// Deprecated: Use [MySQLDriver.Open] with the unified [sqldb.Config] instead.
//
//nolint:staticcheck // Uses deprecated MySQLConfig for backward compat.
func New(cfg sqldb.MySQLConfig, poolCfg sqldb.PoolConfig, logger *slog.Logger, opts ...Option) (*gorm.DB, error) {
	var o dbOpts
	for _, opt := range opts {
		opt(&o)
	}

	tlsEnabled := false
	tlsKey := "custom"
	if o.tlsConfig != nil {
		key, err := registerTLSConfigDedup(o.tlsConfig)
		if err != nil {
			return nil, fmt.Errorf("register mysql TLS config: %w", err)
		}
		tlsKey = key
		tlsEnabled = true
		logger.Info("database TLS enabled")
	}

	committed := false
	defer func() {
		if tlsEnabled && !committed {
			ReleaseTLS(o.tlsConfig)
		}
	}()

	logLevel := gormlogger.Warn
	if cfg.LogLevel == "info" {
		logLevel = gormlogger.Info
	}

	var dsn string
	if tlsEnabled {
		dsn = cfg.DSN(tlsKey)
	} else {
		dsn = cfg.DSN()
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		logger.Warn("database connection failed", "host", cfg.Host, "name", cfg.Name, "error", err)
		return nil, fmt.Errorf("connect to database %s@%s:%d/%s: connection failed", cfg.User, cfg.Host, cfg.Port, cfg.Name)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB instance: %w", err)
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

	committed = true
	return db, nil
}
