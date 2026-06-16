package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	actionlogmemory "github.com/bds421/rho-kit/data/v2/actionlog/memory"
	"github.com/bds421/rho-kit/httpx/v2/mcp"
)

type echoIn struct {
	Message string `json:"message" jsonschema:"required"`
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

// rpcResponse is the parsed JSON-RPC envelope returned by the SDK
// Streamable HTTP handler.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// toolResult is the decoded shape of a `tools/call` result envelope.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// doRPC POSTs a JSON-RPC envelope through the MCP server's Streamable
// HTTP handler with the SDK-required Accept and Content-Type headers.
func doRPC(t *testing.T, h http.Handler, body string) rpcResponse {
	t.Helper()
	return doRPCRequest(t, h, body, nil)
}

// doRPCRequest is the explicit form of doRPC for tests that need to
// stamp extra headers or override request fields.
func doRPCRequest(t *testing.T, h http.Handler, body string, setup func(*http.Request)) rpcResponse {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if setup != nil {
		setup(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp rpcResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "decode response: %s", w.Body.String())
	return resp
}

// callTool sends a tools/call request and decodes the result. Returns
// the inner CallToolResult envelope and whether the JSON-RPC response
// itself carried an `error` member (a protocol-level error rather than
// a tool error).
func callTool(t *testing.T, h http.Handler, name string, args map[string]any, setup func(*http.Request)) (toolResult, *rpcResponse) {
	t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	envelope, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  params,
	})
	require.NoError(t, err)
	resp := doRPCRequest(t, h, string(envelope), setup)
	if resp.Error != nil {
		return toolResult{}, &resp
	}
	var out toolResult
	require.NoError(t, json.Unmarshal(resp.Result, &out), "decode tools/call result: %s", string(resp.Result))
	return out, &resp
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

func TestServer_RoundTrip_ToolsCall(t *testing.T) {
	s := newTestServer(t)
	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error, "unexpected JSON-RPC error: %+v", rpc.Error)
	require.False(t, res.IsError, "successful call must not have IsError set")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "text", res.Content[0].Type)
	assert.Contains(t, res.Content[0].Text, `"echoed":"hi"`)

	var structured map[string]any
	require.NoError(t, json.Unmarshal(res.StructuredContent, &structured))
	assert.Equal(t, "hi", structured["echoed"])
}

func TestServer_ToolsList_ReturnsRegisteredCatalog(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "z-tool", echoHandler))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "a-tool", echoHandler))

	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
	require.Nil(t, resp.Error)
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	names := make([]string, 0, len(result.Tools))
	for _, tt := range result.Tools {
		names = append(names, tt.Name)
	}
	assert.Contains(t, names, "a-tool")
	assert.Contains(t, names, "z-tool")
}

func TestServer_Tools_DeterministicallySorted(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "z-tool", echoHandler))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "a-tool", echoHandler))
	tools := s.Tools()
	require.Len(t, tools, 2)
	assert.Equal(t, "a-tool", tools[0].Name)
	assert.Equal(t, "z-tool", tools[1].Name)
}

func TestServer_ValidationFailure_RecordedAsToolError(t *testing.T) {
	// Missing required field surfaces as a CallToolResult with
	// IsError=true, not a JSON-RPC -32602 protocol error.
	s := newTestServer(t)
	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "validation failure must surface as tool error")
	require.Len(t, res.Content, 1)
}

func TestServer_HandlerValidationError_DoesNotLeakSecretText(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "validate", func(context.Context, echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewValidation("secret-token is invalid")
	}))

	res, rpc := callTool(t, s.HTTP(), "validate", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.NotContains(t, res.Content[0].Text, "secret-token")
	assert.Equal(t, "invalid request", res.Content[0].Text)
}

func TestServer_NotFoundError_DoesNotLeakSecretText(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "lookup", func(context.Context, echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewNotFound("secret-token-entity", "secret-token-id")
	}))

	res, rpc := callTool(t, s.HTTP(), "lookup", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.NotContains(t, res.Content[0].Text, "secret-token")
	assert.Equal(t, "resource not found", res.Content[0].Text)
}

