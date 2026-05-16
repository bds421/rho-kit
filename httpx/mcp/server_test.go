package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	actionlogmemory "github.com/bds421/rho-kit/data/v2/actionlog/memory"
	"github.com/bds421/rho-kit/httpx/v2/mcp"
)

type echoIn struct {
	Message string `json:"message" validate:"required"`
}

type echoOut struct {
	Echoed string `json:"echoed"`
}

type timeIn struct {
	At time.Time `json:"at"`
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

func withTestActor(actor string) mcp.ServerOption {
	return mcp.WithActorExtractor(func(*http.Request) string { return actor })
}

func TestNewServer_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		mcp.NewServer(nil)
	})
}

func TestRegister_PanicsOnNilOption(t *testing.T) {
	s := mcp.NewServer()
	assert.Panics(t, func() {
		_ = mcp.Register[echoIn, echoOut](s, "echo", echoHandler, nil)
	})
}

func TestWithActorFromHeader_PanicsOnInvalidHeaderName(t *testing.T) {
	assert.Panics(t, func() { mcp.WithActorFromHeader("") })
	assert.Panics(t, func() { mcp.WithActorFromHeader("Bad Header") })
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

type failingReadCloser struct {
	err error
}

func (f failingReadCloser) Read([]byte) (int, error) {
	return 0, f.err
}

func (f failingReadCloser) Close() error {
	return nil
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
	// tools/call returns the MCP-spec envelope:
	// {result: {content: [{type:"text", text:"<json>"}], isError:false,
	//          structuredContent: <raw output>}}.
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"}},"id":2}`
	resp := doRPC(t, s.HTTP(), body)
	require.Nil(t, resp["error"], "unexpected error: %v", resp["error"])
	result := resp["result"].(map[string]any)
	content, ok := result["content"].([]any)
	require.True(t, ok, "tools/call result must carry an MCP content array, got %v", result)
	require.Len(t, content, 1)
	first := content[0].(map[string]any)
	assert.Equal(t, "text", first["type"])
	assert.Contains(t, first["text"], `"echoed":"hi"`)
	structured := result["structuredContent"].(map[string]any)
	assert.Equal(t, "hi", structured["echoed"])
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

func TestServer_MessageValidationErrorDoesNotReflectHandlerText(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "validate", func(context.Context, echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewValidation("secret-token is invalid")
	}))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"validate","params":{"message":"x"},"id":3}`)

	require.NotNil(t, resp["error"])
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32602, rpcErr["code"])
	assert.Equal(t, "invalid request", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "secret-token")
}

func TestServer_NotFoundErrorDoesNotReflectHandlerText(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "lookup", func(context.Context, echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewNotFound("secret-token-entity", "secret-token-id")
	}))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"lookup","params":{"message":"x"},"id":3}`)

	require.NotNil(t, resp["error"])
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32602, rpcErr["code"])
	assert.Equal(t, "resource not found", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "secret-token")
}

func TestServer_DecodeFailureDoesNotReflectArgumentValue(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[timeIn, echoOut](s, "time", func(context.Context, timeIn) (echoOut, error) {
		return echoOut{}, nil
	}))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"time","params":{"at":"secret-token"},"id":3}`)

	require.NotNil(t, resp["error"])
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32602, rpcErr["code"])
	assert.Equal(t, "invalid arguments", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "secret-token")
	assert.NotContains(t, rpcErr["message"], "parsing time")
}

func TestServer_UnknownTool(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"secret-token-method","id":4}`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32601, rpcErr["code"])
	assert.Equal(t, "method not found", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "secret-token")
}

func TestServer_UnknownToolsCallNameDoesNotReflectToolName(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"secret-token-tool","arguments":{}},"id":4}`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32601, rpcErr["code"])
	assert.Equal(t, "tool not found", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "secret-token")
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
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"secret-token",`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32700, rpcErr["code"])
	assert.Equal(t, "invalid JSON", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "secret-token")
	assert.NotContains(t, rpcErr["message"], "invalid character")
}

func TestServer_ReadBodyErrorDoesNotLeakRawDetails(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Body = failingReadCloser{err: errors.New("read tcp 10.0.0.5:443: secret backend token")}
	w := httptest.NewRecorder()

	s.HTTP().ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32700, rpcErr["code"])
	assert.Equal(t, "failed to read request body", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "10.0.0.5")
	assert.NotContains(t, rpcErr["message"], "secret")
}

