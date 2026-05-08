// Package app wires the agentic-service EXAMPLE.
//
// SECURITY: this example is illustrative only. It mounts handlers
// without authentication, rate limiting, or CSRF protection so the
// demo curl invocations work out of the box. It must NEVER be used as
// a starting point for production wiring — copy the per-primitive
// recipes from the doc instead.
//
// Production services MUST use app.Builder.WithJWT (paired with
// WithJWTIssuer + WithJWTAudience) / .WithSignedRequests /
// .WithMultiTenant / .WithTenantBudget / .WithActionLogger /
// .WithApprovalStore — the Builder composes the middleware chain
// correctly and runs the always-on production-safety validator at
// startup. The validator unconditionally rejects empty TLS, missing
// JWT issuer/audience, exposed internal-host, weak postgres sslmode,
// and excessive tracing sample rates. Each tightening has a documented
// .Without* opt-out (.WithoutTLS, .WithoutJWTIssuer, etc.) for the
// rare cases where the operator has compensating controls in place;
// production deployments must NOT use those opt-outs casually.
//
// The composition shown here mirrors the canonical v2.0.0 ordering:
//
//	(in production) signedrequest → tenant → budget → handler
//
// In this example only `tenant` is wired (in front of MCP) so the
// strict-audit gate has a tenant on context. The budget and approval
// stores are exercised via the /admin/* handlers' direct API access
// rather than middleware — both forms are documented to consumers.
package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/data/actionlog"
	actionlogmem "github.com/bds421/rho-kit/data/actionlog/memory"
	"github.com/bds421/rho-kit/data/approval"
	approvalmem "github.com/bds421/rho-kit/data/approval/memory"
	"github.com/bds421/rho-kit/data/budget"
	budgetmem "github.com/bds421/rho-kit/data/budget/memory"
	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/mcp"
	"github.com/bds421/rho-kit/httpx/middleware/tenant"
)

