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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/validate"
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
	impl := &sdkmcp.Implementation{
		Name:    "rho-kit/mcp",
		Version: "v0.1.0",
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

// Register adds a [Handler] as an MCP tool with the given name.
//
// Schema generation: the kit derives the input/output JSON-Schema via
// [validate.SchemaFor], which reads `jsonschema:"..."` struct tags.
// The marshalled schema becomes the `inputSchema`/`outputSchema` on
// the SDK [sdkmcp.Tool]. Using the kit's generator (rather than the
// SDK's built-in jsonschema-go inference) keeps the catalog
// consistent with what [validate.Struct] will enforce after decode.
//
// Register returns an error when:
//   - the input or output type contains a cycle (self-reference);
//   - the input or output type cannot be reflected into a schema;
//   - the name is empty, too long, or outside the supported tool-name grammar;
//   - the name has already been registered.
func Register[In any, Out any](s *Server, name string, h Handler[In, Out], opts ...ToolOption) error {
	if s == nil {
		return errors.New("mcp: Register: server must not be nil")
	}
	if h == nil {
		return errors.New("mcp: Register: handler must not be nil")
	}

	cfg := toolConfig{}
	for _, o := range opts {
		if o == nil {
			panic("mcp: Register option must not be nil")
		}
		o(&cfg)
	}

	if err := validateToolName(name); err != nil {
		return err
	}

	var inZero In

	inSchema, err := resolveInputSchema[In](cfg.inputSchema)
	if err != nil {
		return fmt.Errorf("mcp: Register: input schema: %w", err)
	}
	outSchema, err := resolveOutputSchema[Out](cfg.outputSchema)
	if err != nil {
		return fmt.Errorf("mcp: Register: output schema: %w", err)
	}

	desc := cfg.description
	if desc == "" {
		desc = defaultDescription(reflect.TypeOf(inZero), name)
	}

	if cfg.destructive {
		// Vendor-extension: clients that understand the kit's MCP
		// dialect see `x-destructive: true` and can prompt for
		// confirmation. The kit-side gate is the actual enforcement.
		schema, err := withVendorExtension(inSchema, "x-destructive", true)
		if err != nil {
			return fmt.Errorf("mcp: Register: annotate input schema: %w", err)
		}
		inSchema = schema
	}

	// Reserve the registration slot first so concurrent
	// Register(same-name) races are caught before the SDK side-effect.
	s.mu.Lock()
	if _, dup := s.toolMeta[name]; dup {
		s.mu.Unlock()
		return fmt.Errorf("mcp: Register: tool already registered")
	}
	s.toolMeta[name] = &toolMeta{destructive: cfg.destructive}
	s.tools = append(s.tools, Tool{
		Name:         name,
		Description:  desc,
		InputSchema:  inSchema,
		OutputSchema: outSchema,
	})
	s.mu.Unlock()

	// Use the SDK's low-level Server.AddTool with a kit-owned
	// ToolHandler. We avoid sdkmcp.AddTool[In, Out] because that
	// generic helper unmarshals arguments with internaljson and
	// surfaces the raw decode error string back to the caller —
	// breaking the kit's "do not leak caller-controlled bytes" invariant
	// (security review L-4 / decode-failure tests). Owning decode here
	// lets us rewrite the error to a stable "invalid arguments" message
	// while logging the verbose form server-side.
	sdkTool := &sdkmcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: inSchema,
	}
	if len(outSchema) > 0 {
		sdkTool.OutputSchema = outSchema
	}
	if cfg.destructive {
		destHint := true
		sdkTool.Annotations = &sdkmcp.ToolAnnotations{
			DestructiveHint: &destHint,
		}
	}

	s.sdk.AddTool(sdkTool, wrapToolHandler[In, Out](s, name, h, cfg.destructive))
	return nil
}

