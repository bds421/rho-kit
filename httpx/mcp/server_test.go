package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/data/actionlog"
	actionlogmemory "github.com/bds421/rho-kit/data/actionlog/memory"
	"github.com/bds421/rho-kit/httpx/mcp"
)

type echoIn struct {
	Message string `json:"message" validate:"required"`
}

type echoOut struct {
	Echoed string `json:"echoed"`
}

func echoHandler(_ context.Context, in echoIn) (echoOut, error) {
	return echoOut{Echoed: in.Message}, nil
}

func newTestServer(t *testing.T, opts ...mcp.ServerOption) *mcp.Server {
	t.Helper()
	s := mcp.NewServer(opts...)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))
	return s
}

func doRPC(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

func TestServer_RoundTrip_Echo(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"echo","params":{"message":"hello"},"id":1}`)
	assert.Equal(t, "2.0", resp["jsonrpc"])
	assert.EqualValues(t, 1, resp["id"])
	require.Nil(t, resp["error"], "unexpected error: %v", resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, "hello", result["echoed"])
}

func TestServer_RoundTrip_ToolsCall(t *testing.T) {
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"}},"id":2}`
	resp := doRPC(t, s.HTTP(), body)
	require.Nil(t, resp["error"], "unexpected error: %v", resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, "hi", result["echoed"])
}

func TestServer_ToolsList_Sorted(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "z-tool", echoHandler))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "a-tool", echoHandler))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"tools/list","id":7}`)
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, 2)
	assert.Equal(t, "a-tool", tools[0].(map[string]any)["name"])
	assert.Equal(t, "z-tool", tools[1].(map[string]any)["name"])
}

func TestServer_ValidationFailure_ReturnsInvalidParams(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"echo","params":{},"id":3}`)
	require.NotNil(t, resp["error"])
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32602, rpcErr["code"], "validation failure must surface as -32602 Invalid params")
	msg := rpcErr["message"].(string)
	assert.Contains(t, msg, "message", "error message must mention the missing field")
}

func TestServer_UnknownTool(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"nope","id":4}`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32601, rpcErr["code"])
}

func TestServer_RejectsBatchRequests(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `[{"jsonrpc":"2.0","method":"echo","id":1}]`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32600, rpcErr["code"])
	assert.Contains(t, rpcErr["message"], "batch")
}

func TestServer_RejectsNonPOST(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	s.HTTP().ServeHTTP(w, r)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "POST", w.Header().Get("Allow"))
}

func TestServer_InvalidJSON_ReturnsParseError(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0",`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32700, rpcErr["code"])
}

func TestServer_HandlerErrorMappedToOperationFailed(t *testing.T) {
	s := mcp.NewServer()
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewOperationFailed("boom")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"fail","params":{"message":"x"},"id":9}`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32603, rpcErr["code"])
	assert.Equal(t, "boom", rpcErr["message"])
}

func TestServer_RejectsCyclicTypeAtRegistration(t *testing.T) {
	type cyclicIn struct {
		Self *cyclicIn `json:"self"`
	}
	s := mcp.NewServer()
	err := mcp.Register[cyclicIn, echoOut](s, "cyclic", func(_ context.Context, _ cyclicIn) (echoOut, error) {
		return echoOut{}, nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrCyclicSchema))
}

func TestServer_RejectsDuplicateName(t *testing.T) {
	s := newTestServer(t)
	err := mcp.Register[echoIn, echoOut](s, "echo", echoHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestServer_RejectsEmptyName(t *testing.T) {
	s := mcp.NewServer()
	err := mcp.Register[echoIn, echoOut](s, "", echoHandler)
	require.Error(t, err)
}

func TestServer_RejectsNilHandler(t *testing.T) {
	s := mcp.NewServer()
	err := mcp.Register[echoIn, echoOut](s, "x", nil)
	require.Error(t, err)
}

func TestServer_Initialize(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"initialize","id":11}`)
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	caps := result["capabilities"].(map[string]any)
	_, hasTools := caps["tools"]
	assert.True(t, hasTools)
}

func TestServer_DescriptionOverride(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler,
		mcp.WithToolDescription("Echo back the message verbatim."),
	))
	tools := s.Tools()
	require.Len(t, tools, 1)
	assert.Equal(t, "Echo back the message verbatim.", tools[0].Description)
}

func TestServer_DestructiveFlagAddsVendorExtension(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(true),
	))
	tools := s.Tools()
	require.Len(t, tools, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tools[0].InputSchema, &schema))
	assert.Equal(t, true, schema["x-destructive"])
}

func TestServer_BodyCap_RespectsLimit(t *testing.T) {
	s := newTestServer(t, mcp.WithMaxRequestBytes(64))
	huge := strings.Repeat("a", 200)
	body := `{"jsonrpc":"2.0","method":"echo","params":{"message":"` + huge + `"},"id":1}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	s.HTTP().ServeHTTP(w, r)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32700, rpcErr["code"])
}

// --- Action-log integration -------------------------------------------------

func newTestActionLogger(t *testing.T) (actionlog.Logger, *actionlogmemory.Store) {
	t.Helper()
	store := actionlogmemory.New()
	keys := map[string][]byte{
		"k1": bytes.Repeat([]byte{0x42}, 32),
	}
	secrets := actionlog.NewStaticSecrets("k1", keys)
	return actionlog.New(store, secrets), store
}

func withTenantHandler(next http.Handler, tenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenant.WithID(r.Context(), tenant.ID(tenantID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestServer_ActionLog_SuccessfulCallWritesEntry(t *testing.T) {
	logger, store := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger))

	h := withTenantHandler(s.HTTP(), "tenant-123")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "agent-7")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-123"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, "tenant-123", e.TenantID)
	assert.Equal(t, "agent-7", e.Actor)
	assert.Equal(t, "mcp.echo", e.Action)
	assert.Equal(t, actionlog.OutcomeSuccess, e.Outcome)
	assert.Empty(t, e.Reason, "success entries carry no reason")
	assert.Equal(t, "echo", e.Metadata["tool"])

	// Sanity check the store roundtripped one row.
	listed, err := store.List(context.Background(), actionlog.Query{})
	require.NoError(t, err)
	assert.Len(t, listed, 1)
}

func TestServer_ActionLog_FailureEntryRecordsReason(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger))
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, errors.New("kaboom")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	h := withTenantHandler(s.HTTP(), "tenant-9")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"fail","params":{"message":"x"},"id":1}`))
	r.Header.Set("X-Actor-Id", "agent-bad")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-9"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome)
	assert.Equal(t, "kaboom", entries[0].Reason)
}

func TestServer_ActionLog_SkipsWhenTenantMissing(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger))

	// No tenant injection — the request context never carries a
	// tenant ID, so the server must skip rather than write an
	// invalid entry.
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()
	s.HTTP().ServeHTTP(w, r)

	entries, err := logger.List(context.Background(), actionlog.Query{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestServer_ActorExtractor_OverrideUsedOverHeader(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t,
		mcp.WithActionLogger(logger),
		mcp.WithActorExtractor(func(_ *http.Request) string { return "fixed-actor" }),
	)
	h := withTenantHandler(s.HTTP(), "tenant-X")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "ignored")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	entries, err := logger.List(context.Background(), actionlog.Query{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "fixed-actor", entries[0].Actor)
}
