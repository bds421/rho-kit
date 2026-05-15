// Package app wires the agentic-service EXAMPLE.
//
// SECURITY: this example is illustrative only. It requires a strong
// demo bearer token before it starts, but it still omits production
// JWT/signed-request auth, rate limiting, CSRF protection for browser
// flows, persistent stores, and stable secret management. It must
// NEVER be used as a starting point for production wiring — copy the
// per-primitive recipes from the doc instead.
//
// Production services MUST register the security bridges via
// app.Builder.With: jwt.Module (paired with jwt.WithIssuer +
// jwt.WithAudience) / signedrequest.Module / MultiTenant /
// TenantBudget / ActionLogger / ApprovalStore. The Builder composes
// the JWT, tenant, budget, and signed-request middleware chain
// automatically and runs the always-on production-safety validator
// at startup. The validator unconditionally rejects empty TLS,
// missing JWT issuer/audience, exposed internal-host, weak postgres
// sslmode, and excessive tracing sample rates. Each tightening has
// a documented opt-out (jwt.WithoutIssuer, http.WithoutTLS,
// http.WithInternalNonLoopback, etc.) for the rare cases where the
// operator has compensating controls in place; production deployments
// must NOT use those opt-outs casually.
//
// NOTE on approval middleware: ApprovalStore stores the
// [approval.Store] for handler-side consumption only. The Builder
// does NOT install [httpx/middleware/approval] on the public mux —
// handlers must wrap the routes that need approval gating themselves
// (tenant/actor extractors and action/resource derivation are too
// service-specific to wire automatically). See the approval package
// docs and Builder.ApprovalStore docstring for the canonical
// per-route pattern.
//
// The composition shown here mirrors the canonical v2.0.0 ordering:
//
//	(in production) signedrequest → tenant → budget → handler
//
// In this example a local demo bearer token gates MCP/admin routes,
// and `tenant` is wired in front of MCP so the strict-audit gate has
// a tenant on context. The budget and approval stores are exercised
// via the /admin/* handlers' direct API access rather than middleware
// — both forms are documented to consumers.
package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/data/v2/actionlog"
	actionlogmem "github.com/bds421/rho-kit/data/v2/actionlog/memory"
	"github.com/bds421/rho-kit/data/v2/approval"
	approvalmem "github.com/bds421/rho-kit/data/v2/approval/memory"
	"github.com/bds421/rho-kit/data/v2/budget"
	budgetmem "github.com/bds421/rho-kit/data/v2/budget/memory"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/mcp"
	"github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
)

const (
	demoTokenEnv       = "AGENTIC_SERVICE_DEMO_TOKEN"
	minDemoTokenLength = 32
)

// Run starts the HTTP server with the agentic-service stack.
//
// In a real service this would register the security bridges via
// app.Builder.With: tenant.Module, budget.Module, actionlog.Module,
// approval.Module — and let the Builder install the JWT / tenant /
// budget / signed-request middleware on the public mux. Approval
// middleware must still be wrapped by handlers explicitly (see
// app/approval package doc). The example uses a hand-composed mux
// to keep it dependency-light (no DB, no Redis) while still
// exercising every primitive.
func Run(ctx context.Context) error {
	return run(ctx, ":8080")
}

