package mcp

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/data/actionlog"
)

// defaultTenantExtractor reads the tenant id from context using the
// kit's canonical [tenant] package. Returning ok=false leaves the
// action-log Tenant field empty and the entry is skipped (the
// signed-store contract rejects empty tenant ids).
func defaultTenantExtractor(ctx context.Context) (string, bool) {
	id, ok := tenant.FromContext(ctx)
	if !ok {
		return "", false
	}
	return id.String(), true
}

// auditPrecheck runs the tenant-resolution check BEFORE the tool
// dispatches. The audit invariant is: if an action logger is
// configured, every EXECUTED tool call produces a signed entry.
//
// Strict mode (default, see [WithStrictAudit]) enforces the
// invariant by refusing to execute the tool when the tenant cannot
// be resolved — the JSON-RPC caller sees -32603 internal error and
// no tool side effects occur. The signed-store contract rejects
// empty tenant ids; this check pre-empts that rejection at the
// transport layer so the failure surface stays at the boundary
// rather than mid-tool.
//
// Loose mode ([WithStrictAudit(false)]) preserves the legacy
// behaviour: log a warn-level message, skip the audit entry, run
// the tool anyway. The caller has explicitly accepted the audit gap.
//
// Returns ok=false when the tool MUST NOT execute (strict + no
// tenant). The caller emits the JSON-RPC error in that case.
//
// When no action logger is configured this is a no-op (returns
// ok=true) — auditing is opt-in so the strict/loose distinction is
// meaningless.
func (s *Server) auditPrecheck(ctx context.Context, tool string) (ok bool) {
	if s.cfg.actionLogger == nil {
		return true
	}
	tenantID, present := s.cfg.tenantExtractor(ctx)
	if present && tenantID != "" {
		return true
	}
	if s.cfg.strictAudit {
		s.cfg.logger.Error("mcp: refusing tool dispatch; no tenant on context (strict audit mode)",
			"tool", tool,
		)
		return false
	}
	s.cfg.logger.Warn("mcp: skipping action log entry; no tenant on context (loose audit mode)",
		"tool", tool,
	)
	return true
}

// recordActionLog writes one [actionlog.Entry] for a tool call.
//
// Ordering & failure semantics:
//
//   - Sync mode + strict audit (default): caller is expected to
//     invoke recordActionLog BEFORE returning the tool result and
//     to surface a non-nil return as a JSON-RPC error. This
//     preserves the audit invariant that every successfully-returned
//     tool call produced a signed entry.
//   - Sync mode + loose audit: a non-nil return is logged and ignored
//     by the caller — operators have explicitly opted out of the
//     invariant.
//   - Async mode: the append is enqueued onto the bounded worker
//     pool and recordActionLog returns nil immediately. Async mode
//     is best-effort; operators expect that an audit-store outage
//     trades durability for latency rather than failing requests.
//     Queue saturation drops the entry (counter increment) rather
//     than blocking the request hot path or spawning unbounded
//     goroutines.
//
// When no [actionlog.Logger] is configured, this is a no-op.
//
// When no tenant is on context AND we got here, [WithStrictAudit]
// must be false (loose mode); the entry is skipped (rather than
// written with an empty TenantID, which the signed-store contract
// rejects). Strict mode would have refused dispatch in
// [auditPrecheck] before any tool ran.
func (s *Server) recordActionLog(ctx context.Context, r *http.Request, tool string, callErr error) error {
	if s.cfg.actionLogger == nil {
		return nil
	}

	tenantID, ok := s.cfg.tenantExtractor(ctx)
	if !ok || tenantID == "" {
		return nil
	}

	actor := s.cfg.actorExtractor(r)

	outcome := actionlog.OutcomeSuccess
	reason := ""
	if callErr != nil {
		outcome = actionlog.OutcomeFailure
		reason = truncateReason(callErr.Error())
	}

	entry := actionlog.Entry{
		TenantID: tenantID,
		Actor:    actor,
		Action:   "mcp." + tool,
		Outcome:  outcome,
		Reason:   reason,
		Metadata: map[string]any{
			"tool":   tool,
			"method": "mcp",
		},
	}

	if s.cfg.asyncAudit {
		s.enqueueAuditJob(auditJob{entry: entry, tool: tool, tenantID: tenantID})
		return nil
	}

	// Sync path: detach cancellation from the request context so the
	// append survives a client disconnect after the tool returned,
	// but bound the wait so a hung audit store cannot pin the
	// tool-call goroutine indefinitely. The caller still observes
	// the error so strict mode can fail-closed.
	appendCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		s.cfg.strictAuditTimeout,
	)
	defer cancel()
	return s.appendActionLog(appendCtx, entry, tool, tenantID)
}

// enqueueAuditJob hands an audit append to the worker pool. If the
// queue is saturated the entry is dropped and a counter incremented:
// async mode is best-effort by definition, and a hung audit store
// must not be allowed to accumulate goroutines without bound.
//
// Race-safety: senders never close auditQueue. Stop closes the done
// channel and workers drain the remaining queue; senders use a
// select on done so they cannot send after Stop has signalled.
func (s *Server) enqueueAuditJob(job auditJob) {
	select {
	case <-s.auditDone:
		s.auditDropped.Add(1)
		s.cfg.logger.Warn("mcp: async audit dropped; server stopped",
			"tool", job.tool,
			"tenant_id", job.tenantID,
		)
		return
	default:
	}
	select {
	case s.auditQueue <- job:
	case <-s.auditDone:
		s.auditDropped.Add(1)
		s.cfg.logger.Warn("mcp: async audit dropped; server stopped",
			"tool", job.tool,
			"tenant_id", job.tenantID,
		)
	default:
		s.auditDropped.Add(1)
		s.cfg.logger.Warn("mcp: async audit queue full; dropping entry",
			"tool", job.tool,
			"tenant_id", job.tenantID,
		)
	}
}

// appendActionLog performs the actual write. Returns the underlying
// store error so the caller can decide whether to surface it.
func (s *Server) appendActionLog(ctx context.Context, entry actionlog.Entry, tool, tenantID string) error {
	if _, err := s.cfg.actionLogger.Append(ctx, entry); err != nil {
		s.cfg.logger.Error("mcp: action log append failed",
			"tool", tool,
			"tenant_id", tenantID,
			"error", err,
		)
		return err
	}
	return nil
}
