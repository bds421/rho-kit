# NEW: httpx/mcp — expose typed handlers as MCP tools over JSON-RPC

**Phase**: Theme 4 (Agentic surface)
**Status**: landed
**Module path**: `github.com/bds421/rho-kit/httpx/mcp`

## Why

Agentic services increasingly need to expose their REST handlers as
Model Context Protocol (MCP) tools so external agents can call them.
Without a kit-supplied bridge, every team rebuilds this and gets the
security model wrong: skipped tenant isolation, re-implemented
validation, no per-tool action log.

The kit's existing typed-JSON handler pattern + middleware stack
already provides everything an MCP server needs (auth, tenant,
idempotency, rate limit, audit log). This package projects them onto
the MCP wire format.

## Public API (summary)

```go
type Tool struct {
    Name         string
    Description  string
    InputSchema  json.RawMessage
    OutputSchema json.RawMessage
}

type Handler[In any, Out any] func(ctx context.Context, in In) (Out, error)

type Server struct { /* ... */ }

func NewServer(opts ...ServerOption) *Server
func Register[In any, Out any](s *Server, name string, h Handler[In, Out], opts ...ToolOption) error
func (s *Server) HTTP() http.Handler
func (s *Server) Tools() []Tool
```

`Server.HTTP()` returns an `http.Handler` that can be mounted on the
same mux as the REST API. Wrap it with the kit's existing middleware
chain (`httpx/middleware/tenant`, `httpx/middleware/ratelimit`,
`httpx/middleware/budget`, `httpx/middleware/approval`, etc.) — the
JSON-RPC endpoint reuses every existing security primitive without
re-implementation.

## Schema generation

`GenerateSchema(reflect.Type)` walks Go types and emits JSON-Schema:

| Go type | JSON-Schema |
| --- | --- |
| `string` | `{"type":"string"}` |
| `bool` | `{"type":"boolean"}` |
| signed/unsigned ints | `{"type":"integer"}` |
| floats | `{"type":"number"}` |
| `time.Time` | `{"type":"string","format":"date-time"}` |
| `[]T` | `{"type":"array","items":<T>}` |
| `map[string]T` | `{"type":"object","additionalProperties":<T>}` |
| `*T` | schema of `T` (no pointer-ness in JSON) |
| struct | `{"type":"object","properties":...,"required":[...]}` |
| `interface{}` | `{}` (any value) |
| `json.RawMessage` / `[]byte` | `{"type":"string"}` |

`required` is populated from `validate:"required"` tags so it agrees
with the kit's runtime validation. Field names follow `json:"..."`
tags. A `desc:"..."` tag becomes the field's `description`.

Cycles are detected at registration time via a visited-types set;
`Register` returns `ErrCyclicSchema` rather than emitting an
unbounded schema.

## JSON-RPC surface

The endpoint implements the MCP-required JSON-RPC methods:

- `initialize` — server capabilities (tools only).
- `tools/list` — sorted tool catalog.
- `tools/call` — invoke a registered tool by name with arguments.
- `<tool-name>` — shorthand: invoke directly. Equivalent to
  `tools/call` with name in the method field.

Validation errors surface as **`-32602 Invalid params`** rather than
as a 500. Unknown tools return `-32601 Method not found`. Auth /
forbidden errors are mapped to `-32601` to avoid revealing the tool
catalog to unauthenticated callers.

## Action-log integration

Wire `WithActionLogger(l)` to record one `actionlog.Entry` per call:

- `Outcome=success` after a clean return.
- `Outcome=failure` with truncated `Reason` on any handler error.
- `Action="mcp.<tool-name>"`.
- `Actor` from `WithActorExtractor(...)` (default: `X-Actor-Id`
  header, `"anonymous"` if missing).
- `TenantID` from `tenant.FromContext`. When no tenant is on
  context the entry is skipped (the signed-store contract rejects
  empty tenant ids); the server emits a `warn` log.

This makes "what did the agent do this hour against tenant X" a SQL
query rather than a log-grep.

## Definition of done

- [x] `httpx/mcp` (top-level package: Server, Register, schema gen,
      JSON-RPC routing, action-log integration).
- [x] Validation failures surface as `-32602 Invalid params`.
- [x] Cycle detection at registration time with explicit error.
- [x] `kit-new` `--mcp` flag scaffolds a sample tool registration
      (see commit on `cmd/kit-new`).
- [ ] Builder integration (Theme 4 sweep — separate PR).
- [ ] Recipe in `docs/ai/`.

## Trade-offs

- **JSON-RPC batch requests**: deferred. Single-call semantics keep
  the action-log entry per-call rather than per-batch — forensics
  reads are cleaner. A future revision may add batch support behind
  an opt-in option.
- **Streaming tools**: deferred. The current transport returns one
  response per request. Long-running tools should be modelled as
  job-launch + poll-status pairs; a future revision may add
  Server-Sent Events for bona-fide streaming use cases.
- **MCP `content` wrapper**: the `tools/call` result is the tool's
  raw response, not the MCP-canonical `{content: [...]}` envelope.
  SDK consumers who already typed-out the tool's `Out` struct get it
  untouched. A future revision may add `WithMCPContentWrapping(true)`
  for clients that require the wrapper.
- **Vendor extensions**: `WithDestructive(true)` emits
  `x-destructive: true` at the schema's top level. The flag is
  metadata only — actual gating uses
  `httpx/middleware/approval` around the Server.
