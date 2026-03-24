package gormdb

import (
	"context"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/contextutil"
)

// gormTx is a named type to give the context key a unique identity.
type gormTx = *gorm.DB

// txKey is the context key for ambient GORM transactions.
var txKey contextutil.Key[gormTx]

// ContextWithTx returns a new context carrying the given transaction.
func ContextWithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return txKey.Set(ctx, tx)
}

// TxFromContext extracts the transaction from context, if present.
func TxFromContext(ctx context.Context) (*gorm.DB, bool) {
	return txKey.Get(ctx)
}

// DBFromContext returns the transaction from context if present, otherwise the
// fallback DB. Use this in repository methods to transparently participate in
// ambient transactions.
func DBFromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := txKey.Get(ctx); ok {
		return tx
	}
	return fallback
}