func TestServer_HandlerErrorMappedToOperationFailed(t *testing.T) {
	// Default and conflict branches sanitise the error text to
	// avoid leaking infrastructure detail (security review M-1).
	// Server-side logs preserve error type for triage without copying
	// backend diagnostics.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	s := mcp.NewServer(mcp.WithLogger(logger))
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewOperationFailed("boom: pq: relation \"x\" does not exist")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"fail","params":{"message":"x"},"id":9}`)
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32603, rpcErr["code"])
	assert.Equal(t, "internal error", rpcErr["message"],
		"default branch must not leak raw error text to JSON-RPC caller")

	logged := logBuf.String()
	assert.Contains(t, logged, "<redacted error")
	assert.NotContains(t, logged, "boom: pq: relation")
}

func TestServer_RejectsCyclicTypeAtRegistration(t *testing.T) {
	type secretTokenCyclicIn struct {
		Self *secretTokenCyclicIn `json:"self"`
	}
	s := mcp.NewServer()
	err := mcp.Register[secretTokenCyclicIn, echoOut](s, "secret-token-tool", func(_ context.Context, _ secretTokenCyclicIn) (echoOut, error) {
		return echoOut{}, nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrCyclicSchema))
	assert.NotContains(t, err.Error(), "secret-token-tool")
	assert.NotContains(t, err.Error(), "secretTokenCyclicIn")
}

func TestServer_RejectsUnsupportedTypeWithoutReflectingName(t *testing.T) {
	type secretTokenUnsupportedIn struct {
		C chan int `json:"c"`
	}
	s := mcp.NewServer()
	err := mcp.Register[secretTokenUnsupportedIn, echoOut](s, "secret-token-tool", func(_ context.Context, _ secretTokenUnsupportedIn) (echoOut, error) {
		return echoOut{}, nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrUnsupportedType))
	assert.NotContains(t, err.Error(), "secret-token-tool")
	assert.NotContains(t, err.Error(), "secretTokenUnsupportedIn")
	assert.NotContains(t, err.Error(), "chan")
}

func TestServer_RejectsDuplicateName(t *testing.T) {
	s := newTestServer(t)
	err := mcp.Register[echoIn, echoOut](s, "echo", echoHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
	assert.NotContains(t, err.Error(), "echo")
}

func TestServer_RejectsEmptyName(t *testing.T) {
	s := mcp.NewServer()
	err := mcp.Register[echoIn, echoOut](s, "", echoHandler)
	require.Error(t, err)
}

func TestServer_RejectsInvalidToolNames(t *testing.T) {
	tests := []struct {
		name string
		tool string
	}{
		{name: "space", tool: "bad name"},
		{name: "tab", tool: "bad\tname"},
		{name: "control", tool: "bad\x00name"},
		{name: "invalid utf8", tool: string([]byte{0xff})},
		{name: "leading punctuation", tool: ".hidden"},
		{name: "colon", tool: "tool:name"},
		{name: "too long", tool: strings.Repeat("a", mcp.MaxToolNameLen+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mcp.NewServer()
			err := mcp.Register[echoIn, echoOut](s, tt.tool, echoHandler)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid tool name")
			assert.NotContains(t, err.Error(), tt.tool)
		})
	}
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
	s := mcp.NewServer(mcp.WithoutDestructiveGate())
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(),
	))
	tools := s.Tools()
	require.Len(t, tools, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tools[0].InputSchema, &schema))
	assert.Equal(t, true, schema["x-destructive"])
}

func TestServer_DestructiveToolRefusedWithoutGate(t *testing.T) {
	// Default Server: a destructive tool registered without
	// WithDestructiveGate or WithoutDestructiveGate must fail at
	// dispatch — the kit refuses to let the call through.
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(),
	))
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"delete","params":{"message":"hi"},"id":1}`)
	require.NotNil(t, resp["error"], "destructive tool with no gate must error, got: %v", resp)
}

func TestServer_DestructiveToolAllowedWhenGateApproves(t *testing.T) {
	called := 0
	gate := func(_ context.Context, name string, payload []byte) error {
		called++
		assert.Equal(t, "delete", name)
		assert.Contains(t, string(payload), "hi")
		return nil
	}
	s := mcp.NewServer(mcp.WithDestructiveGate(gate))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(),
	))
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"delete","params":{"message":"hi"},"id":1}`)
	require.Nil(t, resp["error"], "approved destructive call must succeed: %v", resp["error"])
	assert.Equal(t, 1, called, "gate must be invoked exactly once per destructive call")
}

func TestServer_DestructiveToolRefusedWhenGateRefuses(t *testing.T) {
	gate := func(context.Context, string, []byte) error {
		return errors.New("approval denied")
	}
	s := mcp.NewServer(mcp.WithDestructiveGate(gate))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(),
	))
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"delete","params":{"message":"hi"},"id":1}`)
	require.NotNil(t, resp["error"], "gate refusal must surface as a JSON-RPC error")
}

