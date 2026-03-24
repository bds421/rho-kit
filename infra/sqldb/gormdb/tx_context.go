package gormdb

import (
	"context"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/contextutil"
)

// gormTxWrapper gives the context key a unique identity.
// Without this, contextutil.Key[*gorm.DB] would collide with any other
// package storing *gorm.DB in context.
type gormTxWrapper struct{ db *gorm.DB }

// txKey is the context key for ambient GORM transactions.
var txKey contextutil.Key[gormTxWrapper]

// ContextWithTx returns a new context carrying the given transaction.
func ContextWithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return txKey.Set(ctx, gormTxWrapper{db: tx})
}

// TxFromContext extracts the transaction from context, if present.
func TxFromContext(ctx context.Context) (*gorm.DB, bool) {
	v, ok := txKey.Get(ctx)
	if !ok {
		return nil, false
	}
	return v.db, true
}

// DBFromContext returns the transaction from context if present, otherwise the
// fallback DB. Use this in repository methods to transparently participate in
// ambient transactions.
func DBFromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return fallback
}
