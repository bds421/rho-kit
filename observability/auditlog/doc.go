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
// # Tamper-evidence
//
// Every appended event carries an HMAC over a canonical encoding of its
// fields plus the previous event's HMAC, forming an append-only chain
// keyed by [WithChainKey]. [VerifyChain] (and the streaming
// [Logger.VerifyChain]) returns wrapped [ErrChainBroken] if any record
// has been modified, deleted, or inserted. Pagination cursors returned by
// [Logger.List] are HMAC-signed with [WithCursorKey] so attackers cannot
// guess / forge cursors to skip records; forged cursors return wrapped
// [ErrInvalidCursor]. Both keys are required (≥32 bytes); [New] panics
// fast at startup if either is missing.
//
// # Key memory hygiene
//
// Both the chain key and the cursor key are wrapped in
// [secret.String], with reveals bounded to a single HMAC compute via
// [secret.String.Use]. Call [Logger.Close] during graceful shutdown
// to zero both wrappers; subsequent [Logger.LogE] / [Logger.List] /
// [Logger.VerifyChain] calls return [ErrLoggerClosed]. Memory dumps
// taken after Close find zeroes in place of the key bytes.
//
// See docs/audit/THREAT_MODEL.md §5.4 for the canonical claims.
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
