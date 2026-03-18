// Package auditlog provides an append-only event ledger for compliance and
// debugging. It records who did what, when, and what the outcome was.
//
// The package follows the kit's pluggable store pattern: a [Store] interface
// defines the persistence contract, with [NewMemoryStore] for testing and
// [gormstore.New] for production. The [Logger] wraps a Store with convenience
// methods and automatic field population (ID, timestamp, trace ID).
//
// # HTTP Middleware
//
// Use [Middleware] to automatically audit all HTTP requests:
//
//	mux := http.NewServeMux()
//	auditMW := auditlog.Middleware(logger,
//	    auditlog.WithActorExtractor(extractUserID),
//	)
//	handler := auditMW(mux)
//
// # Programmatic API
//
// Use [Logger.Log] for domain-specific events:
//
//	infra.AuditLog.Log(ctx, auditlog.Event{
//	    Actor:    userID,
//	    Action:   "approve_order",
//	    Resource: "orders/" + orderID,
//	    Status:   "success",
//	})
//
// # Retention
//
// Use [RetentionJob] with the cron scheduler to clean up old events:
//
//	infra.Cron.Add("audit-retention", "@daily",
//	    auditlog.RetentionJob(store, 365*24*time.Hour, logger))
package auditlog
