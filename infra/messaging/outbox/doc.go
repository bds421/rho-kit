// Package outbox implements the transactional outbox pattern for reliable
// message publishing. It solves the dual-write problem by writing messages
// to a database table within the same transaction as domain state changes,
// then asynchronously relaying them to the message broker.
//
// # Architecture
//
// The package has two main components:
//
//   - [Writer] writes outbox entries within a caller-provided GORM transaction.
//   - [Relay] polls the outbox table and publishes pending entries to the broker.
//     It implements [lifecycle.Component] for integration with the service runner.
//
// # Concurrency
//
// Multiple relay instances can run safely against the same table.
// The GORM store uses SELECT ... FOR UPDATE SKIP LOCKED (PostgreSQL) to
// prevent duplicate delivery across instances.
//
// # Dead-letter
//
// After maxAttempts (default 10), an entry's status is set to "failed".
// Failed entries remain in the table for manual inspection or retry.
// Published entries are retained for the configured retention period
// (default 7 days) and then cleaned up by the relay.
//
// # Usage
//
//	store := outbox.NewGormStore(db)
//	writer := outbox.NewWriter(store)
//
//	// Inside a transaction:
//	gormdb.WithTx(ctx, db, func(tx *gorm.DB) error {
//	    // ... domain writes ...
//	    return writer.Write(ctx, tx, "exchange", "routing.key", msg)
//	})
//
//	// Relay as a lifecycle component:
//	relay := outbox.NewRelay(store, publisher, logger)
//	runner.Add(relay)
package outbox