// resolveInputSchema returns the JSON-Schema bytes to set on the SDK
// Tool's InputSchema. When the caller supplied an override we validate
// that it's a JSON object and reuse it; otherwise we generate from In
// via the kit's validate package.
func resolveInputSchema[In any](override json.RawMessage) (json.RawMessage, error) {
	if len(override) > 0 {
		return validateSchemaOverride("input", override)
	}
	schema, err := validate.SchemaFor[In]()
	if err != nil {
		return nil, mapSchemaError(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	if err := requireObjectSchema("input", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func resolveOutputSchema[Out any](override json.RawMessage) (json.RawMessage, error) {
	if len(override) > 0 {
		return validateSchemaOverride("output", override)
	}
	schema, err := validate.SchemaFor[Out]()
	if err != nil {
		return nil, mapSchemaError(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	if err := requireObjectSchema("output", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// mapSchemaError translates the validate package's schema-generation
// errors back onto the kit's public sentinels so callers can still
// branch on errors.Is(err, ErrCyclicSchema) / ErrUnsupportedType.
func mapSchemaError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, validate.ErrCyclicSchema) {
		return fmt.Errorf("%w: %v", ErrCyclicSchema, sanitiseSchemaErr(err))
	}
	if errors.Is(err, validate.ErrUnsupportedType) {
		return fmt.Errorf("%w: %v", ErrUnsupportedType, sanitiseSchemaErr(err))
	}
	return err
}

// sanitiseSchemaErr strips any reflect.Type names from the validate
// package's wrapper so the kit's "do not leak caller type names"
// invariant survives even when the validate package learns to embed
// type metadata in errors.
func sanitiseSchemaErr(err error) string {
	switch {
	case errors.Is(err, validate.ErrCyclicSchema):
		return "cyclic type reference"
	case errors.Is(err, validate.ErrUnsupportedType):
		return "unsupported type"
	default:
		return "schema build failed"
	}
}

func validateSchemaOverride(kind string, schema json.RawMessage) (json.RawMessage, error) {
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		return nil, fmt.Errorf("%s schema must be a valid JSON object: %w", kind, err)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s schema must be a JSON object", kind)
	}
	// The SDK's AddTool panics unless the schema's "type" is exactly the
	// string "object". An override that omits the key, or sets it to a
	// non-string value, would otherwise reach AddTool and crash the
	// caller after the registration slot was already reserved — so we
	// require the canonical form up front and return an error instead.
	if typ, ok := obj["type"].(string); !ok || typ != "object" {
		return nil, fmt.Errorf("%s schema must have type \"object\"", kind)
	}
	return append(json.RawMessage(nil), schema...), nil
}

// requireObjectSchema asserts that a marshalled JSON-Schema declares
// `"type": "object"`. The MCP SDK's AddTool panics on any other shape
// (scalar, array, type-less); validate.SchemaFor emits a non-object
// schema for non-struct In/Out types (string, int, slice, time.Time,
// json.RawMessage). Catching it here lets Register honour its
// documented error contract instead of panicking after the catalog
// slot has been reserved.
func requireObjectSchema(kind string, schema json.RawMessage) error {
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		return fmt.Errorf("%s schema must be a valid JSON object: %w", kind, err)
	}
	if typ, ok := obj["type"].(string); !ok || typ != "object" {
		return fmt.Errorf("%s schema must have type \"object\" (only struct, map, and pointer-to-struct %s types are supported)", kind, kind)
	}
	return nil
}

func validActionLogTextField(s string, maxLen int, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// defaultDescription derives a description from the input type's name.
func defaultDescription(in reflect.Type, name string) string {
	if in == nil || in.Name() == "" {
		return "Tool: " + name
	}
	return "Tool " + name + " (input: " + in.Name() + ")"
}

// withVendorExtension parses an existing schema, sets a top-level
// extension key, and re-emits it.
func withVendorExtension(schema json.RawMessage, key string, value any) (json.RawMessage, error) {
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj[key] = value
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return out, nil
}

// sanitiseReason prepares a handler error message for storage in an
// audit entry's Reason field. The action-log signed-store contract
// (data/actionlog) rejects any Reason that contains a NUL byte or
// invalid UTF-8 anywhere — a handler error wrapping raw caller bytes
// would otherwise make the append fail, silently dropping the audit
// entry for an executed tool call (and, in strict sync mode, masking
// the mapped caller message as a bare "internal error"). We replace
// invalid UTF-8 sequences with the Unicode replacement character,
// strip NUL bytes, then cap the length.
func sanitiseReason(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return truncateReason(s)
}

// truncateReason caps an error message at MaxReasonLength bytes.
func truncateReason(s string) string {
	if len(s) <= MaxReasonLength {
		return s
	}
	cut := s[:MaxReasonLength]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut + "..."
}

// validateToolName accepts the same ASCII identifier grammar as the
// pre-SDK kit. The SDK already enforces its own (broader) tool-name
// rule; the kit's stricter rule is preserved so action-log Action
// values continue to round-trip.
var toolNameAllowed = func(name string) error {
	switch {
	case name == "":
		return errors.New("mcp: Register: tool name must not be empty")
	case len(name) > MaxToolNameLen:
		return fmt.Errorf("mcp: Register: invalid tool name (max %d bytes)", MaxToolNameLen)
	case !utf8.ValidString(name):
		return errors.New("mcp: Register: invalid tool name (must be valid UTF-8)")
	}
	if !validToolNameRune(rune(name[0])) || name[0] == '.' || name[0] == '-' || name[0] == '_' || name[0] == '/' {
		return errors.New("mcp: Register: invalid tool name (must start with an alphanumeric)")
	}
	for _, r := range name {
		if !validToolNameRune(r) {
			return errors.New("mcp: Register: invalid tool name (allowed: alphanumeric, '.', '_', '-', '/')")
		}
	}
	return nil
}

func validateToolName(name string) error {
	return toolNameAllowed(name)
}

func validToolNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '-' || r == '/':
		return true
	default:
		return false
	}
}

// wrapToolHandler builds an [sdkmcp.ToolHandler] (the SDK's low-level
// callback shape) that owns argument decode, validation, the
// destructive gate, the audit precheck/append, handler dispatch, and
// caller-safe error mapping.
//
// We deliberately bypass the SDK's [sdkmcp.AddTool] generic helper
// because that helper:
//
//  1. Unmarshals arguments using the SDK's internal json package, then
//     surfaces the raw error message back to the caller — leaking
//     decoder text (e.g. `"json: cannot unmarshal \"secret\" into ..."`)
//     to anyone who can call a tool. The kit's transport contract is
//     "never reflect caller-controlled bytes back in error messages".
//  2. Skips the kit's [validate.Struct] enforcement, which is a
//     superset of the JSON-Schema-level checks the SDK performs.
//
// wrapToolHandler is a free function rather than a method because Go's
// 1.x generics do not allow type parameters on methods.
func wrapToolHandler[In any, Out any](s *Server, name string, h Handler[In, Out], destructive bool) sdkmcp.ToolHandler {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		header := http.Header{}
		if req != nil && req.Extra != nil && req.Extra.Header != nil {
			header = req.Extra.Header
		}
		httpReq := (&http.Request{Header: header}).WithContext(ctx)

		if !s.auditPrecheck(ctx, httpReq, name) {
			return errorResult("internal error"), nil
		}

		// Decode arguments. The SDK has already validated the JSON
		// payload against the inputSchema before reaching us; we still
		// decode strictly (DisallowUnknownFields) so an extra field
		// surfaces as the kit's stable "invalid request" message
		// rather than leaking the field name.
		var rawArgs json.RawMessage
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			rawArgs = req.Params.Arguments
		}
		var in In
		if len(rawArgs) > 0 {
			dec := json.NewDecoder(bytes.NewReader(rawArgs))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				s.logInternalError(ctx, "mcp: argument decode failed", err)
				_ = s.recordActionLog(ctx, httpReq, name, errInvalidArguments)
				return errorResult("invalid arguments"), nil
			}
			if tok, err := dec.Token(); err != io.EOF {
				if err != nil {
					s.logInternalError(ctx, "mcp: trailing token after arguments", err)
				} else {
					// dec.Token returns a nil interface for a JSON null token;
					// reflect.TypeOf(nil) is nil and .String() would panic.
					kind := "null"
					if tok != nil {
						kind = reflect.TypeOf(tok).String()
					}
					s.cfg.logger.Warn("mcp: rejected request with trailing JSON tokens",
						redact.String("tool", name),
						redact.String("token_kind", kind),
					)
				}
				_ = s.recordActionLog(ctx, httpReq, name, errInvalidArguments)
				return errorResult("invalid arguments"), nil
			}
		}

		// Destructive-gate enforcement.
		if destructive {
			gatePayload := rawArgs
			if len(gatePayload) == 0 {
				gatePayload = json.RawMessage("{}")
			}
			if s.cfg.destructiveGate != nil {
				if gateErr := s.cfg.destructiveGate(ctx, name, gatePayload); gateErr != nil {
					_ = s.recordActionLog(ctx, httpReq, name, gateErr)
					return errorResult("destructive call refused"), nil
				}
			} else if !s.cfg.destructiveGateAcknowledged {
				_ = s.recordActionLog(ctx, httpReq, name, ErrDestructiveGateRequired)
				return errorResult("destructive tool not configured"), nil
			}
		}

		if err := validate.Struct(in); err != nil {
			_ = s.recordActionLog(ctx, httpReq, name, err)
			return errorResult(mapErrorForCaller(s, ctx, err)), nil
		}

		out, callErr, panicked := callHandlerSafely(s, ctx, name, h, in)

		// Marshal the response payload BEFORE auditing so a
		// marshal-failure on a "successful" handler return surfaces
		// to the audit as a failure rather than a phantom success.
		// The audit invariant is "every executed tool call produces
		// an entry whose outcome matches what the caller saw"; we
		// can only honour it if the audit reason reflects the
		// post-marshal outcome.
		var outBytes []byte
		marshalFailed := false
		if callErr == nil {
			b, marshalErr := json.Marshal(out)
			if marshalErr != nil {
				s.logInternalError(ctx, "mcp: marshal tool output", marshalErr)
				callErr = marshalErr
				marshalFailed = true
			} else {
				outBytes = b
			}
		}

		if auditErr := s.recordActionLog(ctx, httpReq, name, callErr); auditErr != nil {
			if s.cfg.strictAudit && !s.cfg.asyncAudit {
				return errorResult("internal error"), nil
			}
		}
		if callErr != nil {
			if marshalFailed || panicked {
				// Already logged above (marshal) or inside
				// callHandlerSafely (panic); skip the default-branch
				// logInternalError inside mapErrorForCaller to avoid a
				// duplicate server-side entry.
				return errorResult("internal error"), nil
			}
			return errorResult(mapErrorForCaller(s, ctx, callErr)), nil
		}

		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(outBytes)},
			},
			StructuredContent: json.RawMessage(outBytes),
		}, nil
	}
}