func TestServer_DecodeFailure_DoesNotLeakArgumentValue(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[timeIn, echoOut](s, "time", func(context.Context, timeIn) (echoOut, error) {
		return echoOut{}, nil
	}))

	res, rpc := callTool(t, s.HTTP(), "time", map[string]any{"at": "secret-token"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "decode failure must surface as tool error")
	require.Len(t, res.Content, 1)
	assert.NotContains(t, res.Content[0].Text, "secret-token")
}

func TestServer_DecodeFailure_RecordsAuditEntry(t *testing.T) {
	// Argument-decode failures must produce a failure audit entry, the same
	// as validation and destructive-gate refusals, so operators reviewing the
	// audit log see malformed-argument probes — not just schema-validation
	// probes — against a tool.
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithActionLogger(logger),
		withTestActor("agent-decode"),
	)
	require.NoError(t, mcp.Register[timeIn, echoOut](s, "time", func(context.Context, timeIn) (echoOut, error) {
		return echoOut{}, nil
	}))

	h := withTenantHandler(s.HTTP(), "tenant-decode")
	res, rpc := callTool(t, h, "time", map[string]any{"at": "not-a-date"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "decode failure must surface as a tool error")

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-decode"})
	require.NoError(t, err)
	require.Len(t, entries, 1, "decode failure must be audited")
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome)
	// The raw argument value must NOT leak into the audit reason.
	assert.NotContains(t, entries[0].Reason, "not-a-date")
}

func TestServer_UnknownTool_ReturnsErrorResponse(t *testing.T) {
	// Pre-SDK kits scrubbed the tool name from the "method not
	// found" message. The SDK ships the spec-compliant form
	// (`unknown tool "<name>"`) and we surface it verbatim — the
	// tool name is not a kit-controlled secret, so accepting the
	// spec-compliant text is the v2.0.0 trade-off. This regression
	// only asserts the error surfaces.
	s := newTestServer(t)
	res, rpc := callTool(t, s.HTTP(), "unknown-tool-xyz", map[string]any{}, nil)
	// Whichever envelope the SDK chooses for an unknown tool, exactly one of
	// the two error channels must signal failure. Asserting the disjunction
	// (rather than returning early on the rpc.Error branch) catches a
	// regression where unknown tools start returning a success envelope.
	require.True(t, rpc.Error != nil || res.IsError,
		"unknown tool must produce an error response (rpc.Error or res.IsError)")
}

func TestServer_HandlerInternalError_MaskedToCallerAndLoggedServerSide(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	s := mcp.NewServer(mcp.WithLogger(logger))
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewOperationFailed("boom: pq: relation \"x\" does not exist")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	res, rpc := callTool(t, s.HTTP(), "fail", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "internal error", res.Content[0].Text,
		"default branch must not leak raw error text to MCP caller")

	logged := logBuf.String()
	assert.Contains(t, logged, "<redacted error")
	assert.NotContains(t, logged, "boom: pq: relation")
}

func TestServer_HandlerPanic_RecoveredAndMaskedToCaller(t *testing.T) {
	// The SDK dispatch path has no recover(); a panicking handler would
	// otherwise unwind to net/http (crashing the server) and skip the
	// audit append. wrapToolHandler must recover, mask the caller, and
	// log server-side without leaking the panic value.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	s := mcp.NewServer(mcp.WithLogger(logger))
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		panic("handler exploded: secret=hunter2")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	res, rpc := callTool(t, s.HTTP(), "fail", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "a panicking handler must surface as a tool error, not crash")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "internal error", res.Content[0].Text)

	logged := logBuf.String()
	assert.Contains(t, logged, "panic")
	assert.NotContains(t, logged, "hunter2",
		"the panic value must not leak to the caller-visible content")
}

