// Package auditlog provides an append-only event ledger for compliance and
// debugging. It records who did what, when, and what the outcome was.
//
// The package follows the kit's pluggable store pattern: a [Store] interface
// defines the persistence contract, with [NewMemoryStore] for testing and
// local development. Production services should provide a durable [Store]
// implementation. The [Logger] wraps a Store with convenience methods and
// automatic field population (ID, timestamp, trace ID).
//
// Events are validated before persistence. Actor, action, resource, and status
// are required bounded tokens; status must be "success", "failure", or
// "denied"; metadata must be valid JSON and is capped at 64 KiB. Custom stores
// should call [ValidateEvent] in Append to keep the same contract as the bundled
// memory store.
//
// # HTTP Middleware
//
// Use the httpx/middleware/auditlog package to automatically audit HTTP
// requests:
//
//	mux := http.NewServeMux()
//	auditMW := auditmw.Middleware(logger,
//	    auditmw.WithActorExtractor(extractUserID),
//	)
//	handler := auditMW(mux)
//
// # Programmatic API
//
// Use [Logger.LogE] for domain-specific events whose success depends on audit
// persistence. [Logger.Log] is best-effort and only records append failures via
// logs, counters, and the optional drop callback.
//
//	if err := infra.AuditLog.LogE(ctx, auditlog.Event{
//	    Actor:    userID,
//	    Action:   "approve_order",
//	    Resource: "orders/" + orderID,
//	    Status:   "success",
//	}); err != nil {
//	    return err
//	}
//
// # Retention
//
// Use [RetentionJob] with the cron scheduler to clean up old events:
//
//	infra.Cron.Add("audit-retention", "@daily",
//	    auditlog.RetentionJob(store, 365*24*time.Hour, logger))
package auditlog
