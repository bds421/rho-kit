package mcp

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/actionlog"
)

var errAuditActorMissing = errors.New("mcp: action log actor not resolved")

// audit identity resolved once in auditPrecheck and reused by
// recordActionLog so a mid-call extractor failure cannot void the
// strict-audit invariant after the tool has already executed.
type auditIdentityKey struct{}

type auditIdentity struct {
	tenantID string
	actor    string
	actorOK  bool
	// tenantMissing is set in loose mode when the tool runs without a
	// tenant; recordActionLog then skips the entry without re-extracting.
	tenantMissing bool
}

func withAuditIdentity(ctx context.Context, id auditIdentity) context.Context {
	return context.WithValue(ctx, auditIdentityKey{}, id)
}

func auditIdentityFrom(ctx context.Context) (auditIdentity, bool) {
	id, ok := ctx.Value(auditIdentityKey{}).(auditIdentity)
	return id, ok
}

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
// Strict mode (default; opt out via [WithBestEffortAuditOnMissingTenant]) enforces the
// invariant by refusing to execute the tool when the tenant cannot
// be resolved — the caller sees a [sdkmcp.CallToolResult] with
// `isError: true` and "internal error" content, and no tool side
// effects occur. The signed-store contract rejects empty tenant ids;
// this check pre-empts that rejection at the transport layer so the
// failure surface stays at the boundary rather than mid-tool.
//
// Loose mode ([WithBestEffortAuditOnMissingTenant]) preserves the legacy
// behaviour: log a warn-level message, skip the audit entry, run
// the tool anyway. The caller has explicitly accepted the audit gap.
//
// Returns ok=false when the tool MUST NOT execute (strict + no
// tenant). The caller emits the CallToolResult error in that case.
//
// When no action logger is configured this is a no-op (returns
// ok=true) — auditing is opt-in so the strict/loose distinction is
// meaningless.
// auditPrecheck returns a context carrying the resolved audit identity
// (when an action logger is configured) and ok=false when the tool MUST
// NOT execute (strict mode + missing tenant/actor).
func (s *Server) auditPrecheck(ctx context.Context, r *http.Request, tool string) (context.Context, bool) {
	if s.cfg.actionLogger == nil {
		return ctx, true
	}
	tenantID, present := s.extractTenant(ctx)
	if !present || tenantID == "" {
		if s.cfg.strictAudit {
			s.cfg.logger.Error("mcp: refusing tool dispatch; no tenant on context (strict audit mode)",
				redact.String("tool", tool),
			)
			return ctx, false
		}
		s.cfg.logger.Warn("mcp: skipping action log entry; no tenant on context (loose audit mode)",
			redact.String("tool", tool),
		)
		return withAuditIdentity(ctx, auditIdentity{tenantMissing: true}), true
	}

	actor, actorOK := s.extractActor(r)
	if !actorOK {
		if s.cfg.strictAudit {
			s.cfg.logger.Error("mcp: refusing tool dispatch; no actor resolved (strict audit mode)",
				redact.String("tool", tool),
				redact.String("tenant_id", tenantID),
			)
			return ctx, false
		}
		s.cfg.logger.Warn("mcp: action log actor unresolved; recording anonymous actor (loose audit mode)",
			redact.String("tool", tool),
			redact.String("tenant_id", tenantID),
		)
		actor = AnonymousActor
		actorOK = true
	}
	return withAuditIdentity(ctx, auditIdentity{
		tenantID: tenantID,
		actor:    actor,
		actorOK:  actorOK,
	}), true
}