// callHandlerSafely invokes the typed tool handler, converting a panic
// into a returned error so the dispatch path can audit and mask it like
// any other failure.
//
// The MCP SDK's dispatch path (v1.6.1) contains no recover(); a
// panicking handler would otherwise unwind past wrapToolHandler into
// the SDK's jsonrpc2 goroutine and crash the process — and, because the
// panic happens before recordActionLog runs, no failure entry would be
// written, breaking the strict-audit invariant that every executed tool
// call produces a signed entry. Recovering here turns the panic into a
// generic, caller-safe error ([errHandlerPanicked]) and logs the
// recovered value server-side (redacted). The bool return signals the
// caller to skip the duplicate default-branch log in mapErrorForCaller.
func callHandlerSafely[In any, Out any](s *Server, ctx context.Context, name string, h Handler[In, Out], in In) (out Out, err error, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			err = errHandlerPanicked
			var zero Out
			out = zero
			s.cfg.logger.ErrorContext(ctx, "mcp: tool handler panicked",
				redact.String("tool", name),
				redact.Panic(rec),
			)
		}
	}()
	out, err = h(ctx, in)
	return out, err, false
}

// errorResult builds a CallToolResult with IsError=true and the
// supplied caller-safe message as the sole text-content item. The
// message MUST NOT contain caller-controlled bytes.
func errorResult(msg string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: msg},
		},
	}
}

