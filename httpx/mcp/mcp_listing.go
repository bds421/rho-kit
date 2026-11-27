package mcp

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Annotations are the hints a client reads in tools/list (MCP
// ToolAnnotations) besides the destructive hint, which [WithDestructive]
// sets. Given with [WithAnnotations], every hint is sent explicitly, the
// false ones too, since the specification's defaults for an absent
// destructive or open-world hint are true.
type Annotations struct {
	// Title is the tool's display name, sent as the tool's title and the
	// annotations' title.
	Title string
	// ReadOnly: the tool changes nothing.
	ReadOnly bool
	// Idempotent: calling it again with the same arguments changes
	// nothing more.
	Idempotent bool
	// OpenWorld: the tool reaches entities outside the service (a web
	// search, an outgoing webhook).
	OpenWorld bool
}

// WithAnnotations sets the tool's title and hints.
func WithAnnotations(a Annotations) ToolOption {
	return func(c *toolConfig) {
		annotations := a
		c.annotations = &annotations
	}
}

// sdkAnnotations are what tools/list says of the tool.
func sdkAnnotations(a *Annotations, destructive bool) *sdkmcp.ToolAnnotations {
	if a == nil {
		if !destructive {
			return nil
		}
		return &sdkmcp.ToolAnnotations{DestructiveHint: &destructive}
	}
	openWorld := a.OpenWorld
	return &sdkmcp.ToolAnnotations{
		Title: a.Title, ReadOnlyHint: a.ReadOnly, DestructiveHint: &destructive,
		IdempotentHint: a.Idempotent, OpenWorldHint: &openWorld,
	}
}

// ToolFilter reports whether tools/list shows the tool named name to the
// caller of ctx (the request context, with what the auth middleware put
// on it).
type ToolFilter func(ctx context.Context, name string) bool

// WithToolFilter lists to each caller only the tools filter keeps, such
// as those its key may call. It shapes the listing only: tools/call still
// dispatches a tool the caller was not shown, so the handler (or what it
// calls) must still refuse what the caller may not do.
func WithToolFilter(filter ToolFilter) ServerOption {
	if filter == nil {
		panic("mcp: WithToolFilter requires a filter")
	}
	return func(c *serverConfig) { c.toolFilter = filter }
}

// filterListing is the receiving middleware that applies the ToolFilter
// to every tools/list answer.
func filterListing(filter ToolFilter) sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			res, err := next(ctx, method, req)
			list, ok := res.(*sdkmcp.ListToolsResult)
			if err != nil || method != "tools/list" || !ok {
				return res, err
			}
			kept := make([]*sdkmcp.Tool, 0, len(list.Tools))
			for _, tool := range list.Tools {
				if filter(ctx, tool.Name) {
					kept = append(kept, tool)
				}
			}
			filtered := *list
			filtered.Tools = kept
			return &filtered, nil
		}
	}
}

// WithMaxRequestBytes bounds the bytes read of one request; a larger one
// is refused with 413 Request Entity Too Large. Zero keeps the SDK's
// default (sdkmcp.DefaultMaxRequestBodyBytes, 4 MiB); raise it for tools
// that take files, and bound the body before the endpoint as well.
func WithMaxRequestBytes(n int64) ServerOption {
	if n < 0 {
		panic("mcp: WithMaxRequestBytes requires zero or more bytes")
	}
	return func(c *serverConfig) { c.maxRequestBytes = n }
}

func title(cfg toolConfig) string {
	if cfg.annotations == nil {
		return ""
	}
	return cfg.annotations.Title
}