func run(ctx context.Context, addr string) error {
	demoToken, err := demoBearerTokenFromEnv()
	if err != nil {
		return err
	}

	// In-memory backends keep the example self-contained. Production
	// wiring swaps these for the postgres / redis backends.
	bud := budgetmem.Open(1000 /* cap per period */, time.Minute)

	// Generate an ephemeral 32-byte secret per process start. This means
	// every restart invalidates the chain, which is fine for the demo
	// (no persistence) and prevents a copy-pasted hard-coded secret
	// from leaking into production via this file.
	demoSecret := make([]byte, 32)
	if _, randErr := rand.Read(demoSecret); randErr != nil {
		return fmt.Errorf("agentic-service: generate demo HMAC secret: %w", randErr)
	}
	// Generate an ephemeral 32-byte cursor signing key per process start so
	// admin/UI pagination cursors are unforgeable even in the demo. Production
	// must wire a stable rotated key — restarting today invalidates outstanding
	// cursors, which is intentional for a memory-backed demo.
	demoActionlogCursorKey := make([]byte, 32)
	if _, randErr := rand.Read(demoActionlogCursorKey); randErr != nil {
		return fmt.Errorf("agentic-service: generate actionlog cursor key: %w", randErr)
	}
	demoApprovalCursorKey := make([]byte, 32)
	if _, randErr := rand.Read(demoApprovalCursorKey); randErr != nil {
		return fmt.Errorf("agentic-service: generate approval cursor key: %w", randErr)
	}
	actionlogCursorSigner, err := actionlog.NewCursorSigner(demoActionlogCursorKey)
	if err != nil {
		return fmt.Errorf("agentic-service: build actionlog cursor signer: %w", err)
	}
	approvalCursorSigner, err := approval.NewCursorSigner(demoApprovalCursorKey)
	if err != nil {
		return fmt.Errorf("agentic-service: build approval cursor signer: %w", err)
	}
	alogStore := actionlogmem.New(actionlogCursorSigner)
	slog.Default().Warn("agentic-service: using ephemeral per-process HMAC secret — chain resets on every restart; production must wire a real keysprovider")
	alogger := actionlog.New(alogStore, actionlog.NewStaticSecrets("v1", map[string][]byte{
		"v1": demoSecret,
	}))

	astore := approvalmem.New(approvalCursorSigner)

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
	mux.Handle("/mcp", requireDemoBearerToken(demoToken, mcpHTTPHandler(mcpServer)))
	mux.Handle("/admin/dangerous-action", requireDemoBearerToken(demoToken, dangerousAction(astore)))
	mux.Handle("/admin/budget", requireDemoBearerToken(demoToken, budgetStatus(bud)))

	// httpx.NewServer wires the kit's slowloris defaults
	// (ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout,
	// MaxHeaderBytes) and a slog-backed ErrorLog. The Builder uses
	// the same helper internally; the example calls it directly so it
	// stays dependency-light without forking those defaults by hand.
	// kit-doctor:allow http-server-error-log reason="default slog ErrorLog is sufficient for the example"
	srv := httpx.NewServer(addr, mux)
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

func demoBearerTokenFromEnv() ([]byte, error) {
	token := os.Getenv(demoTokenEnv)
	if len(token) < minDemoTokenLength {
		return nil, fmt.Errorf("agentic-service: %s must be set to at least %d bytes", demoTokenEnv, minDemoTokenLength)
	}
	return []byte(token), nil
}

func requireDemoBearerToken(token []byte, next http.Handler) http.Handler {
	if len(token) < minDemoTokenLength {
		panic("agentic-service: demo bearer token must be at least 32 bytes")
	}
	if next == nil {
		panic("agentic-service: requireDemoBearerToken requires a non-nil handler")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader, ok := singletonRequestHeader(r, "Authorization")
		if !ok || !validDemoBearerToken(authHeader, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentic-service-example"`)
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func singletonRequestHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", false
	}
	return value, true
}

func validDemoBearerToken(header string, want []byte) bool {
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(got) != len(want) {
		dummy := make([]byte, len(want))
		_ = subtle.ConstantTimeCompare(dummy, want)
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), want) == 1
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
	srv := mcp.NewServer(
		mcp.WithActionLogger(alog),
		mcp.WithActorExtractor(func(*http.Request) string { return "demo-operator" }),
	)
	if err := mcp.Register[EchoIn, EchoOut](srv, "echo", echo,
		mcp.WithToolDescription("Echo the input message back to the caller."),
	); err != nil {
		panic("agentic-service: MCP tool registration failed")
	}
	return srv
}

// mcpHTTPHandler returns the MCP server's HTTP handler wrapped in the
// kit's tenant middleware. The middleware lifts X-Tenant-Id from the
// request header onto context so MCP's default tenant extractor (which
// reads from core/tenant context) finds it. The kit's default
// extractor reads from ctx (assuming an upstream auth middleware did
// the resolution); this example deliberately trusts the
// caller-supplied header instead, so we opt in to HeaderExtractor
// explicitly. WithRequired(false) means requests without the header
// still pass through; the strict-audit gate inside MCP rejects them
// at the audit-precheck step rather than the transport edge — that
// gives the audit a single chokepoint and a uniform error code
// (-32603) regardless of which transport carried the call.
func mcpHTTPHandler(srv *mcp.Server) http.Handler {
	return tenant.New(
		tenant.WithExtractor(tenant.HeaderExtractor("X-Tenant-Id")),
		tenant.WithoutTenantRequired(),
	)(srv.HTTP())
}

// dangerousAction is a contrived endpoint that creates an approval
// request rather than executing immediately.
//
// SECURITY: in production, wrap this in:
//   - auth middleware (auth.JWT or RequireS2SAuth) so the
//     caller is authenticated; the actor field is derived from the
//     verified JWT subject (auth.UserID), NOT from a client header.
//   - the httpx/middleware/approval middleware which creates the
//     approval entry from a verified actor automatically.
//
// This handler reads X-Tenant-Id from the header because the demo
// bearer token is only an edge credential for local exercise.
// Production must take the tenant from the verified JWT or
// signed-request claim. The placeholder "demo-actor" actor below is
// a deliberate non-spoofable string so audit forensics on this
// example don't accept attacker-controlled values.
func dangerousAction(s approval.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID, ok := singletonRequestHeader(r, "X-Tenant-Id")
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "missing X-Tenant-Id")
			return
		}
		now := time.Now()
		id, err := uuid.NewV7()
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			return
		}
		req, err := s.Create(r.Context(), approval.Request{
			ID:        id.String(),
			TenantID:  tenantID,
			Actor:     "demo-actor",
			Action:    "admin.dangerous-action",
			Resource:  "example",
			State:     approval.StatePending,
			CreatedAt: now,
			ExpiresAt: now.Add(24 * time.Hour),
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal error")
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
		tenantID, ok := singletonRequestHeader(r, "X-Tenant-Id")
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "missing X-Tenant-Id")
			return
		}
		remaining, err := b.Peek(r.Context(), tenantID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant":    tenantID,
			"remaining": remaining,
		})
	}
}
