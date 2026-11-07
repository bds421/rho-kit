package auditlog

import "context"

type joinKey struct{}

// Join returns ctx carrying tx, a transaction of the Store's own kind, so
// [Logger.LogE] appends a success event inside it: the entry commits or
// rolls back with the change it records. A Store that supports this
// documents its transaction type and its own helper (for example
// auditlog/postgres.WithTx); other Stores ignore it.
func Join(ctx context.Context, tx any) context.Context {
	return context.WithValue(ctx, joinKey{}, tx)
}

// Joined returns the transaction [Join] put into ctx, for a Store.
func Joined(ctx context.Context) (any, bool) {
	tx := ctx.Value(joinKey{})
	return tx, tx != nil
}

// detached is ctx without a joined transaction: an event that is not a
// success never joins, so a rolled-back change keeps its refusal or
// failure on record.
func detached(ctx context.Context, event Event) context.Context {
	if event.Status == StatusSuccess {
		return ctx
	}
	if _, ok := Joined(ctx); !ok {
		return ctx
	}
	return context.WithValue(ctx, joinKey{}, nil)
}