func TestServer_HandlerPanic_StrictAudit_WritesFailureEntry(t *testing.T) {
	// The strict-audit invariant: every executed tool call produces a
	// signed entry. A panicking handler still "executed" (side effects
	// may have occurred), so the recovered panic must be audited as a
	// failure.
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		panic("handler exploded")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	h := withTenantHandler(s.HTTP(), "tenant-panic")
	res, rpc := callTool(t, h, "fail", map[string]any{"message": "x"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "agent-7") })
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-panic"})
	require.NoError(t, err)
	require.Len(t, entries, 1, "a recovered handler panic must still produce an audit entry")
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome)
	assert.NotEmpty(t, entries[0].Reason)
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

func TestServer_RejectsNonObjectInputType(t *testing.T) {
	// SDK AddTool panics unless the input schema's "type" is exactly
	// "object". validate.SchemaFor emits a non-object schema for scalar/
	// slice/time.Time/json.RawMessage In types; Register must surface a
	// clean error (and leave the catalog unchanged) rather than panic
	// after reserving the slot.
	s := mcp.NewServer()
	err := mcp.Register[string, echoOut](s, "scalar-tool", func(_ context.Context, _ string) (echoOut, error) {
		return echoOut{}, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema")
	assert.NotContains(t, err.Error(), "scalar-tool",
		"registration error must not reflect the tool name")
	assert.Empty(t, s.Tools(), "failed registration must not leave a phantom catalog entry")
	// The slot must be free: a follow-up registration with the same name
	// and a valid type must succeed.
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "scalar-tool", echoHandler))
}

func TestServer_RejectsNonObjectOutputType(t *testing.T) {
	s := mcp.NewServer()
	err := mcp.Register[echoIn, string](s, "scalar-out-tool", func(_ context.Context, _ echoIn) (string, error) {
		return "", nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema")
	assert.Empty(t, s.Tools(), "failed registration must not leave a phantom catalog entry")
}

func TestServer_RejectsRawMessageInputType(t *testing.T) {
	// json.RawMessage infers the permissive empty schema ({}), which has
	// no "type" key and would also panic in AddTool.
	s := mcp.NewServer()
	err := mcp.Register[json.RawMessage, echoOut](s, "raw-tool", func(_ context.Context, _ json.RawMessage) (echoOut, error) {
		return echoOut{}, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema")
	assert.Empty(t, s.Tools())
}

func TestRegister_RejectsTypelessSchemaOverrides(t *testing.T) {
	// validateSchemaOverride accepts overrides lacking a "type" key (or
	// with a non-string "type"); both panic in AddTool. Register must
	// reject them up front.
	tests := []struct {
		name string
		opt  mcp.ToolOption
	}{
		{name: "input no type", opt: mcp.WithInputSchema(json.RawMessage(`{}`))},
		{name: "input non-string type", opt: mcp.WithInputSchema(json.RawMessage(`{"type":123}`))},
		{name: "output no type", opt: mcp.WithOutputSchema(json.RawMessage(`{"title":"x"}`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mcp.NewServer()
			err := mcp.Register[echoIn, echoOut](s, "secret-token-tool", echoHandler, tt.opt)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "schema")
			assert.NotContains(t, err.Error(), "secret-token-tool")
			assert.Empty(t, s.Tools())
		})
	}
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

func TestServer_Initialize_AdvertisesToolsCapability(t *testing.T) {
	s := newTestServer(t)
	resp := doRPC(t, s.HTTP(),
		`{"jsonrpc":"2.0","id":11,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v0"}}}`)
	require.Nil(t, resp.Error)
	var result struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	_, hasTools := result.Capabilities["tools"]
	assert.True(t, hasTools, "initialize must advertise the tools capability: %s", string(resp.Result))
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

func TestServer_DestructiveFlag_RegistersVendorExtensionAndAnnotation(t *testing.T) {
	s := mcp.NewServer(mcp.WithoutDestructiveGate())
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(),
	))
	tools := s.Tools()
	require.Len(t, tools, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tools[0].InputSchema, &schema))
	assert.Equal(t, true, schema["x-destructive"],
		"destructive flag must surface as the kit vendor extension on the input schema")
}

func TestServer_DestructiveToolRefusedWithoutGate(t *testing.T) {
	s := mcp.NewServer()
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "delete", echoHandler,
		mcp.WithDestructive(),
	))
	res, rpc := callTool(t, s.HTTP(), "delete", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "destructive tool with no gate must error, got: %+v", res)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].Text, "destructive tool not configured")
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
	res, rpc := callTool(t, s.HTTP(), "delete", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	assert.False(t, res.IsError, "approved destructive call must succeed")
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
	res, rpc := callTool(t, s.HTTP(), "delete", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "destructive call refused", res.Content[0].Text)
}