func TestServer_NonDestructiveToolBypassesGate(t *testing.T) {
	called := 0
	gate := func(context.Context, string, []byte) error {
		called++
		return errors.New("should not be called for non-destructive")
	}
	s := mcp.NewServer(mcp.WithDestructiveGate(gate))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`)
	require.Nil(t, resp["error"])
	assert.Equal(t, 0, called, "gate must not be invoked for non-destructive tools")
}

func TestRegister_RejectsInvalidSchemaOverrides(t *testing.T) {
	tests := []struct {
		name string
		opt  mcp.ToolOption
	}{
		{name: "invalid input JSON", opt: mcp.WithInputSchema(json.RawMessage(`{`))},
		{name: "input array", opt: mcp.WithInputSchema(json.RawMessage(`[]`))},
		{name: "output null", opt: mcp.WithOutputSchema(json.RawMessage(`null`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mcp.NewServer()
			err := mcp.Register[echoIn, echoOut](s, "secret-token-tool", echoHandler, tt.opt)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "schema")
			assert.NotContains(t, err.Error(), "secret-token-tool")
		})
	}
}

func TestRegister_CopiesSchemaOverridesAndToolsReturn(t *testing.T) {
	s := mcp.NewServer()
	inputSchema := json.RawMessage(`{"type":"object","title":"original"}`)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler,
		mcp.WithInputSchema(inputSchema),
	))

	for i := range inputSchema {
		inputSchema[i] = ' '
	}
	tools := s.Tools()
	require.Len(t, tools, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tools[0].InputSchema, &schema))
	assert.Equal(t, "original", schema["title"])

	for i := range tools[0].InputSchema {
		tools[0].InputSchema[i] = ' '
	}
	tools = s.Tools()
	require.NoError(t, json.Unmarshal(tools[0].InputSchema, &schema),
		"mutating a Tools() result must not mutate the server catalog")
	assert.Equal(t, "original", schema["title"])
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
	assert.Equal(t, "request body exceeds maximum size", rpcErr["message"])
	assert.NotContains(t, rpcErr["message"], "64")
}

// --- Action-log integration -------------------------------------------------

func newTestActionLogger(t *testing.T) (actionlog.Logger, *actionlogmemory.Store) {
	t.Helper()
	cursorSigner, err := actionlog.NewCursorSigner(bytes.Repeat([]byte{0x55}, 32))
	require.NoError(t, err)
	store := actionlogmemory.New(cursorSigner)
	keys := map[string][]byte{
		"k1": bytes.Repeat([]byte{0x42}, 32),
	}
	secrets := actionlog.NewStaticSecrets("k1", keys)
	return actionlog.New(store, secrets), store
}

func withTenantHandler(next http.Handler, tenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := tenant.WithID(r.Context(), tenant.ID(tenantID))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestServer_ActionLog_SuccessfulCallWritesEntry(t *testing.T) {
	logger, store := newTestActionLogger(t)
	// Opt in to the legacy X-Actor-Id behaviour for this trust-boundary
	// fixture — the default extractor no longer trusts the header.
	s := newTestServer(t, mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))

	h := withTenantHandler(s.HTTP(), "tenant-123")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "agent-7")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-123"})
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
	listed, _, err := store.List(context.Background(), actionlog.Query{TenantID: "tenant-123"})
	require.NoError(t, err)
	assert.Len(t, listed, 1)
}

func TestServer_ActionLog_FailureEntryRecordsReason(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))
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

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-9"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome)
	assert.Equal(t, "kaboom", entries[0].Reason)
}

// invokeCounterHandler returns an echo handler that increments the
// supplied counter on every dispatch. Used to assert that strict
// audit mode prevents the tool from running when the tenant cannot
// be resolved.
func invokeCounterHandler(counter *int) mcp.Handler[echoIn, echoOut] {
	return func(_ context.Context, in echoIn) (echoOut, error) {
		*counter++
		return echoOut{Echoed: in.Message}, nil
	}
}

func TestServer_ActionLog_StrictMode_NoTenant_RefusesDispatch(t *testing.T) {
	// H-2 fix: when an action logger is configured and tenant
	// resolution fails, strict mode (the default) must return
	// -32603 internal error AND NOT execute the tool. The audit
	// invariant — every executed tool produces a signed entry —
	// is preserved by refusing dispatch.
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger))

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	resp := doRPC(t, s.HTTP(),
		`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`)

	require.NotNil(t, resp["error"], "expected JSON-RPC error in strict mode without tenant")
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32603, rpcErr["code"])
	assert.Equal(t, "internal error", rpcErr["message"])

	assert.Equal(t, 0, calls, "tool MUST NOT execute when strict-mode audit cannot be attributed")

	entries, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Empty(t, entries, "no audit entry should be written when tool was refused")
}

func TestServer_ActionLog_StrictMode_TenantExtractorPanicRefusesDispatch(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithActionLogger(logger),
		mcp.WithTenantExtractor(func(context.Context) (string, bool) {
			panic("tenant failed")
		}),
	)

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	resp := doRPC(t, s.HTTP(),
		`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`)

	require.NotNil(t, resp["error"], "expected JSON-RPC error when tenant extractor panics")
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32603, rpcErr["code"])
	assert.Equal(t, 0, calls, "tool must not execute when strict audit tenant extraction panics")
}

func TestServer_ActionLog_LooseMode_NoTenant_RunsToolAndSkipsAudit(t *testing.T) {
	// Loose mode preserves the legacy fail-open behaviour: log a
	// warning, skip the audit entry, run the tool. Operators must
	// opt in via WithBestEffortAuditOnMissingTenant().
	var logBuf bytes.Buffer
	slogger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithLogger(slogger),
		mcp.WithActionLogger(logger),
		mcp.WithBestEffortAuditOnMissingTenant(),
	)

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	resp := doRPC(t, s.HTTP(),
		`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`)

	require.Nil(t, resp["error"], "loose mode must let the call succeed: %v", resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, "hi", result["echoed"])
	assert.Equal(t, 1, calls, "tool must execute in loose mode")

	entries, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Empty(t, entries, "loose mode skips the audit entry when tenant absent")

	assert.Contains(t, logBuf.String(), "skipping action log entry",
		"loose mode must emit a warn-level log so operators can spot unscoped tool calls")
}

func TestServer_ActionLog_StrictMode_WithTenant_WritesEntry(t *testing.T) {
	// Strict mode (the default) must not break the happy path.
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))

	h := withTenantHandler(s.HTTP(), "tenant-strict")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "agent-strict")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-strict"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "agent-strict", entries[0].Actor)
}

// asyncBlockingLogger wraps a real action logger but blocks Append
// until a signal channel is closed. Used to prove that async mode
// does not extend MCP latency: the response must arrive before the
// audit append releases.
type asyncBlockingLogger struct {
	inner   actionlog.Logger
	release chan struct{}
	wg      *sync.WaitGroup
}

func (l *asyncBlockingLogger) Append(ctx context.Context, e actionlog.Entry) (actionlog.Entry, error) {
	defer l.wg.Done()
	<-l.release
	return l.inner.Append(ctx, e)
}

func (l *asyncBlockingLogger) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	return l.inner.Get(ctx, id)
}

func (l *asyncBlockingLogger) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, string, error) {
	return l.inner.List(ctx, q)
}

func (l *asyncBlockingLogger) VerifyChain(ctx context.Context, tenantID string) error {
	return l.inner.VerifyChain(ctx, tenantID)
}

type auditContextKey struct{}

type contextRecordingLogger struct {
	inner  actionlog.Logger
	value  any
	ctxErr error
}

func (l *contextRecordingLogger) Append(ctx context.Context, e actionlog.Entry) (actionlog.Entry, error) {
	l.value = ctx.Value(auditContextKey{})
	l.ctxErr = ctx.Err()
	return l.inner.Append(ctx, e)
}

func (l *contextRecordingLogger) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	return l.inner.Get(ctx, id)
}

func (l *contextRecordingLogger) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, string, error) {
	return l.inner.List(ctx, q)
}

func (l *contextRecordingLogger) VerifyChain(ctx context.Context, tenantID string) error {
	return l.inner.VerifyChain(ctx, tenantID)
}

func TestServer_ActionLog_AsyncMode_RespondsBeforeAppend(t *testing.T) {
	// L-3 fix: WithAsyncAuditDispatch() spawns the audit append in a
	// background goroutine so MCP latency does not depend on the
	// audit store's response time.
	innerLogger, _ := newTestActionLogger(t)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	blocking := &asyncBlockingLogger{
		inner:   innerLogger,
		release: make(chan struct{}),
		wg:      wg,
	}

	s := mcp.NewServer(
		mcp.WithActionLogger(blocking),
		withTestActor("agent-async"),
		mcp.WithAsyncAuditDispatch(),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	h := withTenantHandler(s.HTTP(), "tenant-async")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)
	// Response must already be written even though the audit
	// append is still blocked.
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Nil(t, resp["error"])

	// At this point the goroutine is parked on `<-release`. No
	// entry has reached the inner store yet.
	entriesEarly, _, err := innerLogger.List(context.Background(), actionlog.Query{TenantID: "tenant-async"})
	require.NoError(t, err)
	assert.Empty(t, entriesEarly, "async append must not yet have written when response is returned")

	// Release the audit, wait for the goroutine to land.
	close(blocking.release)
	wg.Wait()

	entriesLate, _, err := innerLogger.List(context.Background(), actionlog.Query{TenantID: "tenant-async"})
	require.NoError(t, err)
	require.Len(t, entriesLate, 1, "async append must eventually write the entry")
	assert.Equal(t, "mcp.echo", entriesLate[0].Action)
}

func TestServer_ActionLog_AsyncMode_PreservesContextValuesAfterCancellation(t *testing.T) {
	innerLogger, _ := newTestActionLogger(t)
	logger := &contextRecordingLogger{inner: innerLogger}
	s := mcp.NewServer(
		mcp.WithActionLogger(logger),
		withTestActor("agent-async-context"),
		mcp.WithAsyncAuditDispatch(),
		mcp.WithAsyncAuditWorkers(1),
		mcp.WithAsyncAuditQueue(1),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	parent := context.WithValue(context.Background(), auditContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`)).
		WithContext(ctx)
	w := httptest.NewRecorder()
	withTenantHandler(s.HTTP(), "tenant-async-context").ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, s.Stop(stopCtx))

	assert.Equal(t, "trace-123", logger.value)
	assert.NoError(t, logger.ctxErr)
}

