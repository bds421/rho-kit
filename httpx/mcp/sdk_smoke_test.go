package mcp

// Smoke test for the modelcontextprotocol/go-sdk dependency added in
// wave 120. The SDK is required so that wave 121 can migrate the kit's
// MCP transport implementation onto mcp.Server / AddTool /
// StreamableHTTPHandler without further dependency plumbing. This file
// only references one symbol to ensure the module resolves
// end-to-end; it intentionally exercises no behaviour.

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sdkSmokeProtocolVersion captures a symbol from the SDK so the import
// is not optimised out. The value is unused; it exists purely to keep
// the dependency wired in.
var sdkSmokeProtocolVersion = sdkmcp.LatestProtocolVersion