func TestServer_NonDestructiveToolBypassesGate(t *testing.T) {
	called := 0
	gate := func(context.Context, string, []byte) error {
		called++
		return errors.New("should not be called for non-destructive")
	}
	s := mcp.NewServer(mcp.WithDestructiveGate(gate))
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))
	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	assert.False(t, res.IsError)
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

func TestServer_AcceptHeader_Required(t *testing.T) {
	// The SDK enforces the spec: clients must send
	// `Accept: application/json, text/event-stream`. Missing or wrong
	// Accept yields 400 Bad Request.
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	s.HTTP().ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"SDK must reject requests without the streamable Accept header: body=%s", w.Body.String())
}

func TestServer_ContentTypeHeader_Required(t *testing.T) {
	s := newTestServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	s.HTTP().ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code,
		"SDK must reject non-JSON Content-Type: body=%s", w.Body.String())
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
	s := newTestServer(t, mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))

	h := withTenantHandler(s.HTTP(), "tenant-123")
	res, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "agent-7") })
	require.Nil(t, rpc.Error)
	require.False(t, res.IsError)

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
	_, _ = callTool(t, h, "fail", map[string]any{"message": "x"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "agent-bad") })

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-9"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome)
	assert.Equal(t, "kaboom", entries[0].Reason)
}

func TestServer_ActionLog_FailureReasonWithInvalidBytes_StillRecorded(t *testing.T) {
	// A handler error embedding NUL or invalid-UTF-8 bytes (e.g. echoing
	// raw caller input) must not poison the audit append: the signed
	// store rejects such reasons, so the kit must sanitise the reason
	// before building the entry. Otherwise strict sync mode loses the
	// failure entry AND swaps the mapped caller message for a bare
	// "internal error".
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, apperror.NewValidation("bad arg \x00\xff\xfe value")
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "fail", boom))

	h := withTenantHandler(s.HTTP(), "tenant-9")
	res, rpc := callTool(t, h, "fail", map[string]any{"message": "x"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "agent-bad") })
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	// The validation error must still map to the caller-facing message,
	// not be masked as "internal error" by an audit-append failure.
	assert.Equal(t, "invalid request", res.Content[0].Text,
		"audit sanitisation must not change the caller-facing error")

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-9"})
	require.NoError(t, err)
	require.Len(t, entries, 1, "executed tool call must still produce an audit entry")
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome)
	assert.NotContains(t, entries[0].Reason, "\x00", "sanitised reason must not contain NUL")
	assert.True(t, utf8.ValidString(entries[0].Reason), "sanitised reason must be valid UTF-8")
}

func invokeCounterHandler(counter *int) mcp.Handler[echoIn, echoOut] {
	return func(_ context.Context, in echoIn) (echoOut, error) {
		*counter++
		return echoOut{Echoed: in.Message}, nil
	}
}

func TestServer_ActionLog_StrictMode_NoTenant_RefusesDispatch(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger))

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "strict mode must refuse dispatch as a tool error")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "internal error", res.Content[0].Text)

	assert.Equal(t, 0, calls, "tool MUST NOT execute when strict-mode audit cannot be attributed")

	entries, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Empty(t, entries)
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

	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError)
	assert.Equal(t, 0, calls, "tool must not execute when strict audit tenant extraction panics")
}

