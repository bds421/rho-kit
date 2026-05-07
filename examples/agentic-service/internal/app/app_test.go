package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/actionlog"
	actionlogmem "github.com/bds421/rho-kit/data/actionlog/memory"
)

func TestRun_StartsAndShutsDown(t *testing.T) {
	// Run with an immediately-cancelled ctx so ListenAndServe returns
	// quickly. Smoke test: the wiring compiles and survives a clean
	// shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx)
	require.NoError(t, err)
}

func TestMCPServer_EchoToolRoundtrip(t *testing.T) {
	alog := actionlog.New(actionlogmem.New(), actionlog.NewStaticSecrets("v1", map[string][]byte{
		"v1": []byte("at-least-32-bytes-of-secret-bytes!"),
	}))
	srv := newMCPServer(alog)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	srv.HTTP().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected result object, got %v", resp)
	assert.Equal(t, "hi", result["echoed"])
}

func TestMCPServer_RejectsValidationFailure(t *testing.T) {
	alog := actionlog.New(actionlogmem.New(), actionlog.NewStaticSecrets("v1", map[string][]byte{
		"v1": []byte("at-least-32-bytes-of-secret-bytes!"),
	}))
	srv := newMCPServer(alog)

	// Missing required `message` field.
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"echo","params":{},"id":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	srv.HTTP().ServeHTTP(rec, req)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "expected error object on validation failure, got %v", resp)
	// JSON-RPC -32602 = Invalid params; the kit maps validation
	// failures to that code so SDKs can branch cleanly.
	assert.Equal(t, float64(-32602), errObj["code"])
}

// Sanity: avoid unused-import lint when time is only referenced for
// the fixture clock-shift below. Keeps the test file compilable in
// isolation.
var _ = time.Second