// mapErrorForCaller converts a handler/validation error into a
// caller-safe message string. Sensitive infrastructure errors are
// logged server-side and the caller sees "internal error" only.
//
// Validation errors with structured Fields slices (constructed by
// handler code with the wire surface in mind) are passed through —
// they carry field names, not free-form text — so the caller learns
// which argument to correct on the next attempt.
func mapErrorForCaller(s *Server, ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case apperror.IsValidation(err):
		if ve, ok := apperror.AsValidation(err); ok && len(ve.Fields) > 0 {
			return ve.Error()
		}
		return "invalid request"
	case apperror.IsNotFound(err):
		return "resource not found"
	case apperror.IsAuthRequired(err):
		return "authentication required"
	case apperror.IsForbidden(err):
		return "forbidden"
	case apperror.IsRateLimit(err):
		return "rate limit exceeded"
	case apperror.IsConflict(err):
		return "conflict"
	default:
		s.logInternalError(ctx, "mcp: tool returned internal error", err)
		return "internal error"
	}
}

// logInternalError records a server-side log entry for an error
// whose details must not be returned to the caller.
func (s *Server) logInternalError(ctx context.Context, msg string, err error) {
	s.cfg.logger.ErrorContext(ctx, msg,
		redact.Error(err),
	)
}
