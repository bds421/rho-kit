// Package app wires the agentic-service example together. The
// composition shown here is the canonical v2.0.0 stack:
//
//	signedrequest (outermost when configured) → tenant → budget → handlers
//
// All optional middleware can be omitted independently. The Builder
// composes them in the right order regardless of registration sequence.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/data/actionlog"
	actionlogmem "github.com/bds421/rho-kit/data/actionlog/memory"
	"github.com/bds421/rho-kit/data/approval"
	approvalmem "github.com/bds421/rho-kit/data/approval/memory"
	"github.com/bds421/rho-kit/data/budget"
	budgetmem "github.com/bds421/rho-kit/data/budget/memory"
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
	// SECURITY: this HMAC secret is a DEMO PLACEHOLDER. It satisfies the
	// >= 32-byte length check in NewStaticSecrets but has zero entropy
	// and is published in this repo. Production deployments MUST load
	// the secret from a KMS, env var, or secret manager (e.g. via
	// security/keysprovider) — never hard-code. Copying this file
	// without rotating the secret is a critical misconfiguration.
	alogger := actionlog.New(alogStore, actionlog.NewStaticSecrets("v1", map[string][]byte{
		"v1": []byte("at-least-32-bytes-of-secret-bytes!"),
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

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
// request rather than executing immediately. Production code wraps
// this in the httpx/middleware/approval middleware which does the
// same thing automatically.
func dangerousAction(s approval.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-Id")
		if tenantID == "" {
			http.Error(w, "missing X-Tenant-Id", http.StatusBadRequest)
			return
		}
		req, err := s.Create(r.Context(), approval.Request{
			TenantID:  tenantID,
			Actor:     r.Header.Get("X-Actor"),
			Action:    "admin.dangerous-action",
			Resource:  "example",
			State:     approval.StatePending,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(24 * time.Hour),
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

// budgetStatus exposes the remaining budget for the tenant. Real
// services emit X-Budget-Remaining via the budget middleware on
// every response; this endpoint demonstrates the Peek API directly.
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