func TestServer_ActionLog_SyncMode_AppendBeforeResponse(t *testing.T) {
	// Sync mode (the default) writes the audit entry before the
	// JSON-RPC response — the entry is visible the moment
	// ServeHTTP returns.
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger), withTestActor("agent-sync"))

	h := withTenantHandler(s.HTTP(), "tenant-sync")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-sync"})
	require.NoError(t, err)
	require.Len(t, entries, 1, "sync mode writes the entry before returning the response")
}

func TestServer_RejectsTrailingJSONInBody(t *testing.T) {
	// `{...} {...}` at the top of the request body is two JSON
	// values back to back. json.Unmarshal flags this as
	// "invalid character after top-level value" — the handler must
	// reject the call rather than accept the first object and
	// ignore the trailing data.
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1} {"y":2}`
	resp := doRPC(t, s.HTTP(), body)
	require.NotNil(t, resp["error"], "trailing JSON must be rejected")
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32700, rpcErr["code"])
}

func TestServer_DisallowUnknownFields_ReturnsGenericMessage(t *testing.T) {
	// L-4 fix: an extra field in the params payload must be
	// rejected as -32602 with a generic "invalid request" message.
	// The decoder's "json: unknown field \"foo\"" string would
	// otherwise leak the input-struct shape.
	var logBuf bytes.Buffer
	slogger := slog.New(slog.NewTextHandler(&logBuf, nil))
	s := newTestServer(t, mcp.WithLogger(slogger))

	resp := doRPC(t, s.HTTP(),
		`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi","extra":"bad"},"id":1}`)

	require.NotNil(t, resp["error"])
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32602, rpcErr["code"])
	assert.Equal(t, "invalid request", rpcErr["message"],
		"unknown-field rejection must not echo the field name to the caller")

	logged := logBuf.String()
	assert.Contains(t, logged, "unknown field",
		"server-side log should retain the stable rejection reason")
	assert.NotContains(t, logged, "extra",
		"server-side log must not retain the caller-controlled field name")
	assert.NotContains(t, logged, "json:",
		"server-side log must not retain the decoder's raw error text")
}

