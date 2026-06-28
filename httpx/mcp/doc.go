// Package mcp exposes typed kit handlers as Model Context Protocol
// (MCP) tools over a Streamable HTTP endpoint.
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
// wire format. The transport is owned by the official Go SDK
// ([github.com/modelcontextprotocol/go-sdk/mcp]); the kit's value-add
// is the [Register] typed registration generic, the strict tenant +
// actor audit invariant, the destructive-tool gate, and the
// action-log integration.
//
// # Wire format
//
// The [Server.HTTP] handler is the SDK's Streamable HTTP transport
// configured for stateless + JSON-response mode. Each request is a
// JSON-RPC envelope (a single object) carrying one of the standard
// MCP methods (`initialize`, `tools/list`, `tools/call`, `ping`,
// etc.). Clients MUST send:
//
//   - `Content-Type: application/json` on POST,
//   - `Accept: application/json, text/event-stream` on every request
//     (the SDK rejects requests that lack either media type with
//     400 Bad Request).
//
// JSON-RPC notifications, batch requests, and the legacy kit
// "shorthand" invocation (`method: "<tool-name>"`) are no longer
// supported — clients must use `tools/call`.
//
// # Schema generation
//
// Tool input/output schemas are generated from the Go types via the
// kit's [core/v2/validate] package, which reads `jsonschema:"..."`
// struct tags. The result is a JSON-Schema 2020-12 document; the SDK
// validates inputs against it before invoking the kit's wrapper, and
// the kit then runs [validate.Struct] for field-level validation on
// top.
//
// Cycle detection is performed at registration time, not at request
// time — a self-referential input struct produces [ErrCyclicSchema]
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
// configured [WithActorExtractor]. The default actor is
// [AnonymousActor], but strict audit mode rejects that anonymous
// default when [WithActionLogger] is configured unless the caller
// explicitly opts in with [WithAllowAnonymousActor]. The Server does
// NOT trust any request header by default; wire [WithActorFromContext]
// with [github.com/bds421/rho-kit/httpx/v2/middleware/auth.FormatActorFromContext]
// (or [auth.Actor]) to read verified identity from auth middleware —
// not raw [auth.UserID] alone, which omits machine attribution.
//
// The SDK's [sdkmcp.CallToolRequest] does not expose the full
// [*http.Request]. The kit's wrapper synthesises a minimal
// [*http.Request] whose [http.Request.Header] is the inbound HTTP
// header set and whose [http.Request.Context] is the request
// context. Custom actor extractors that depended on other fields
// (URL, RemoteAddr, ...) need to be reshaped to read Header /
// Context.
//
// When no tenant is on the context, behaviour depends on
// [WithBestEffortAuditOnMissingTenant]:
//
//   - Strict mode (default) refuses to dispatch the tool. The SDK
//     surfaces the refusal as a [sdkmcp.CallToolResult] with
//     `isError: true` and a generic "internal error" content item,
//     preserving the audit invariant that every executed tool
//     produces a signed entry.
//   - Loose mode logs a warn-level message, skips the audit entry,
//     and runs the tool — preserved for operators who explicitly
//     accept the audit gap.
//
// By default the audit append runs synchronously between dispatch
// and response-write so the entry is durable before the caller
// sees the result. [WithAsyncAuditDispatch] flips the append to a
// goroutine for latency-sensitive deployments.
//
// # Destructive-tool gate
//
// Tools registered with [WithDestructive] fail with
// [ErrDestructiveGateRequired] at call time unless the Server has
// either a [WithDestructiveGate] gate function or an explicit
// [WithoutDestructiveGate] acknowledgement. Destructive tools also
// advertise themselves via:
//
//   - The SDK's [sdkmcp.ToolAnnotations.DestructiveHint] (the
//     spec-defined hint), and
//   - The kit's `x-destructive: true` vendor extension on the input
//     schema (preserved for kit-aware clients that pre-date the
//     annotation).
//
// # Error surface
//
// Application-level handler errors (validation failures, gate
// refusals, internal errors) are returned as
// [sdkmcp.CallToolResult] objects with `isError: true` and a
// caller-safe message in the content envelope. This matches the
// MCP spec recommendation that tool-execution errors propagate to
// the model rather than being surfaced as JSON-RPC protocol
// errors. Sensitive infrastructure details (database errors,
// internal hostnames, file paths) are scrubbed from the message and
// logged server-side instead.
//
// # What is NOT done here
//
//   - Auth, rate-limit, idempotency, CSRF: delegated to the
//     surrounding middleware chain. The Server itself has no opinion.
//   - Streaming tools / partial results: deferred. The current
//     transport returns one response per request.
//   - JSON-RPC batch requests: deferred. Single-request semantics
//     keep the action-log entry per-call rather than per-batch.
package mcp
