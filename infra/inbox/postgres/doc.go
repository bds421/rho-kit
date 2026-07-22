// Package postgres provides a transactional Postgres inbox for at-least-once
// message consumers.
//
// Process claims a (consumer, message ID) pair, executes a callback, and
// commits the claim with the callback's local effects. When the context already
// carries an outbox/postgres transaction, Process joins it; otherwise it owns a
// new transaction. The callback receives a context carrying the same pgx.Tx,
// so an outbox Writer configured with outbox/postgres.RequireTx can enqueue
// downstream work atomically.
//
// A duplicate is a successful Result with Duplicate set and never executes the
// callback. A callback error rolls back the new claim, permitting broker
// redelivery. The inbox makes no exactly-once claim for calls to external
// systems; those must use an idempotency key or saga of their own.
//
// The inbox table migration is shipped in infra/outbox/postgres because a
// service should publish its inbox and outbox schema as one transactional
// messaging bundle via `kit-migrate publish outbox`.
package postgres
