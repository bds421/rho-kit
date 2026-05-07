// Package mcp exposes typed kit handlers as Model Context Protocol
// (MCP) tools over a JSON-RPC 2.0 endpoint.
//
// # Why this exists
//
// Agentic services increasingly need to expose their REST handlers
// to external agents through MCP. Without a kit-supplied bridge,
// every team rebuilds the JSON-RPC + schema-generation + audit-log
// glue and gets the security model wrong (skipped tenant isolation,
// re-implemented validation, no per-tool action log).
//
// This package projects the kit's typed handler shape onto the MCP
// wire format. The [Server] is just an [http.Handler] — wrap it with
// the same middleware stack you already use for the REST mux
// (auth, tenant, idempotency, rate limit, audit) and the security
// model agrees with the rest of the service.
//
// # JSON-RPC surface
//
// The [Server] implements three JSON-RPC methods of the MCP protocol:
//
//   - "initialize" — returns server capabilities (tools only).
//   - "tools/list" — returns the registered tool catalog.
//   - "tools/call" — dispatches to a registered handler.
//
// Method names without a slash (e.g. "echo") are treated as a direct
// tool invocation — equivalent to "tools/call" with the name carried
// in the method field. This shorthand makes the endpoint usable from
// minimal JSON-RPC clients that don't understand the MCP envelope.
//
// # Schema generation
//
// Tool input/output schemas are generated from the Go types via
// reflection, honouring `json:"..."` tags for field names and
// `validate:"..."` tags for the `required` array. See [GenerateSchema]
// for the exhaustive type-mapping table.
//
// Cycle detection is performed at registration time, not at request
// time — a self-referential input struct produces an explicit error
// from [Register] rather than a runtime stack overflow.
//
// # Action-log integration
//
// When [WithActionLogger] is supplied, every tool call writes an
// [actionlog.Entry]:
//
//   - Outcome=success after a successful invocation.
//   - Outcome=failure when the handler returns an error.
//   - Reason carries the error message on failure (truncated to a
//     safe length so a verbose error doesn't blow up the audit row).
//
// Tenant comes from [tenant.FromContext]; actor comes from the
// configured [WithActorExtractor] (default: the X-Actor-Id header).
// When no tenant is on the context the entry is skipped (the
// signed-store contract rejects empty tenant ids); the [Server]
// emits a warn-level log so operators can spot unscoped tool calls
// in production.
//
// # What is NOT done here
//
//   - Auth, rate-limit, idempotency, CSRF: delegated to the
//     surrounding middleware chain. The Server itself has no opinion.
//   - Streaming tools / partial results: deferred. The current
//     transport returns one response per request. A future revision
//     may add Server-Sent Events for long-running tools.
//   - JSON-RPC batch requests (an array of calls in a single payload):
//     deferred. Single-request semantics keep the action-log entry
//     per-call rather than per-batch, which is what forensics
//     actually wants.
package mcp
