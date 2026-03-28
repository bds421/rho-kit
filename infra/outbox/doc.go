// Package outbox implements the transactional outbox pattern for reliable
// event publishing. It solves the dual-write problem by writing entries
// to a database table within the same transaction as domain state changes,
// then asynchronously relaying them to an external system via a pluggable
// [Publisher] interface.
//
// The package is transport-agnostic and storage-agnostic: it does not depend
// on any specific broker or database driver. Adapters for AMQP, Redis Streams,
// Kafka, or any other transport implement the [Publisher] interface. Storage
// backends (GORM, pgx, etc.) implement the [Store] interface.
//
// # Architecture
//
// The package has two main components:
//
//   - [Writer] writes outbox entries via a [Store] implementation.
//   - [Relay] polls the outbox store and publishes pending entries via a [Publisher].
//     It implements [lifecycle.Component] for integration with the service runner.
//
// # Concurrency
//
// Multiple relay instances can run safely against the same store.
// Storage implementations should use appropriate locking (e.g. SELECT FOR
// UPDATE SKIP LOCKED for PostgreSQL) with an atomic claim pattern: entries
// are set to "processing" status within the same transaction as the lock,
// preventing other relays from claiming them. If a relay crashes, stale
// "processing" entries are automatically recovered back to "pending" after
// a configurable timeout (default 5 minutes).
//
// # Dead-letter
//
// After maxAttempts (default 10), an entry's status is set to "failed".
// Failed entries remain in the store for manual inspection or retry.
// Published entries are retained for the configured retention period
// (default 7 days) and then cleaned up by the relay.
//
// # Usage
//
//	store := gormstore.New(db)   // from infra/outbox/gormstore
//	writer := outbox.NewWriter(store)
//
//	// Inside a transaction (using gormdb.ContextWithTx for atomicity):
//	err := db.Transaction(func(tx *gorm.DB) error {
//	    ctx := gormdb.ContextWithTx(ctx, tx)
//	    // ... domain writes using tx ...
//	    return writer.Write(ctx, outbox.WriteParams{
//	        Topic:       "orders",
//	        RoutingKey:  "order.created",
//	        MessageID:   msg.ID,
//	        MessageType: "order.created",
//	        Payload:     payload,
//	    })
//	})
//
//	// Relay as a lifecycle component:
//	relay := outbox.NewRelay(store, publisher, logger)
//	runner.Add(relay)
package outbox
