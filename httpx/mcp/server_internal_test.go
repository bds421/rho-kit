package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONRPCResult_MarshalErrorDoesNotLeakRawDetails(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSONRPCResult(rr, json.RawMessage(`1`), map[string]any{
		"secret": make(chan int),
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, leak := range []string{"unsupported type", "chan", "secret", "marshal result"} {
		if strings.Contains(body, leak) {
			t.Fatalf("response leaked %q in %q", leak, body)
		}
	}

	var resp jsonRPCResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error")
	}
	if resp.Error.Code != rpcErrInternalError {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, rpcErrInternalError)
	}
	if resp.Error.Message != "internal error" {
		t.Fatalf("error message = %q, want internal error", resp.Error.Message)
	}
}
