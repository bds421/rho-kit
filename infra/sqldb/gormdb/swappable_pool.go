package gormdb

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"
	"time"
)

// SwappablePool wraps a *sql.DB behind an atomic pointer, allowing the
// underlying connection pool to be swapped at runtime without changing the
// *gorm.DB reference. This enables credential rotation: create a new pool
// with updated credentials, swap it in, close the old pool after a grace
// period.
//
// SwappablePool implements gorm.ConnPool and gorm.ConnPoolBeginner so GORM
// transparently delegates all queries to the current pool. It also implements
// GetDBConn so callers of db.DB() receive the current *sql.DB.
//
// After a swap, new queries immediately use the new pool. In-flight
// transactions on the old pool complete normally. The old pool is closed
// after graceCloseDelay (default: ConnMaxLifetime or 30s).
type SwappablePool struct {
	current         atomic.Pointer[sql.DB]
	logger          *slog.Logger
	graceCloseDelay time.Duration
}

// NewSwappablePool creates a SwappablePool backed by the given *sql.DB.
// graceCloseDelay is how long to wait before closing the old pool after a
// swap. Use ConnMaxLifetime or a similar value. Zero defaults to 30s.
func NewSwappablePool(db *sql.DB, logger *slog.Logger, graceCloseDelay time.Duration) *SwappablePool {
	if graceCloseDelay <= 0 {
		graceCloseDelay = 30 * time.Second
	}
	p := &SwappablePool{
		logger:          logger,
		graceCloseDelay: graceCloseDelay,
	}
	p.current.Store(db)
	return p
}

// Swap replaces the underlying *sql.DB with newDB. All new queries
// immediately use newDB. The old pool is closed asynchronously after
// the grace period to allow in-flight queries to complete.
func (p *SwappablePool) Swap(newDB *sql.DB) {
	old := p.current.Swap(newDB)
	if old == nil {
		return
	}

	go func() {
		time.Sleep(p.graceCloseDelay)
		if err := old.Close(); err != nil {
			p.logger.Warn("failed to close old database pool", "error", err)
		} else {
			p.logger.Info("old database pool closed after credential rotation")
		}
	}()
}

// Current returns the active *sql.DB.
func (p *SwappablePool) Current() *sql.DB {
	return p.current.Load()
}

// --- gorm.ConnPool implementation ---

func (p *SwappablePool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.current.Load().PrepareContext(ctx, query)
}

func (p *SwappablePool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.current.Load().ExecContext(ctx, query, args...)
}

func (p *SwappablePool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.current.Load().QueryContext(ctx, query, args...)
}

func (p *SwappablePool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.current.Load().QueryRowContext(ctx, query, args...)
}

// --- gorm.TxBeginner implementation ---

func (p *SwappablePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.current.Load().BeginTx(ctx, opts)
}

// --- gorm.GetDBConnector implementation (for db.DB()) ---

func (p *SwappablePool) GetDBConn() (*sql.DB, error) {
	return p.current.Load(), nil
}

