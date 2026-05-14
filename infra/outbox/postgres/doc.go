// Package postgres is the pgx-backed [outbox.Store] (and the narrower
// [outbox.Inserter] / [outbox.RelayStore] subsets).
//
// Inserter participates in the caller's business transaction via a
// ctx-stashed [pgx.Tx]. Callers wrap their transactional code with
// [WithTx] and either pass [RequireTx] as the outbox.Writer's
// txCheck (recommended — fails fast outside a tx) or use
// [outbox.NewWriterWithoutTransactionCheck] for explicitly-opted-out
// flows. Relay paths (Claimer, Outcomer, Janitor, Observer) manage
// their own transactions against the pool and do not consult ctx for
// a tx handle.
//
// Schema lives in [Migrations]; apply with the kit migrate helper or
// embed directly. Integration tests live in the sibling
// integrationtest module so production callers do not transitively
// pull testcontainers.
package postgres