// recordActionLog writes one [actionlog.Entry] for a tool call.
//
// Ordering & failure semantics:
//
//   - Sync mode + strict audit (default): caller is expected to
//     invoke recordActionLog BEFORE returning the tool result and
//     to surface a non-nil return as a CallToolResult error. This
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
// When no tenant is on context AND we got here, [WithBestEffortAuditOnMissingTenant]
// must be false (loose mode); the entry is skipped (rather than
// written with an empty TenantID, which the signed-store contract
// rejects). Strict mode would have refused dispatch in
// [auditPrecheck] before any tool ran.
func (s *Server) recordActionLog(ctx context.Context, r *http.Request, tool string, callErr error) error {
	if s.cfg.actionLogger == nil {
		return nil
	}

	// Prefer the identity resolved in auditPrecheck so a second extraction
	// cannot silently drop the audit entry after the tool has executed.
	var tenantID, actor string
	if id, ok := auditIdentityFrom(ctx); ok {
		if id.tenantMissing {
			return nil
		}
		tenantID = id.tenantID
		actor = id.actor
		if !id.actorOK || actor == "" {
			if s.cfg.strictAudit {
				return errAuditActorMissing
			}
			actor = AnonymousActor
		}
	} else {
		// Fallback for callers that invoke recordActionLog without precheck
		// (should not happen on the hot path).
		var ok bool
		tenantID, ok = s.extractTenant(ctx)
		if !ok || tenantID == "" {
			if s.cfg.strictAudit {
				return errors.New("mcp: action log tenant not resolved")
			}
			return nil
		}
		var actorOK bool
		actor, actorOK = s.extractActor(r)
		if !actorOK {
			if s.cfg.strictAudit {
				return errAuditActorMissing
			}
			actor = AnonymousActor
		}
	}

	outcome := actionlog.OutcomeSuccess
	reason := ""
	if callErr != nil {
		outcome = actionlog.OutcomeFailure
		reason = sanitiseReason(callErr.Error())
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
		s.enqueueAuditJob(auditJob{ctx: ctx, entry: entry, tool: tool, tenantID: tenantID})
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

func (s *Server) extractTenant(ctx context.Context) (tenantID string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			s.cfg.logger.Error("mcp: tenant extractor panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			tenantID, ok = "", false
		}
	}()
	return s.cfg.tenantExtractor(ctx)
}

func (s *Server) extractActor(r *http.Request) (actor string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			s.cfg.logger.Error("mcp: actor extractor panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			actor, ok = AnonymousActor, false
		}
	}()
	actor = s.cfg.actorExtractor(r)
	if !validActionLogTextField(actor, actionlog.MaxActorLen, true) {
		if actor != "" {
			s.cfg.logger.Warn("mcp: actor extractor returned invalid actor id; using anonymous actor",
				"actor_len", len(actor),
			)
		}
		return AnonymousActor, false
	}
	if actor == AnonymousActor && !s.cfg.allowAnonymousActor {
		s.cfg.logger.Warn("mcp: anonymous actor requires explicit opt-in")
		return AnonymousActor, false
	}
	return actor, true
}

// enqueueAuditJob hands an audit append to the worker pool. If the
// queue is saturated the entry is dropped and a counter incremented:
// async mode is best-effort by definition, and a hung audit store
// must not be allowed to accumulate goroutines without bound.
//
// Race-safety: enqueueAuditJob takes auditStopMu.RLock for the whole
// check-then-send window. [Server.Stop] takes the write lock to flip
// auditStopped and close auditDone, so a sender that observes
// auditStopped == false is guaranteed to send to a still-open queue
// before Stop's close runs. The earlier two-step
// "select-default-then-select" pattern allowed the Go scheduler to
// pick the send case after Stop had already signalled, leaking a job
// past the worker drain.
func (s *Server) enqueueAuditJob(job auditJob) {
	s.auditStopMu.RLock()
	defer s.auditStopMu.RUnlock()
	if s.auditStopped.Load() {
		s.auditDropped.Add(1)
		s.cfg.logger.Warn("mcp: async audit dropped; server stopped",
			redact.String("tool", job.tool),
			redact.String("tenant_id", job.tenantID),
		)
		return
	}
	select {
	case s.auditQueue <- job:
	default:
		s.auditDropped.Add(1)
		s.cfg.logger.Warn("mcp: async audit queue full; dropping entry",
			redact.String("tool", job.tool),
			redact.String("tenant_id", job.tenantID),
		)
	}
}

// appendActionLog performs the actual write. Returns the underlying
// store error so the caller can decide whether to surface it.
func (s *Server) appendActionLog(ctx context.Context, entry actionlog.Entry, tool, tenantID string) error {
	if _, err := s.cfg.actionLogger.Append(ctx, entry); err != nil {
		s.cfg.logger.Error("mcp: action log append failed",
			redact.String("tool", tool),
			redact.String("tenant_id", tenantID),
			redact.Error(err),
		)
		return err
	}
	return nil
}
