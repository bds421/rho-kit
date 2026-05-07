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
	// Default and conflict branches sanitise the error text to
	// avoid leaking infrastructure detail (security review M-1).
	// The full error must still appear in the server-side log.
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
	assert.Contains(t, logged, "boom: pq: relation",
		"server-side log must retain the full error for forensics")
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

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-9"})
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

	entries, err := logger.List(context.Background(), actionlog.Query{})
	require.NoError(t, err)
	assert.Empty(t, entries, "no audit entry should be written when tool was refused")
}

func TestServer_ActionLog_LooseMode_NoTenant_RunsToolAndSkipsAudit(t *testing.T) {
	// Loose mode preserves the legacy fail-open behaviour: log a
	// warning, skip the audit entry, run the tool. Operators must
	// opt in via WithStrictAudit(false).
	var logBuf bytes.Buffer
	slogger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logger, _ := newTestActionLogger(t)
	s := mcp.NewServer(
		mcp.WithLogger(slogger),
		mcp.WithActionLogger(logger),
		mcp.WithStrictAudit(false),
	)

	calls := 0
	require.NoError(t, mcp.Register[echoIn, echoOut](s, "echo", invokeCounterHandler(&calls)))

	resp := doRPC(t, s.HTTP(),
		`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`)

	require.Nil(t, resp["error"], "loose mode must let the call succeed: %v", resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, "hi", result["echoed"])
	assert.Equal(t, 1, calls, "tool must execute in loose mode")

	entries, err := logger.List(context.Background(), actionlog.Query{})
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

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-strict"})
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

func (l *asyncBlockingLogger) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, error) {
	return l.inner.List(ctx, q)
}

func (l *asyncBlockingLogger) Sign(e actionlog.Entry) (string, string, error) {
	return l.inner.Sign(e)
}

func (l *asyncBlockingLogger) Verify(e actionlog.Entry) error {
	return l.inner.Verify(e)
}

func TestServer_ActionLog_AsyncMode_RespondsBeforeAppend(t *testing.T) {
	// L-3 fix: WithAsyncAudit(true) spawns the audit append in a
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
		mcp.WithAsyncAudit(true),
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
	entriesEarly, err := innerLogger.List(context.Background(), actionlog.Query{TenantID: "tenant-async"})
	require.NoError(t, err)
	assert.Empty(t, entriesEarly, "async append must not yet have written when response is returned")

	// Release the audit, wait for the goroutine to land.
	close(blocking.release)
	wg.Wait()

	entriesLate, err := innerLogger.List(context.Background(), actionlog.Query{TenantID: "tenant-async"})
	require.NoError(t, err)
	require.Len(t, entriesLate, 1, "async append must eventually write the entry")
	assert.Equal(t, "mcp.echo", entriesLate[0].Action)
}

func TestServer_ActionLog_SyncMode_AppendBeforeResponse(t *testing.T) {
	// Sync mode (the default) writes the audit entry before the
	// JSON-RPC response — the entry is visible the moment
	// ServeHTTP returns.
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger))

	h := withTenantHandler(s.HTTP(), "tenant-sync")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-sync"})
	require.NoError(t, err)
	require.Len(t, entries, 1, "sync mode writes the entry before returning the response")
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
		"server-side log retains the original decoder error for forensics")
}

func TestDefaultActorExtractor_NoLongerTrustsHeader(t *testing.T) {
	// H-7 fix: the default actor extractor must NOT read X-Actor-Id —
	// any caller can set the header and forge the audit trail. The
	// recorded actor must be AnonymousActor regardless of what the
	// caller sends.
	logger, _ := newTestActionLogger(t)
	s := newTestServer(t, mcp.WithActionLogger(logger))

	h := withTenantHandler(s.HTTP(), "tenant-h7")
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	r.Header.Set("X-Actor-Id", "alice")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-h7"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, mcp.AnonymousActor, entries[0].Actor,
		"default extractor must not trust X-Actor-Id; recorded actor must be the anonymous sentinel")
	assert.NotEqual(t, "alice", entries[0].Actor)
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

	entries, err := logger.List(context.Background(), actionlog.Query{TenantID: "tenant-h7-ctx"})
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

	entries, err := logger.List(context.Background(), actionlog.Query{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "fixed-actor", entries[0].Actor)
}