func TestDefaultActorExtractor_StrictAuditRefusesAnonymousDespiteHeader(t *testing.T) {
	// The default actor extractor must NOT read X-Actor-Id: any
	// caller can set the header and forge the audit trail. With an
	// action logger configured, strict audit now refuses to dispatch
	// unless a verified actor extractor is wired explicitly.
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger))

	h := withTenantHandler(s.HTTP(), "tenant-h7")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "alice")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.NotNil(t, resp["error"], "strict audit must reject missing actor attribution")
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32603, rpcErr["code"])

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-h7"})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestWithAllowAnonymousActor_RecordsAnonymous(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t,
		mcp.WithActionLogger(logger),
		mcp.WithAllowAnonymousActor(),
	)

	h := withTenantHandler(s.HTTP(), "tenant-anon")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "spoofed")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-anon"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, mcp.AnonymousActor, entries[0].Actor)
	assert.NotEqual(t, "spoofed", entries[0].Actor)
}

func TestWithActorFromContext_ReadsAuthContext(t *testing.T) {
	// Define a private context key that mirrors how an auth middleware
	// would attach the verified user id to the request context.
	type userIDKey struct{}
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t,
		mcp.WithActionLogger(logger),
		mcp.WithActorFromContext(func(ctx context.Context) string {
			v, _ := ctx.Value(userIDKey{}).(string)
			return v
		}),
	)

	// Wrap the handler in a thin middleware that mocks the auth
	// middleware contract: it stamps the verified user id onto the
	// context BEFORE the MCP server sees the request.
	h := withTenantHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userIDKey{}, "verified-bob")
		s.HTTP().ServeHTTP(w, r.WithContext(ctx))
	}), "tenant-h7-ctx")

	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "spoofed")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-h7-ctx"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "verified-bob", entries[0].Actor,
		"WithActorFromContext must read from the auth-populated context, not the request header")
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

	entries, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "fixed-actor", entries[0].Actor)
}

