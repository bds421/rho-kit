// Package mcp exposes typed kit handlers as Model Context Protocol
// (MCP) tools, wrapping the modelcontextprotocol/go-sdk transport.
//
// Wave 121 retired the kit's hand-rolled JSON-RPC implementation in
// favour of the official Go SDK
// ([github.com/modelcontextprotocol/go-sdk/mcp]). The wire format is
// now SDK-canonical Streamable HTTP; clients must send
// `Accept: application/json, text/event-stream` and
// `Content-Type: application/json` per the MCP spec.
//
// The kit's value-add — typed [Register] generic, server-side
// destructive-tool gate, strict tenant/actor audit invariants and the
// action-log integration — is preserved as a thin wrapper around the
// SDK's `AddTool` + `NewStreamableHTTPHandler` primitives.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/httpx/v2/internal/headerutil"
)

// AnonymousActor is the actor id recorded when no authenticated identity
// is available for a tool call (no auth middleware ran, or it ran but did
// not surface a user id) and the service explicitly opts into anonymous
// audit entries. Strict audit rejects this sentinel by default when an
// action logger is configured; use [WithAllowAnonymousActor] only for
// local demos or deliberate shared-identity transports.
const AnonymousActor = "anonymous"

// DefaultActorHeader is the conventional header name used by the
// opt-in [WithActorFromHeader] extractor. The Server does NOT read
// this header by default — see the documentation on
// [WithActorFromHeader] for the trust requirement.
//
// Services that authenticate callers via JWT/auth middleware should
// wire [WithActorFromContext] (or a custom [WithActorExtractor]) and
// leave this header alone.
const DefaultActorHeader = "X-Actor-Id"

// MaxReasonLength caps the length of [actionlog.Entry.Reason] when
// recording a failed tool call. A verbose error (e.g. a wrapped
// stack trace) shouldn't bloat an audit row — operators reading the
// log want a sentence, not a transcript.
const MaxReasonLength = 1024

// MaxToolNameLen is the maximum registered MCP tool-name length.
// Action-log entries store tool calls as "mcp."+name and cap Action
// at actionlog.MaxActionLen, so registration enforces the same limit
// up front instead of letting a call fail after the tool has run.
const MaxToolNameLen = actionlog.MaxActionLen - len("mcp.")

// ErrDestructiveGateRequired is returned to the caller when a tool
// marked [WithDestructive] is invoked on a Server that has neither
// a [DestructiveGate] wired via [WithDestructiveGate] nor an
// explicit [WithoutDestructiveGate] acknowledgement.
var ErrDestructiveGateRequired = errors.New("mcp: destructive tool requires a server-side gate (configure WithDestructiveGate, or acknowledge via WithoutDestructiveGate)")

// ErrCyclicSchema is returned by [Register] when the In/Out type
// contains a cycle (a struct that recursively references its own
// type). The kit walks the type via the [validate] package's
// jsonschema-go-backed inference; cyclic types cannot be expressed as
// JSON-Schema and would otherwise produce a non-terminating tool
// catalog.
var ErrCyclicSchema = errors.New("mcp: schema generation: cyclic type reference")

// ErrUnsupportedType is returned by [Register] when a Go type has no
// JSON-Schema mapping (e.g. channels, function types, non-string map
// keys).
var ErrUnsupportedType = errors.New("mcp: schema generation: unsupported type")

// errHandlerPanicked is the generic, caller-safe error recorded when a
// tool handler panics. The recovered panic value is logged server-side
// (redacted) but never surfaced to the caller or written verbatim to
// the audit reason, preserving the kit's "do not reflect handler
// internals to the caller" invariant.
var errHandlerPanicked = errors.New("mcp: tool handler panicked")

// errInvalidArguments is the generic, non-reflecting reason recorded in the
// action log when an argument payload fails to decode (unknown field, type
// mismatch, or trailing JSON tokens). It mirrors the caller-facing "invalid
// arguments" message and deliberately omits the raw payload so the audit reason
// never reflects caller-controlled bytes — keeping malformed-argument probes
// visible to operators alongside validation and gate refusals.
var errInvalidArguments = errors.New("mcp: invalid arguments")

