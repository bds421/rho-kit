// Package stack provides a canonical HTTP middleware chain.
//
// Default applies metrics, request IDs, tracing, and logging in the recommended
// order, with options to disable or extend the chain. Outer middleware wraps
// the full stack, while Inner middleware runs closest to the handler.
//
// # Audit logging
//
// The tamper-evident audit-log middleware is intentionally NOT wired by
// [Default]; the underlying [observability/auditlog.Logger] needs
// service-specific chain / cursor keys and a concrete store. Pass
// [WithAuditLog] to inject it at the canonical position — innermost, so the
// Inner-wedge auth middleware has already populated the actor and the
// recorded response status matches the bytes the handler wrote. See
// docs/audit/THREAT_MODEL.md §4.1.
package stack
