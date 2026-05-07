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

// recordActionLog writes one [actionlog.Entry] for a tool call.
//
// The log is best-effort: if the configured logger errors (store
// down, secret rotation lag), we log the error and continue. The
// tool call has already happened — refusing to return its result to
// the agent because the audit append failed would be the wrong
// trade-off, and it's the same posture httpx/middleware/auditlog
// uses today.
//
// When no [actionlog.Logger] is configured, this is a no-op.
//
// When no tenant is on context, the entry is skipped (rather than
// written with an empty TenantID, which the signed-store contract
// rejects). The Server logs a warn-level message so operators can
// notice unscoped tool calls in production.
func (s *Server) recordActionLog(ctx context.Context, r *http.Request, tool string, callErr error) {
	if s.cfg.actionLogger == nil {
		return
	}

	tenantID, ok := s.cfg.tenantExtractor(ctx)
	if !ok || tenantID == "" {
		s.cfg.logger.Warn("mcp: skipping action log entry; no tenant on context",
			"tool", tool,
		)
		return
	}

	actor := s.cfg.actorExtractor(r)
	if actor == "" {
		actor = "anonymous"
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
	_, err := s.cfg.actionLogger.Append(context.WithoutCancel(ctx), entry)
	if err != nil {
		s.cfg.logger.Error("mcp: action log append failed",
			"tool", tool,
			"tenant_id", tenantID,
			"error", err,
		)
	}
}