// Tool describes one MCP tool. Every handler registered via
// [Register] becomes one Tool. The fields are the kit-visible surface
// of the SDK's [sdkmcp.Tool]; the underlying SDK value is constructed
// at registration time and lives inside the SDK server.
type Tool struct {
	// Name uniquely identifies the tool. Convention: dotted lowercase
	// scope (e.g. "user.delete"). Names are ASCII identifiers and
	// capped at MaxToolNameLen bytes.
	Name string `json:"name"`

	// Description is human-readable. Default: derived from the input
	// type's name; override via [WithToolDescription].
	Description string `json:"description,omitempty"`

	// InputSchema is the JSON-Schema describing the tool's input. The
	// SDK infers it from the In type's `jsonschema:"..."` tags unless
	// overridden via [WithInputSchema].
	InputSchema json.RawMessage `json:"inputSchema"`

	// OutputSchema describes the tool's response. Optional in MCP;
	// some clients use it for typed deserialisation. The SDK infers
	// it from the Out type unless overridden via [WithOutputSchema].
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// Handler is the kit-canonical typed handler shape for MCP tools.
// The In type is the input struct; Out is the response. Both must
// marshal/unmarshal cleanly via encoding/json. Validation tags from
// [core/v2/validate] are honoured by the [Server] before the handler
// runs.
type Handler[In any, Out any] func(ctx context.Context, in In) (Out, error)

// ToolOption configures a tool at registration time.
type ToolOption func(*toolConfig)

type toolConfig struct {
	description  string
	inputSchema  json.RawMessage
	outputSchema json.RawMessage
	destructive  bool
}

// WithToolDescription sets the human-readable description shown in
// the tool catalog. Pass an empty string to fall back to the
// auto-derived description.
func WithToolDescription(s string) ToolOption {
	return func(c *toolConfig) { c.description = s }
}

// WithInputSchema overrides the SDK-inferred input JSON-Schema. The
// override must be a valid JSON object with `"type": "object"` (an
// MCP spec requirement enforced by the SDK).
func WithInputSchema(schema json.RawMessage) ToolOption {
	return func(c *toolConfig) { c.inputSchema = schema }
}

// WithOutputSchema overrides the SDK-inferred output JSON-Schema. The
// override must be a valid JSON object with `"type": "object"`.
func WithOutputSchema(schema json.RawMessage) ToolOption {
	return func(c *toolConfig) { c.outputSchema = schema }
}

// WithDestructive marks the tool as destructive. The Server records
// this in the tool catalog as an SDK `ToolAnnotations.DestructiveHint`
// AND a kit `x-destructive: true` vendor extension on the input
// schema (preserving the pre-SDK on-wire contract for kit-aware
// clients).
//
// Destructive tools fail with [ErrDestructiveGateRequired] at call
// time unless one of these is configured:
//
//   - [WithDestructiveGate] — supplies a server-side gate (typically
//     wired to [httpx/middleware/approval] or a custom authorization
//     hook) that runs before dispatch and can refuse the call.
//   - [WithoutDestructiveGate] — explicit "I am running destructive
//     tools without server-side enforcement; clients prompt only"
//     acknowledgement.
func WithDestructive() ToolOption {
	return func(c *toolConfig) { c.destructive = true }
}

// ServerOption configures the [Server].
type ServerOption func(*serverConfig)

type serverConfig struct {
	logger                      *slog.Logger
	actionLogger                actionlog.Logger
	actorExtractor              func(*http.Request) string
	allowAnonymousActor         bool
	tenantExtractor             func(ctx context.Context) (string, bool)
	strictAudit                 bool
	strictAuditTimeout          time.Duration
	asyncAudit                  bool
	asyncAuditWorkers           int
	asyncAuditQueue             int
	asyncAuditTimeout           time.Duration
	destructiveGate             DestructiveGate
	destructiveGateAcknowledged bool
	serverName                  string
	serverVersion               string
}

// DestructiveGate authorizes a destructive-tool invocation. The
// Server calls it before dispatching any tool registered with
// [WithDestructive]; a non-nil error refuses the call and the kit
// returns that error to the caller (typically a structured
// "approval required" response).
//
// ctx is the request context. toolName identifies the tool. The
// payload is the raw, JSON-marshalled tool input — useful for
// approval flows that need to surface the proposed change to a
// human reviewer.
type DestructiveGate func(ctx context.Context, toolName string, payload []byte) error

// WithDestructiveGate wires a server-side gate for tools registered
// with [WithDestructive].
func WithDestructiveGate(fn DestructiveGate) ServerOption {
	if fn == nil {
		panic("mcp: WithDestructiveGate requires a non-nil gate")
	}
	return func(c *serverConfig) { c.destructiveGate = fn }
}

// WithoutDestructiveGate is the explicit "I gate destructive calls
// somewhere else in my stack and just want the metadata annotation"
// acknowledgement.
func WithoutDestructiveGate() ServerOption {
	return func(c *serverConfig) { c.destructiveGateAcknowledged = true }
}

// WithLogger sets the [slog.Logger] used for server-side errors.
// Default: [slog.Default].
func WithLogger(l *slog.Logger) ServerOption {
	if l == nil {
		panic("mcp: WithLogger requires a non-nil logger")
	}
	return func(c *serverConfig) { c.logger = l }
}

// WithActionLogger wires an [actionlog.Logger]. When set, the Server
// appends one entry per tool call (Outcome=success on a clean
// return, Outcome=failure on any error).
func WithActionLogger(l actionlog.Logger) ServerOption {
	if l == nil {
		panic("mcp: WithActionLogger requires a non-nil logger")
	}
	return func(c *serverConfig) { c.actionLogger = l }
}

// WithActorExtractor sets the function that resolves an actor id
// from a request. The default returns [AnonymousActor], but strict
// audit mode rejects that default when an action logger is configured
// unless [WithAllowAnonymousActor] is also supplied.
//
// The SDK's [sdkmcp.CallToolRequest] does not expose the full
// [*http.Request]; the kit synthesises a minimal request whose
// [http.Request.Header] is the inbound HTTP header and whose
// [http.Request.Context] is the request context. Custom extractors
// that depend on other fields of [http.Request] need to be reshaped
// to read Header / Context instead.
func WithActorExtractor(fn func(*http.Request) string) ServerOption {
	if fn == nil {
		panic("mcp: WithActorExtractor requires a non-nil extractor")
	}
	return func(c *serverConfig) {
		c.actorExtractor = func(r *http.Request) string {
			v := fn(r)
			if v == "" {
				return AnonymousActor
			}
			return v
		}
	}
}

// WithAllowAnonymousActor permits the default [AnonymousActor] value
// when an action logger is configured.
func WithAllowAnonymousActor() ServerOption {
	return func(c *serverConfig) { c.allowAnonymousActor = true }
}

// WithActorFromContext reads the actor id from the request context
// using fn — typically [auth.FormatActorFromContext] from
// httpx/middleware/auth for actionlog-shaped strings, or [auth.Actor]
// when a bare attribution id is enough. The function receives the context
// from the inbound HTTP request.
func WithActorFromContext(fn func(context.Context) string) ServerOption {
	if fn == nil {
		panic("mcp: WithActorFromContext requires a non-nil extractor")
	}
	return WithActorExtractor(func(r *http.Request) string {
		return fn(r.Context())
	})
}

// WithActorFromHeader reads the actor id from the named request
// header.
//
// SECURITY WARNING: any caller able to reach this service can set the
// header. Use only when a reverse proxy strips and re-stamps the
// header from a verified identity. Prefer [WithActorFromContext]
// otherwise.
func WithActorFromHeader(header string) ServerOption {
	if !httpguts.ValidHeaderFieldName(header) {
		panic("mcp: WithActorFromHeader requires a valid non-empty header name")
	}
	return WithActorExtractor(func(r *http.Request) string {
		value, ok := headerutil.SingletonIdentity(r.Header, header)
		if !ok {
			return ""
		}
		return value
	})
}

// WithTenantExtractor sets the function that resolves a tenant id
// from a context.
func WithTenantExtractor(fn func(ctx context.Context) (string, bool)) ServerOption {
	if fn == nil {
		panic("mcp: WithTenantExtractor requires a non-nil extractor")
	}
	return func(c *serverConfig) { c.tenantExtractor = fn }
}

// WithStrictAuditTimeout caps how long a synchronous strict-mode
// audit append may run before its bounded context deadline trips.
// Default: 5s.
func WithStrictAuditTimeout(d time.Duration) ServerOption {
	if d <= 0 {
		panic("mcp: WithStrictAuditTimeout requires a positive duration")
	}
	return func(c *serverConfig) { c.strictAuditTimeout = d }
}

// WithBestEffortAuditOnMissingTenant opts out of the default
// fail-closed audit gate so that tool calls dispatched on a request
// with no tenant context will still execute.
func WithBestEffortAuditOnMissingTenant() ServerOption {
	return func(c *serverConfig) { c.strictAudit = false }
}

// WithAsyncAuditDispatch enables best-effort async audit append.
//
// Async dispatch voids the strict-audit fail-closed invariant: queue
// saturation or a hung store can drop entries for already-executed tools.
// Combine with [WithBestEffortAuditOnMissingTenant] to acknowledge
// best-effort audit; [NewServer] panics if async is enabled while strict
// audit remains on (the default).
func WithAsyncAuditDispatch() ServerOption {
	return func(c *serverConfig) { c.asyncAudit = true }
}

// WithAsyncAuditWorkers sets the number of background workers
// performing async audit appends. Default: 4. Must be > 0.
func WithAsyncAuditWorkers(n int) ServerOption {
	if n <= 0 {
		panic("mcp: WithAsyncAuditWorkers requires a positive worker count")
	}
	return func(c *serverConfig) { c.asyncAuditWorkers = n }
}

// WithAsyncAuditQueue sets the bounded queue depth for async audit
// appends. Default: 256. Must be > 0.
func WithAsyncAuditQueue(n int) ServerOption {
	if n <= 0 {
		panic("mcp: WithAsyncAuditQueue requires a positive queue depth")
	}
	return func(c *serverConfig) { c.asyncAuditQueue = n }
}

// WithAsyncAuditTimeout caps how long a single async audit append
// may run before its context deadline trips. Default: 5s.
func WithAsyncAuditTimeout(d time.Duration) ServerOption {
	if d <= 0 {
		panic("mcp: WithAsyncAuditTimeout requires a positive duration")
	}
	return func(c *serverConfig) { c.asyncAuditTimeout = d }
}

// WithServerInfo overrides the MCP Implementation name/version advertised
// in the initialize handshake. Defaults are "rho-kit/mcp" and the kit
// module major path version ("v2"). Empty name or version panics — an
// empty Implementation is never useful and almost always a wiring bug.
func WithServerInfo(name, version string) ServerOption {
	if strings.TrimSpace(name) == "" {
		panic("mcp: WithServerInfo requires a non-empty name")
	}
	if strings.TrimSpace(version) == "" {
		panic("mcp: WithServerInfo requires a non-empty version")
	}
	return func(c *serverConfig) {
		c.serverName = name
		c.serverVersion = version
	}
}

// Server collects registered tools and serves the MCP Streamable HTTP
// surface via the modelcontextprotocol/go-sdk.
//
// Construct via [NewServer]. Register tools with [Register]. Mount
// the [Server.HTTP] handler on the same mux as the REST API.
//
// Safe for concurrent use — the tool catalog cache is RWMutex-guarded;
// async audit workers join an internal WaitGroup on Shutdown.
type Server struct {
	cfg serverConfig

	sdk *sdkmcp.Server

	mu       sync.RWMutex
	tools    []Tool
	toolMeta map[string]*toolMeta

	// auditQueue is a bounded channel that workers drain.
	auditQueue   chan auditJob
	auditDone    chan struct{}
	auditStopMu  sync.RWMutex
	auditStopped atomic.Bool
	auditWG      sync.WaitGroup
	auditDropped atomic.Int64
}

// toolMeta records kit-side per-tool state needed during dispatch.
// The SDK owns the canonical [sdkmcp.Tool] (input schema, output
// schema, handler); the kit only needs to remember whether the tool
// is destructive for gate enforcement.
type toolMeta struct {
	destructive bool
}

type auditJob struct {
	ctx      context.Context
	entry    actionlog.Entry
	tool     string
	tenantID string
}

// NewServer creates a Server with the given options.
func NewServer(opts ...ServerOption) *Server {
	cfg := defaultServerConfig()
	for _, o := range opts {
		if o == nil {
			panic("mcp: NewServer option must not be nil")
		}
		o(&cfg)
	}
	name := cfg.serverName
	if name == "" {
		name = "rho-kit/mcp"
	}
	ver := cfg.serverVersion
	if ver == "" {
		ver = "v2"
	}
	impl := &sdkmcp.Implementation{
		Name:    name,
		Version: ver,
	}
	sdkOpts := &sdkmcp.ServerOptions{
		Logger: cfg.logger,
	}
	if cfg.asyncAudit && cfg.strictAudit {
		// Async append is best-effort (drops on queue saturation). Strict
		// audit promises every executed tool call produces a signed entry.
		// Combining them silently voids that promise — fail loud instead.
		panic("mcp: WithAsyncAuditDispatch requires WithBestEffortAuditOnMissingTenant (async dispatch is best-effort and voids strict audit)")
	}
	s := &Server{
		cfg:      cfg,
		sdk:      sdkmcp.NewServer(impl, sdkOpts),
		toolMeta: make(map[string]*toolMeta),
	}
	if cfg.asyncAudit {
		s.startAuditWorkers()
	}
	return s
}

func defaultServerConfig() serverConfig {
	return serverConfig{
		logger: slog.Default(),
		// Default actor extractor must NOT trust any header.
		actorExtractor: func(_ *http.Request) string {
			return AnonymousActor
		},
		tenantExtractor:    defaultTenantExtractor,
		strictAudit:        true,
		strictAuditTimeout: 5 * time.Second,
		asyncAudit:         false,
		asyncAuditWorkers:  4,
		asyncAuditQueue:    256,
		asyncAuditTimeout:  5 * time.Second,
	}
}

// startAuditWorkers spins up a bounded worker pool for async audit
// appends.
func (s *Server) startAuditWorkers() {
	s.auditQueue = make(chan auditJob, s.cfg.asyncAuditQueue)
	s.auditDone = make(chan struct{})
	for i := 0; i < s.cfg.asyncAuditWorkers; i++ {
		s.auditWG.Add(1)
		go s.auditWorker()
	}
}

func (s *Server) auditWorker() {
	defer s.auditWG.Done()
	for {
		select {
		case job := <-s.auditQueue:
			s.runAuditJob(job)
		case <-s.auditDone:
			for {
				select {
				case job := <-s.auditQueue:
					s.runAuditJob(job)
				default:
					return
				}
			}
		}
	}
}

func (s *Server) runAuditJob(job auditJob) {
	defer func() {
		if rec := recover(); rec != nil {
			s.cfg.logger.Error("mcp: async audit append panicked",
				redact.String("tool", job.tool),
				redact.String("tenant_id", job.tenantID),
				redact.Panic(rec),
			)
		}
	}()
	ctx, cancel := asyncAuditContext(job.ctx, s.cfg.asyncAuditTimeout)
	defer cancel()
	if err := s.appendActionLog(ctx, job.entry, job.tool, job.tenantID); err != nil {
		s.cfg.logger.Warn("mcp: async audit append failed",
			redact.String("tool", job.tool),
			redact.String("tenant_id", job.tenantID),
			redact.ErrorKey("err", err),
		)
	}
}

func asyncAuditContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// Stop drains in-flight async audit appends and shuts down the worker
// pool. Safe to call multiple times.
func (s *Server) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mcp: Server.Stop requires a non-nil context")
	}
	if !s.cfg.asyncAudit {
		return nil
	}
	// Signal shutdown exactly once (idempotent close of auditDone), but ALWAYS
	// fall through to wait on auditWG below. A first Stop whose context times
	// out returns ctx.Err() while workers are still draining; a retry with a
	// fresh context must re-wait rather than return a false success, or the
	// caller races process exit against in-flight audit appends.
	s.auditStopMu.Lock()
	if !s.auditStopped.Load() {
		s.auditStopped.Store(true)
		close(s.auditDone)
	}
	s.auditStopMu.Unlock()
	done := make(chan struct{})
	go func() {
		s.auditWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AsyncAuditDropped returns the cumulative number of async audit
// appends that were dropped because the bounded queue was saturated.
func (s *Server) AsyncAuditDropped() int64 {
	return s.auditDropped.Load()
}

// HTTP returns the http.Handler that speaks MCP Streamable HTTP.
//
// The handler is the SDK's
// [sdkmcp.NewStreamableHTTPHandler] in stateless + JSON-response mode:
// every POST is treated as an independent JSON-RPC envelope and the
// reply is `application/json` rather than `text/event-stream`. This
// keeps the kit's prior "stateless, one-call-per-request" contract.
//
// Clients MUST send `Accept: application/json, text/event-stream` and
// `Content-Type: application/json`. The SDK rejects requests that omit
// either with `400 Bad Request` / `415 Unsupported Media Type`.
func (s *Server) HTTP() http.Handler {
	return sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return s.sdk },
		&sdkmcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			Logger:       s.cfg.logger,
		},
	)
}

// Tools returns a copy of the registered tool catalog, sorted by
// name. The returned slice is safe to mutate.
func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		tool := t
		tool.InputSchema = append(json.RawMessage(nil), t.InputSchema...)
		tool.OutputSchema = append(json.RawMessage(nil), t.OutputSchema...)
		out = append(out, tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
