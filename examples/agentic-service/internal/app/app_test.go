package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/actionlog"
	actionlogmem "github.com/bds421/rho-kit/data/v2/actionlog/memory"
	"github.com/bds421/rho-kit/data/v2/approval"
	approvalmem "github.com/bds421/rho-kit/data/v2/approval/memory"
	budgetmem "github.com/bds421/rho-kit/data/v2/budget/memory"
)

type failingApprovalStore struct {
	approval.Store
}

func (f failingApprovalStore) Create(context.Context, approval.Request) (approval.Request, error) {
	return approval.Request{}, errors.New("postgres://user:secret@10.0.0.5/app failed")
}

type failingBudget struct{}

func (f failingBudget) Consume(context.Context, string, int64) (bool, int64, time.Duration, error) {
	return false, 0, 0, errors.New("unused")
}

func (f failingBudget) Peek(context.Context, string) (int64, error) {
	return 0, errors.New("redis://:secret@10.0.0.5:6379 unavailable")
}

func newTestLogger(t *testing.T) actionlog.Logger {
	t.Helper()
	return actionlog.New(actionlogmem.New(), actionlog.NewStaticSecrets("v1", map[string][]byte{
		"v1": []byte("at-least-32-bytes-of-secret-bytes!"),
	}))
}

func TestRun_StartsAndShutsDown(t *testing.T) {
	t.Setenv(demoTokenEnv, strings.Repeat("a", minDemoTokenLength))
	// Run with an immediately-cancelled ctx so ListenAndServe returns
	// quickly. Smoke test: the wiring compiles and survives a clean
	// shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, "127.0.0.1:0")
	require.NoError(t, err)
}

func TestRun_RequiresDemoToken(t *testing.T) {
	t.Setenv(demoTokenEnv, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), demoTokenEnv)
}

func TestRequireDemoBearerToken_RejectsMissingOrWrongToken(t *testing.T) {
	token := []byte(strings.Repeat("a", minDemoTokenLength))
	called := false
	h := requireDemoBearerToken(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, header := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, `Bearer realm="agentic-service-example"`, rec.Header().Get("WWW-Authenticate"))
	}
	assert.False(t, called)
}

func TestRequireDemoBearerToken_AllowsValidToken(t *testing.T) {
	token := strings.Repeat("a", minDemoTokenLength)
	h := requireDemoBearerToken([]byte(token), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMCPServer_EchoToolRoundtrip(t *testing.T) {
	srv := newMCPServer(newTestLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	mcpHTTPHandler(srv).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected result object, got %v", resp)
	assert.Equal(t, "hi", result["echoed"])
}

func TestMCPServer_RejectsValidationFailure(t *testing.T) {
	srv := newMCPServer(newTestLogger(t))

	// Missing required `message` field.
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{},"id":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	mcpHTTPHandler(srv).ServeHTTP(rec, req)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "expected error object on validation failure, got %v", resp)
	// JSON-RPC -32602 = Invalid params; the kit maps validation
	// failures to that code so SDKs can branch cleanly.
	assert.Equal(t, float64(-32602), errObj["code"])
}

// TestStrictAudit_RefusesWhenTenantMissing pins the H-2 audit-fix
// behaviour at the example layer: with an action logger configured
// and strict-audit on (the v2.0.0 default), MCP must refuse to
// dispatch a tool when no tenant resolves to context. The example
// wires the tenant middleware in non-required mode so the strict-audit
// gate inside MCP is the chokepoint — uniform -32603 regardless of
// transport.
func TestStrictAudit_RefusesWhenTenantMissing(t *testing.T) {
	srv := newMCPServer(newTestLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	req.Header.Set("Content-Type", "application/json")
	// X-Tenant-Id deliberately omitted.
	rec := httptest.NewRecorder()
	mcpHTTPHandler(srv).ServeHTTP(rec, req)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "expected error object when tenant is missing, got %v", resp)
	assert.Equal(t, float64(-32603), errObj["code"],
		"strict audit must refuse dispatch with -32603 internal error")
	// Confirm the tool did NOT execute by asserting no echoed result.
	_, hasResult := resp["result"]
	assert.False(t, hasResult, "tool must not run when audit precheck fails")
}

// TestDangerousAction_CreatesApprovalRequest exercises the approval
// flow the example advertises: POST /admin/dangerous-action returns
// 202 Accepted with an approval ID, and the approval store has a
// pending entry for the request.
func TestDangerousAction_CreatesApprovalRequest(t *testing.T) {
	store := approvalmem.New()
	h := dangerousAction(store)

	req := httptest.NewRequest(http.MethodPost, "/admin/dangerous-action", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))

	approvalID, ok := resp["approval_id"].(string)
	require.True(t, ok, "expected approval_id in response, got %v", resp)
	assert.NotEmpty(t, approvalID)
	assert.Equal(t, "pending", resp["status"])

	// Approval store actually contains the request — bind to a
	// real entry, not just a 202 response.
	got, err := store.Get(req.Context(), approvalID)
	require.NoError(t, err)
	assert.Equal(t, "acme", got.TenantID)
	assert.Equal(t, "demo-actor", got.Actor,
		"actor must be the deliberate non-spoofable placeholder, NOT a client header")
	assert.Equal(t, approval.StatePending, got.State)
}

func TestDangerousAction_StoreErrorDoesNotLeak(t *testing.T) {
	h := dangerousAction(failingApprovalStore{})

	req := httptest.NewRequest(http.MethodPost, "/admin/dangerous-action", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "internal error")
	assert.NotContains(t, rec.Body.String(), "secret")
	assert.NotContains(t, rec.Body.String(), "10.0.0.5")
}

func TestDangerousAction_MissingTenantUsesJSONError(t *testing.T) {
	h := dangerousAction(approvalmem.New())

	req := httptest.NewRequest(http.MethodPost, "/admin/dangerous-action", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":"missing X-Tenant-Id","code":"VALIDATION"}`, rec.Body.String())
}

// TestBudgetStatus_ReturnsRemaining confirms the /admin/budget
// endpoint returns the tenant's current budget remaining via the
// Peek API. Demonstrates the read-only side of the budget primitive.
func TestBudgetStatus_ReturnsRemaining(t *testing.T) {
	bud := budgetmem.New(1000, time.Minute)
	h := budgetStatus(bud)

	req := httptest.NewRequest(http.MethodGet, "/admin/budget", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))
	assert.Equal(t, "acme", resp["tenant"])
	// JSON encodes int64 as float64; assert via the float form.
	assert.Equal(t, float64(1000), resp["remaining"],
		"a fresh tenant should have the full per-period cap available")
}

func TestBudgetStatus_BackendErrorDoesNotLeak(t *testing.T) {
	h := budgetStatus(failingBudget{})

	req := httptest.NewRequest(http.MethodGet, "/admin/budget", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "internal error")
	assert.NotContains(t, rec.Body.String(), "secret")
	assert.NotContains(t, rec.Body.String(), "10.0.0.5")
}

func TestBudgetStatus_MissingTenantUsesJSONError(t *testing.T) {
	h := budgetStatus(budgetmem.New(1000, time.Minute))

	req := httptest.NewRequest(http.MethodGet, "/admin/budget", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":"missing X-Tenant-Id","code":"VALIDATION"}`, rec.Body.String())
}
