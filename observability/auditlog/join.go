package auditlog

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrChainHeld refuses an event that may not join (any but success) from a
// context whose joined transaction already holds the chain lock: appended
// on its own it would wait for that lock until its own transaction ends.
// Log such an event before the success entry, or outside the change.
var ErrChainHeld = errors.New("auditlog: the joined transaction holds the chain lock; log a refusal or failure before the success entry or outside the change")

type joinKey struct{}

type joined struct {
	tx   any
	held atomic.Bool
}

// Join returns ctx carrying tx, a transaction of the Store's own kind, so
// [Logger.LogE] appends a success event inside it: the entry commits or
// rolls back with the change it records. The returned ctx belongs to that
// transaction. A Store that supports this documents its transaction type
// and its own helper (for example auditlog/postgres.WithTx); other Stores
// ignore it.
func Join(ctx context.Context, tx any) context.Context {
	return context.WithValue(ctx, joinKey{}, &joined{tx: tx})
}

// Joined returns the transaction [Join] put into ctx, for a Store.
func Joined(ctx context.Context) (any, bool) {
	j := joinOf(ctx)
	if j == nil {
		return nil, false
	}
	return j.tx, true
}

// MarkChainHeld records that the joined transaction in ctx took the chain
// lock, which it holds until it ends; a Store calls it once it has.
func MarkChainHeld(ctx context.Context) {
	if j := joinOf(ctx); j != nil {
		j.held.Store(true)
	}
}

func joinOf(ctx context.Context) *joined {
	j, _ := ctx.Value(joinKey{}).(*joined)
	return j
}

// detached is ctx without a joined transaction for an event that is not a
// success, so a rolled-back change keeps its refusal or failure on record;
// once the joined transaction holds the chain lock it is ErrChainHeld.
func detached(ctx context.Context, event Event) (context.Context, error) {
	j := joinOf(ctx)
	if event.Status == StatusSuccess || j == nil {
		return ctx, nil
	}
	if j.held.Load() {
		return ctx, ErrChainHeld
	}
	return context.WithValue(ctx, joinKey{}, (*joined)(nil)), nil
}