func TestServer_ActionLog_LooseMode_NoTenant_RunsToolAndSkipsAudit(t *testing.T) {
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

	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.False(t, res.IsError, "loose mode must let the call succeed")
	var structured map[string]any
	require.NoError(t, json.Unmarshal(res.StructuredContent, &structured))
	assert.Equal(t, "hi", structured["echoed"])
	assert.Equal(t, 1, calls, "tool must execute in loose mode")

	entries, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Empty(t, entries)

	assert.Contains(t, logBuf.String(), "skipping action log entry")
}

func TestServer_ActionLog_StrictMode_WithTenant_WritesEntry(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger), mcp.WithActorFromHeader("X-Actor-Id"))

	h := withTenantHandler(s.HTTP(), "tenant-strict")
	_, _ = callTool(t, h, "echo", map[string]any{"message": "hi"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "agent-strict") })

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-strict"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "agent-strict", entries[0].Actor)
}

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
	res, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.False(t, res.IsError)

	entriesEarly, _, err := innerLogger.List(context.Background(), actionlog.Query{TenantID: "tenant-async"})
	require.NoError(t, err)
	assert.Empty(t, entriesEarly, "async append must not yet have written when response is returned")

	close(blocking.release)
	wg.Wait()

	entriesLate, _, err := innerLogger.List(context.Background(), actionlog.Query{TenantID: "tenant-async"})
	require.NoError(t, err)
	require.Len(t, entriesLate, 1)
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

	// Dispatch with a LIVE request context that carries a trace value but is
	// cancelled the instant the response is written. The async audit worker
	// must observe the value (propagated) yet a NON-cancelled context, because
	// asyncAuditContext detaches cancellation via context.WithoutCancel. The
	// SDK only sees a live context at dispatch time, so the audit job is
	// guaranteed to enqueue — making the assertions below unconditional.
	parent := context.WithValue(context.Background(), auditContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"}}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	withTenantHandler(s.HTTP(), "tenant-async-context").ServeHTTP(w, r)
	// Cancel after the handler returned but before the async worker drains, so
	// any naive context inheritance would surface as a cancelled audit context.
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, s.Stop(stopCtx))

	// Unconditional: the job MUST have enqueued and the worker context MUST
	// preserve the value while dropping the cancellation.
	require.Equal(t, "trace-123", logger.value,
		"async audit context must preserve request values via WithoutCancel")
	assert.NoError(t, logger.ctxErr,
		"async audit context must not inherit the request's cancellation")
}

func TestServer_ActionLog_SyncMode_AppendBeforeResponse(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger), withTestActor("agent-sync"))

	h := withTenantHandler(s.HTTP(), "tenant-sync")
	_, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-sync"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestDefaultActorExtractor_StrictAuditRefusesAnonymousDespiteHeader(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger))

	h := withTenantHandler(s.HTTP(), "tenant-h7")
	res, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "alice") })
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "strict audit must reject missing actor attribution")

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
	_, _ = callTool(t, h, "echo", map[string]any{"message": "hi"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "spoofed") })

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-anon"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, mcp.AnonymousActor, entries[0].Actor)
	assert.NotEqual(t, "spoofed", entries[0].Actor)
}