func TestServer_ActorExtractorPanicRefusesStrictAuditDispatch(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t,
		mcp.WithActionLogger(logger),
		mcp.WithActorExtractor(func(*http.Request) string {
			panic("actor failed")
		}),
	)
	h := withTenantHandler(s.HTTP(), "tenant-actor-panic")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.NotNil(t, resp["error"])

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-actor-panic"})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestServer_ActorExtractorInvalidRefusesStrictAuditDispatch(t *testing.T) {
	tests := []struct {
		name  string
		actor string
	}{
		{name: "space", actor: "bad actor"},
		{name: "control", actor: "bad\x00actor"},
		{name: "too long", actor: strings.Repeat("a", actionlog.MaxActorLen+1)},
		{name: "invalid utf8", actor: string([]byte{0xff})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := newTestActionLogger(t)
			s := newTestServer(t,
				mcp.WithActionLogger(logger),
				mcp.WithActorExtractor(func(*http.Request) string { return tt.actor }),
			)
			h := withTenantHandler(s.HTTP(), "tenant-actor-invalid")
			r := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, http.StatusOK, w.Code)

			var resp map[string]any
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			require.NotNil(t, resp["error"])

			entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-actor-invalid"})
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestWithActorFromHeader_AmbiguousHeaderRefusesStrictAuditDispatch(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*http.Request)
	}{
		{
			name: "duplicate",
			setup: func(r *http.Request) {
				r.Header.Add("X-Actor-Id", "alice")
				r.Header.Add("X-Actor-Id", "bob")
			},
		},
		{
			name: "blank",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor-Id", " ")
			},
		},
		{
			name: "internal whitespace",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor-Id", "alice bob")
			},
		},
		{
			name: "comma combined",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor-Id", "alice,bob")
			},
		},
		{
			name: "control",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor-Id", "alice\nbob")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := newTestActionLogger(t)
			s := newTestServer(t,
				mcp.WithActionLogger(logger),
				mcp.WithActorFromHeader("X-Actor-Id"),
			)
			h := withTenantHandler(s.HTTP(), "tenant-actor-header")
			r := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
			tt.setup(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, http.StatusOK, w.Code)

			var resp map[string]any
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			require.NotNil(t, resp["error"])

			entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-actor-header"})
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

// failingLogger always returns an error from Append. Used to prove
// strict-mode fail-closed behaviour when the audit store is down.
type failingLogger struct {
	inner   actionlog.Logger
	appends int64
}

func (l *failingLogger) Append(_ context.Context, _ actionlog.Entry) (actionlog.Entry, error) {
	atomic.AddInt64(&l.appends, 1)
	return actionlog.Entry{}, errors.New("audit store unavailable")
}

func (l *failingLogger) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	return l.inner.Get(ctx, id)
}

func (l *failingLogger) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, string, error) {
	return l.inner.List(ctx, q)
}

func (l *failingLogger) VerifyChain(ctx context.Context, tenantID string) error {
	return l.inner.VerifyChain(ctx, tenantID)
}

func TestServer_ActionLog_StrictMode_AppendFailure_FailsResponse(t *testing.T) {
	// Strict + sync mode must fail-closed when the audit store
	// rejects the append. The tool may have run, but the JSON-RPC
	// response is -32603 internal error so the caller never sees
	// "success" without a durable signed entry.
	inner, _ := newTestActionLogger(t)
	logger := &failingLogger{inner: inner}
	s := mcp.NewServer(mcp.WithActionLogger(logger), withTestActor("agent-fail"))

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	h := withTenantHandler(s.HTTP(), "tenant-fail")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.NotNil(t, resp["error"], "strict-mode append failure must surface as JSON-RPC error")
	rpcErr := resp["error"].(map[string]any)
	assert.EqualValues(t, -32603, rpcErr["code"])

	assert.EqualValues(t, 1, atomic.LoadInt64(&logger.appends), "append must have been attempted exactly once")
	assert.Equal(t, 1, calls, "tool ran before the audit append failed (documented ordering)")
}