// Run starts the HTTP server with the agentic-service stack.
//
// In a real service this would call app.Builder.WithMultiTenant /
// .WithTenantBudget / .WithActionLogger / .WithApprovalStore and let
// the Builder install the middleware on the public mux. The example
// uses a hand-composed mux to keep it dependency-light (no DB, no
// Redis) while still exercising every primitive.
func Run(ctx context.Context) error {
	// In-memory backends keep the example self-contained. Production
	// wiring swaps these for the postgres / redis backends.
	bud := budgetmem.New(1000 /* cap per period */, time.Minute)

	alogStore := actionlogmem.New()
	// Generate an ephemeral 32-byte secret per process start. This means
	// every restart invalidates the chain, which is fine for the demo
	// (no persistence) and prevents a copy-pasted hard-coded secret
	// from leaking into production via this file.
	demoSecret := make([]byte, 32)
	if _, randErr := rand.Read(demoSecret); randErr != nil {
		return fmt.Errorf("agentic-service: generate demo HMAC secret: %w", randErr)
	}
	slog.Default().Warn("agentic-service: using ephemeral per-process HMAC secret — chain resets on every restart; production must wire a real keysprovider")
	alogger := actionlog.New(alogStore, actionlog.NewStaticSecrets("v1", map[string][]byte{
		"v1": demoSecret,
	}))

	astore := approvalmem.New()

	mcpServer := newMCPServer(alogger)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Tenant middleware sits outside MCP so the X-Tenant-Id header
	// lifts to context BEFORE the MCP server's strict-audit gate
	// inspects it. With strict audit on (the v2.0.0 default), MCP
	// refuses to dispatch a tool when no tenant resolves and an
	// action logger is configured. See httpx/mcp doc for details.
	mux.Handle("/mcp", mcpHTTPHandler(mcpServer))
	mux.HandleFunc("/admin/dangerous-action", dangerousAction(astore))
	mux.HandleFunc("/admin/budget", budgetStatus(bud))

	// httpx.NewServer wires the kit's slowloris defaults
	// (ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout,
	// MaxHeaderBytes) and a slog-backed ErrorLog. The Builder uses
	// the same helper internally; the example calls it directly so it
	// stays dependency-light without forking those defaults by hand.
	// kit-doctor:allow http-server-error-log reason="default slog ErrorLog is sufficient for the example"
	srv := httpx.NewServer(":8080", mux)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// EchoIn is the input for the sample MCP tool.
type EchoIn struct {
	Message string `json:"message" validate:"required" desc:"Text to echo back."`
}

// EchoOut is the response for the sample MCP tool.
type EchoOut struct {
	Echoed string `json:"echoed"`
}

// echo is the canonical MCP handler shape.
func echo(_ context.Context, in EchoIn) (EchoOut, error) {
	return EchoOut{Echoed: in.Message}, nil
}

// newMCPServer registers the sample tools with the MCP server.
//
// In production, also chain auth + ratelimit middleware in front via
// the Builder. Use a real action-log Logger (postgres backend) so
// calls land in a query-able audit trail.
func newMCPServer(alog actionlog.Logger) *mcp.Server {
	srv := mcp.NewServer(mcp.WithActionLogger(alog))
	if err := mcp.Register[EchoIn, EchoOut](srv, "echo", echo,
		mcp.WithToolDescription("Echo the input message back to the caller."),
	); err != nil {
		panic(fmt.Errorf("mcp register echo: %w", err))
	}
	return srv
}

// mcpHTTPHandler returns the MCP server's HTTP handler wrapped in the
// kit's tenant middleware. The middleware lifts X-Tenant-Id from the
// request header onto context so MCP's default tenant extractor (which
// reads from core/tenant context) finds it. WithRequired(false) means
// requests without the header still pass through; the strict-audit gate
// inside MCP rejects them at the audit-precheck step rather than the
// transport edge — that gives the audit a single chokepoint and a
// uniform error code (-32603) regardless of which transport carried
// the call.
func mcpHTTPHandler(srv *mcp.Server) http.Handler {
	return tenant.New(tenant.WithRequired(false))(srv.HTTP())
}

// dangerousAction is a contrived endpoint that creates an approval
// request rather than executing immediately.
//
// SECURITY: in production, wrap this in:
//   - auth middleware (RequireUserWithJWT or RequireS2SAuth) so the
//     caller is authenticated; the actor field is derived from the
//     verified JWT subject (auth.UserID), NOT from a client header.
//   - the httpx/middleware/approval middleware which creates the
//     approval entry from a verified actor automatically.
//
// This handler reads X-Tenant-Id from the header, which is acceptable
// only because the demo has no auth — production must take the tenant
// from the verified JWT or signed-request claim. The placeholder
// "demo-actor" actor below is a deliberate non-spoofable string so
// audit forensics on this example don't accept attacker-controlled
// values.
func dangerousAction(s approval.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-Id")
		if tenantID == "" {
			http.Error(w, "missing X-Tenant-Id", http.StatusBadRequest)
			return
		}
		now := time.Now()
		req, err := s.Create(r.Context(), approval.Request{
			ID:        uuid.Must(uuid.NewV7()).String(),
			TenantID:  tenantID,
			Actor:     "demo-actor",
			Action:    "admin.dangerous-action",
			Resource:  "example",
			State:     approval.StatePending,
			CreatedAt: now,
			ExpiresAt: now.Add(24 * time.Hour),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"approval_id": req.ID,
			"status":      string(req.State),
		})
	}
}

// budgetStatus exposes the remaining budget for the tenant.
//
// SECURITY: in production, the tenant ID must come from the verified
// JWT claim, not a client-supplied header. Wrap in auth + tenant
// middleware. Real services also emit X-Budget-Remaining via the
// budget middleware on every response so callers see headroom inline
// — this endpoint demonstrates the Peek API for completeness.
func budgetStatus(b budget.Budget) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-Id")
		if tenantID == "" {
			http.Error(w, "missing X-Tenant-Id", http.StatusBadRequest)
			return
		}
		remaining, err := b.Peek(r.Context(), tenantID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant":    tenantID,
			"remaining": remaining,
		})
	}
}