func TestWithActorFromContext_ReadsAuthContext(t *testing.T) {
	type userIDKey struct{}
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t,
		mcp.WithActionLogger(logger),
		mcp.WithActorFromContext(func(ctx context.Context) string {
			v, _ := ctx.Value(userIDKey{}).(string)
			return v
		}),
	)

	h := withTenantHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userIDKey{}, "verified-bob")
		s.HTTP().ServeHTTP(w, r.WithContext(ctx))
	}), "tenant-h7-ctx")

	_, _ = callTool(t, h, "echo", map[string]any{"message": "hi"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "spoofed") })

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
	_, _ = callTool(t, h, "echo", map[string]any{"message": "hi"},
		func(r *http.Request) { r.Header.Set("X-Actor-Id", "ignored") })

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
	res, _ := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	require.True(t, res.IsError)

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
			res, _ := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
			require.True(t, res.IsError)

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
			res, _ := callTool(t, h, "echo", map[string]any{"message": "hi"}, tt.setup)
			require.True(t, res.IsError)

			entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-actor-header"})
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

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
	inner, _ := newTestActionLogger(t)
	logger := &failingLogger{inner: inner}
	s := mcp.NewServer(mcp.WithActionLogger(logger), withTestActor("agent-fail"))

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	h := withTenantHandler(s.HTTP(), "tenant-fail")
	res, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "strict-mode append failure must surface as tool error")

	assert.EqualValues(t, 1, atomic.LoadInt64(&logger.appends))
	assert.Equal(t, 1, calls, "tool ran before the audit append failed (documented ordering)")
}

func TestServer_ActionLog_LooseMode_AppendFailure_StillReturnsResult(t *testing.T) {
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
	res, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)
	require.False(t, res.IsError, "loose mode must still return success on append failure")
	var structured map[string]any
	require.NoError(t, json.Unmarshal(res.StructuredContent, &structured))
	assert.Equal(t, "hi", structured["echoed"])
	assert.Contains(t, logBuf.String(), "action log append failed")
}

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
		res, _ := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
		require.False(t, res.IsError, "async mode must keep responding while queue saturates")
	}

	dropped := s.AsyncAuditDropped()
	assert.Greater(t, dropped, int64(0), "saturated queue must drop entries rather than leak goroutines")
	assert.Less(t, dropped, int64(N))

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
		_, _ = callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(stopCtx))

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-drain"})
	require.NoError(t, err)
	assert.Len(t, entries, 4)
}

func TestServer_AsyncAudit_StopAfterTimeout_StillDrains(t *testing.T) {
	// A first Stop whose context times out while workers are still draining
	// must NOT mark the server as cleanly stopped: a retry with a fresh
	// context must re-wait on the worker drain rather than returning a false
	// success that races process exit against in-flight audit appends.
	innerLogger, store := newTestActionLogger(t)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	blocking := &asyncBlockingLogger{
		inner:   innerLogger,
		release: make(chan struct{}),
		wg:      wg,
	}
	s := mcp.NewServer(
		mcp.WithActionLogger(blocking),
		withTestActor("agent-stop-drain"),
		mcp.WithAsyncAuditDispatch(),
		mcp.WithAsyncAuditWorkers(1),
		mcp.WithAsyncAuditQueue(1),
	)
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", echoHandler))

	h := withTenantHandler(s.HTTP(), "tenant-stop-drain")
	_, rpc := callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
	require.Nil(t, rpc.Error)

	// First Stop: the worker is blocked on the logger, so the short context
	// must time out and report the error.
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer firstCancel()
	err := s.Stop(firstCtx)
	require.Error(t, err, "first Stop must time out while the worker is blocked")

	// Release the worker shortly AFTER the second Stop begins, so that a buggy
	// Stop (one that returns nil immediately on the already-stopped path) would
	// return before the append lands, whereas the correct Stop re-waits on the
	// drain and only returns once the append has completed.
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(blocking.release)
	}()
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()
	require.NoError(t, s.Stop(secondCtx), "retry Stop must wait for the drain to complete")

	// The second Stop returning successfully MUST imply the drain finished.
	// Check immediately (no wg.Wait first) so a premature return is caught.
	entries, _, listErr := store.List(context.Background(), actionlog.Query{TenantID: "tenant-stop-drain"})
	require.NoError(t, listErr)
	require.Len(t, entries, 1, "the audit append must have landed before the second Stop returned")
	wg.Wait()
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
			_, _ = callTool(t, h, "echo", map[string]any{"message": "hi"}, nil)
		}()
	}
	close(start)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(stopCtx))
	wg.Wait()

	entries, _, err := store.List(context.Background(), actionlog.Query{TenantID: "tenant-race"})
	require.NoError(t, err)
	dropped := s.AsyncAuditDropped() - droppedBefore
	assert.Equal(t, int64(N), int64(len(entries))+dropped,
		"every enqueue must either land in the store (%d) or be counted dropped (%d)",
		len(entries), dropped)
}