func TestServer_ActionLog_LooseMode_AppendFailure_StillReturnsResult(t *testing.T) {
	// Loose mode preserves the legacy fail-open behaviour: append
	// failures are logged but the result is returned. Operators have
	// explicitly opted out of the audit invariant.
	var logBuf bytes.Buffer
	slogger := slog.New(slog.NewTextHandler(&logBuf, nil))

	inner, _ := newTestActionLogger(t)
	logger := &failingLogger{inner: inner}
	s := mcp.NewServer(
		mcp.WithLogger(slogger),
		mcp.WithActionLogger(logger),
		withTestActor("agent-loose-fail"),
		mcp.WithBestEffortAuditOnMissingTenant(),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	h := withTenantHandler(s.HTTP(), "tenant-loose-fail")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Nil(t, resp["error"], "loose mode must still return success on append failure: %v", resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, "hi", result["echoed"])
	assert.Contains(t, logBuf.String(), "action log append failed")
}

// blockingForeverLogger never returns from Append. Used to prove the
// async worker pool is bounded: more concurrent calls than the queue
// can hold must drop entries (counter increment) rather than spawn
// unbounded goroutines.
type blockingForeverLogger struct {
	inner   actionlog.Logger
	release chan struct{}
	calls   int64
}

func (l *blockingForeverLogger) Append(ctx context.Context, e actionlog.Entry) (actionlog.Entry, error) {
	atomic.AddInt64(&l.calls, 1)
	select {
	case <-l.release:
	case <-ctx.Done():
		return actionlog.Entry{}, ctx.Err()
	}
	return l.inner.Append(ctx, e)
}

func (l *blockingForeverLogger) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	return l.inner.Get(ctx, id)
}

func (l *blockingForeverLogger) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, string, error) {
	return l.inner.List(ctx, q)
}

func (l *blockingForeverLogger) VerifyChain(ctx context.Context, tenantID string) error {
	return l.inner.VerifyChain(ctx, tenantID)
}

func TestServer_AsyncAudit_QueueSaturation_DropsRatherThanLeaks(t *testing.T) {
	// Tight bound: 1 worker + queue depth 1 = at most 2 in-flight
	// appends. Anything beyond that is dropped, surfaced via
	// Server.AsyncAuditDropped().
	inner, _ := newTestActionLogger(t)
	blocking := &blockingForeverLogger{
		inner:   inner,
		release: make(chan struct{}),
	}
	s := mcp.NewServer(
		mcp.WithActionLogger(blocking),
		withTestActor("agent-sat"),
		mcp.WithAsyncAuditDispatch(),
		mcp.WithAsyncAuditWorkers(1),
		mcp.WithAsyncAuditQueue(1),
		mcp.WithAsyncAuditTimeout(2*time.Second),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	h := withTenantHandler(s.HTTP(), "tenant-sat")

	const N = 20
	for i := 0; i < N; i++ {
		r := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code, "async mode must keep responding while queue saturates")
	}

	dropped := s.AsyncAuditDropped()
	assert.Greater(t, dropped, int64(0), "saturated queue must drop entries rather than leak goroutines")
	assert.Less(t, dropped, int64(N), "not every request should drop; the worker + queue should absorb some")

	close(blocking.release)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(stopCtx))
}

func TestServer_AsyncAudit_StopDrainsWorkers(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithActionLogger(logger),
		withTestActor("agent-drain"),
		mcp.WithAsyncAuditDispatch(),
		mcp.WithAsyncAuditWorkers(2),
		mcp.WithAsyncAuditQueue(8),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	h := withTenantHandler(s.HTTP(), "tenant-drain")
	for i := 0; i < 4; i++ {
		r := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(stopCtx), "Stop must drain within timeout")

	// All 4 entries should now be visible.
	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-drain"})
	require.NoError(t, err)
	assert.Len(t, entries, 4)
}

func TestServer_StopRejectsNilContext(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithActionLogger(logger),
		withTestActor("agent-stop"),
		mcp.WithAsyncAuditDispatch(),
		mcp.WithAsyncAuditWorkers(1),
		mcp.WithAsyncAuditQueue(1),
	)

	var ctx context.Context
	err := s.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")

	require.NoError(t, s.Stop(context.Background()))
}

