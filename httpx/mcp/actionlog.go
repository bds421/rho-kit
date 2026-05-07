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
// The log is best-effort once we get past the strict-mode tenant
// gate (see [auditPrecheck]): if the configured logger errors
// (store down, secret rotation lag), we log the error and continue.
// The tool call has already happened — refusing to return its
// result to the agent because the audit append failed would be the
// wrong trade-off, and it's the same posture
// httpx/middleware/auditlog uses today.
//
// When no [actionlog.Logger] is configured, this is a no-op.
//
// When no tenant is on context AND we got here, [WithStrictAudit]
// must be false (loose mode); the entry is skipped (rather than
// written with an empty TenantID, which the signed-store contract
// rejects). Strict mode would have refused dispatch in
// [auditPrecheck] before any tool ran.
//
// When [WithAsyncAudit] is true, the append spawns a goroutine
// using context.WithoutCancel(ctx) and returns immediately. The
// caller must NOT rely on the entry being durable before the next
// statement runs — see [WithAsyncAudit] for the trade-off.
func (s *Server) recordActionLog(ctx context.Context, r *http.Request, tool string, callErr error) {
	if s.cfg.actionLogger == nil {
		return
	}

	tenantID, ok := s.cfg.tenantExtractor(ctx)
	if !ok || tenantID == "" {
		// Loose mode: strict mode would have refused dispatch
		// before the tool ran. We already emitted a warn at
		// auditPrecheck, so the skip is silent here.
		return
	}

	actor := s.cfg.actorExtractor(r)
	if actor == "" {
		actor = AnonymousActor
	}

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

	// We deliberately use a fresh background-derived context so the
	// audit append survives a request-context cancel that fired
	// after the tool returned. ctx.Value lookups won't find tenant
	// here, but we already resolved it above and persist as a field.
	appendCtx := context.WithoutCancel(ctx)

	if s.cfg.asyncAudit {
		go s.appendActionLog(appendCtx, entry, tool, tenantID)
		return
	}
	s.appendActionLog(appendCtx, entry, tool, tenantID)
}

// appendActionLog performs the actual write. Extracted so the sync
// and async paths share error-logging behaviour.
func (s *Server) appendActionLog(ctx context.Context, entry actionlog.Entry, tool, tenantID string) {
	if _, err := s.cfg.actionLogger.Append(ctx, entry); err != nil {
		s.cfg.logger.Error("mcp: action log append failed",
			"tool", tool,
			"tenant_id", tenantID,
			"error", err,
		)
	}
}