type unmarshalableOut struct {
	C chan int `json:"c"`
}

func TestServer_MarshalFailureAfterHandlerSuccess_RecordsAuditAsFailure(t *testing.T) {
	// Marshalling a chan-typed field fails at json.Marshal. The kit
	// must surface this as a tool error AND record the audit entry as
	// Outcome=failure — auditing "success" while the caller sees
	// "internal error" would break the audit invariant.
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger), withTestActor("agent-marshal"))
	// Bypass the schema check that would normally reject the output
	// type via an explicit output schema override.
	require.NoError(t, mcp.Register[echoIn, unmarshalableOut](s, "boom",
		func(_ context.Context, _ echoIn) (unmarshalableOut, error) {
			return unmarshalableOut{C: make(chan int)}, nil
		},
		mcp.WithOutputSchema(json.RawMessage(`{"type":"object"}`)),
	))

	h := withTenantHandler(s.HTTP(), "tenant-marshal")
	res, rpc := callTool(t, h, "boom", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	require.True(t, res.IsError, "marshal failure must surface as a tool error")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "internal error", res.Content[0].Text)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-marshal"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, actionlog.OutcomeFailure, entries[0].Outcome,
		"marshal failure must be recorded as a failure in the audit log, not a phantom success")
	assert.NotEmpty(t, entries[0].Reason, "audit entry must carry a non-empty reason on marshal failure")
}

func TestServer_ToolsCall_ReturnsMCPContentShape(t *testing.T) {
	s := newTestServer(t)
	res, rpc := callTool(t, s.HTTP(), "echo", map[string]any{"message": "world"}, nil)
	require.Nil(t, rpc.Error)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "text", res.Content[0].Type)
	assert.Contains(t, res.Content[0].Text, `"echoed":"world"`)
	var structured map[string]any
	require.NoError(t, json.Unmarshal(res.StructuredContent, &structured))
	assert.Equal(t, "world", structured["echoed"])
}

func TestServer_ShorthandCall_NoLongerSupported(t *testing.T) {
	// The pre-SDK kit treated `method: "<tool-name>"` as a shorthand
	// invocation. The SDK transport routes only the spec methods
	// (`tools/call`, `tools/list`, `initialize`, `ping`, ...). Sending
	// an unknown method is rejected at the transport layer (HTTP 400
	// from the SDK's StreamableHTTPHandler) — confirming the shorthand
	// surface is gone is enough for this regression check.
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"message":"world"}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	s.HTTP().ServeHTTP(w, r)
	assert.NotEqual(t, http.StatusOK, w.Code,
		"shorthand invocation must NOT succeed; got %d %s", w.Code, w.Body.String())
}

func TestTruncateReason_PreservesUTF8Boundaries(t *testing.T) {
	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(mcp.WithActionLogger(logger), withTestActor("agent-utf8"))

	long := strings.Repeat("★", 400)
	boom := func(_ context.Context, _ echoIn) (echoOut, error) {
		return echoOut{}, errors.New(long)
	}
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "boom", boom))

	h := withTenantHandler(s.HTTP(), "tenant-utf8")
	_, _ = callTool(t, h, "boom", map[string]any{"message": "x"}, nil)

	entries, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-utf8"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	reason := entries[0].Reason
	assert.LessOrEqual(t, len(reason), 1024+3)
	assert.True(t, strings.HasSuffix(reason, "..."))
	core := strings.TrimSuffix(reason, "...")
	for _, r := range core {
		assert.NotEqual(t, '�', r, "truncated reason must contain no replacement runes")
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

// ensure io is used for compile (used elsewhere via tests of failing
// readers, kept for symmetry should hostile-input tests be re-added).
var _ = io.EOF