// TestServer_AsyncAudit_StopRace_NoLostJobs proves the round-3 fix:
// concurrent enqueueAuditJob calls racing Server.Stop must either be
// counted as a successful append (visible in the store) or counted as
// dropped — never silently lost. Before the fix, the two-step
// "select<-done; select queue<-/<-done/default" pattern allowed the Go
// scheduler to pick the queue-send case after Stop had already closed
// auditDone; a worker that had already entered its drain branch could
// then exit without seeing that send.
func TestServer_AsyncAudit_StopRace_NoLostJobs(t *testing.T) {
	const N = 100
	logger, store := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithActionLogger(logger),
		withTestActor("agent-race"),
		mcp.WithAsyncAuditDispatch(),
		mcp.WithAsyncAuditWorkers(2),
		mcp.WithAsyncAuditQueue(N),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	h := withTenantHandler(s.HTTP(), "tenant-race")

	droppedBefore := s.AsyncAuditDropped()
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			r := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
		}()
	}
	close(start)

	// Race Stop against the senders. Some senders win and enqueue; the
	// rest are dropped. Either way no entry may vanish unaccounted.
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(stopCtx))
	wg.Wait()

	entries, _, err := store.List(context.Background(), actionlog.Query{TenantID: "tenant-race"})
	require.NoError(t, err)
	dropped := s.AsyncAuditDropped() - droppedBefore
	assert.Equal(t, int64(N), int64(len(entries))+dropped,
		"every enqueue must either land in the store (%d) or be counted dropped (%d); none may be silently lost",
		len(entries), dropped)
}

func TestServer_ToolsCall_ReturnsMCPContentShape(t *testing.T) {
	// MCP-spec compliant: tools/call result must be
	// {content: [{type:"text", text:"<json>"}], isError:false,
	//  structuredContent: <raw>}.
	// The 2024-11-05 spec defines content types text/image/audio/resource;
	// "json" is not in the union and was a kit-only extension before this
	// release. structuredContent is the 2025-03-26-forward-compatible
	// surface for clients that want the raw JSON without re-parsing text.
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"message":"world"}},"id":1}`
	resp := doRPC(t, s.HTTP(), body)
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	content, ok := result["content"].([]any)
	require.True(t, ok, "tools/call must wrap the tool output in a content array")
	require.Len(t, content, 1)
	first := content[0].(map[string]any)
	assert.Equal(t, "text", first["type"])
	assert.Contains(t, first["text"], `"echoed":"world"`,
		"text content must carry the JSON-encoded tool output")
	assert.Equal(t, false, result["isError"],
		"successful tool call must surface isError=false")
	structured, ok := result["structuredContent"].(map[string]any)
	require.True(t, ok, "tools/call must surface structuredContent for spec-2025-03-26 clients")
	assert.Equal(t, "world", structured["echoed"])
}

func TestServer_ShorthandCall_ReturnsRawResult(t *testing.T) {
	// Shorthand (method = tool name) keeps the raw output for kit
	// consumers using the typed Out struct directly.
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"echo","params":{"message":"world"},"id":1}`
	resp := doRPC(t, s.HTTP(), body)
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	_, hasContent := result["content"]
	assert.False(t, hasContent, "shorthand calls must NOT wrap in content array")
	assert.Equal(t, "world", result["echoed"])
}

func TestTruncateReason_PreservesUTF8Boundaries(t *testing.T) {
	// Test the unexported truncateReason indirectly via a failure
	// audit reason. Build an error string longer than MaxReasonLength
	// (1024 bytes) using 3-byte UTF-8 runes positioned so the cap
	// would land mid-rune. The recorded Reason must remain valid
	// UTF-8 (no unicode replacement glyph, decodable round-trip).
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger), withTestActor("agent-utf8"))

	// Each '★' is 3 bytes; 400 of them = 1200 bytes > 1024.
	long := strings.Repeat("★", 400)
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, errors.New(long)
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "boom", boom))

	h := withTenantHandler(s.HTTP(), "tenant-utf8")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"boom","params":{"message":"x"},"id":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-utf8"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	reason := entries[0].Reason
	// LessOrEqual since the truncation walks back to a rune boundary.
	assert.LessOrEqual(t, len(reason), 1024+3, "truncated reason must fit within cap + ellipsis")
	assert.True(t, strings.HasSuffix(reason, "..."), "truncated reason must end with ellipsis")
	core := strings.TrimSuffix(reason, "...")
	for i, r := range core {
		_ = i
		// Ranging produces RuneError on invalid bytes.
		assert.NotEqual(t, '�', r, "truncated reason must contain no replacement runes (invalid UTF-8)")
	}
}

type embedHello struct {
	Hello string `json:"hello"`
}

type embedWrapper struct {
	*embedHello
	Extra string `json:"extra"`
}

func TestRegister_AnonymousPointerEmbedDoesNotPanic(t *testing.T) {
	s := mcp.NewServer()
	require.NotPanics(t, func() {
		err := mcp.Register[embedWrapper, echoOut](s, "embed",
			func(_ context.Context, _ embedWrapper) (echoOut, error) { return echoOut{}, nil })
		require.NoError(t, err, "registration with anonymous *Embedded must succeed")
	})
}
