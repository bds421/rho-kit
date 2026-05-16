package mcp

// Smoke test for the modelcontextprotocol/go-sdk dependency added in
// wave 120. The SDK is required so that wave 121 can migrate the kit's
// MCP transport implementation onto mcp.Server / AddTool /
// StreamableHTTPHandler without further dependency plumbing. This file
// only references one symbol to ensure the module resolves
// end-to-end; it intentionally exercises no behaviour.

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSDKSmokeImport asserts that the modelcontextprotocol/go-sdk
// module is wired into the build. It instantiates a typed pointer
// from the SDK so the import is not optimised out and the symbol is
// reachable. Wave 121 replaces this with real usage of mcp.Server /
// AddTool / StreamableHTTPHandler.
func TestSDKSmokeImport(t *testing.T) {
	var server *sdkmcp.Server
	if server != nil {
		t.Fatalf("expected nil zero-value server pointer, got %v", server)
	}
}
