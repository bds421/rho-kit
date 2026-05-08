package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/bds421/rho-kit/data/v2/actionlog"
)

// AnonymousActor is the actor id recorded when no authenticated identity
// is available for a tool call (no auth middleware ran, or it ran but did
// not surface a user id). The signed action-log store rejects empty actor
// values, so the Server substitutes this sentinel.
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

// Tool describes one MCP tool. Every handler registered via
// [Register] becomes one Tool. The fields are the public surface of
// the MCP `tools/list` response — clients read them to render the
// tool catalog and validate inputs.
type Tool struct {
	// Name uniquely identifies the tool. Convention: dotted lowercase
	// scope (e.g. "user.delete"). Names may not contain whitespace.
	Name string `json:"name"`

	// Description is human-readable. Default: derived from the input
	// type's name; override via [WithToolDescription].
	Description string `json:"description,omitempty"`

	// InputSchema is a JSON-Schema describing the tool's input
	// struct. Auto-generated from the Go type via [GenerateSchema];
	// override via [WithInputSchema].
	InputSchema json.RawMessage `json:"inputSchema"`

	// OutputSchema describes the tool's response. Optional in MCP;
	// some clients use it for typed deserialisation. Auto-generated
	// from the Go type unless overridden via [WithOutputSchema].
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// Handler is the kit-canonical typed handler shape for MCP tools.
// The In type is the input struct; Out is the response. Both must
// marshal/unmarshal cleanly via encoding/json. Validation tags from
// [core/validate] are honoured by the [Server] before the handler
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

// WithInputSchema overrides the auto-generated input JSON-Schema.
// Use sparingly — drift between the schema and the Go struct
// produces validation failures that look like bugs.
func WithInputSchema(schema json.RawMessage) ToolOption {
	return func(c *toolConfig) { c.inputSchema = schema }
}

// WithOutputSchema overrides the auto-generated output JSON-Schema.
func WithOutputSchema(schema json.RawMessage) ToolOption {
	return func(c *toolConfig) { c.outputSchema = schema }
}

// WithDestructive flags the tool as destructive. The Server records
// this in the tool catalog and emits a top-level `destructive: true`
// JSON-Schema vendor-extension so clients can prompt for
// confirmation. Wire the [httpx/middleware/approval] middleware
// around the Server to gate destructive calls; the flag here is
// metadata only.
func WithDestructive(b bool) ToolOption {
	return func(c *toolConfig) { c.destructive = b }
}

// ServerOption configures the [Server].
type ServerOption func(*serverConfig)

type serverConfig struct {
	logger             *slog.Logger
	actionLogger       actionlog.Logger
	actorExtractor     func(*http.Request) string
	tenantExtractor    func(ctx context.Context) (string, bool)
	maxRequestBytes    int64
	strictAudit        bool
	strictAuditTimeout time.Duration
	asyncAudit         bool
	asyncAuditWorkers  int
	asyncAuditQueue    int
	asyncAuditTimeout  time.Duration
}

// WithLogger sets the [slog.Logger] used for server-side errors.
// Default: [slog.Default].
func WithLogger(l *slog.Logger) ServerOption {
	if l == nil {
		panic("mcp: WithLogger: logger must not be nil")
	}
	return func(c *serverConfig) { c.logger = l }
}

// WithActionLogger wires an [actionlog.Logger]. When set, the Server
// appends one entry per tool call (Outcome=success on a clean
// return, Outcome=failure on any error).
//
// Without an action logger the Server still serves tools — the
// audit trail simply moves to whatever transport-layer logging the
// caller already runs. Production deployments are strongly
// encouraged to wire this so "what did the agent do this hour
// against tenant X" is a SQL query.
func WithActionLogger(l actionlog.Logger) ServerOption {
	if l == nil {
		panic("mcp: WithActionLogger: logger must not be nil")
	}
	return func(c *serverConfig) { c.actionLogger = l }
}

// WithActorExtractor sets the function that resolves an actor id
// from a request. The default returns [AnonymousActor] — the Server
// does NOT trust any header by default, since headers can be spoofed
// by any caller able to reach the JSON-RPC endpoint.
//
// Services that put actor on context via auth middleware should pass
// [WithActorFromContext]. Services that genuinely want to trust a
// header (and have a reverse proxy stamping it) can pass
// [WithActorFromHeader] — read its doc first.
//
// An empty return value is recorded as [AnonymousActor] — same
// convention as [httpx/middleware/approval] — so the action log
// never carries an empty Actor field that the store would reject.
func WithActorExtractor(fn func(*http.Request) string) ServerOption {
	if fn == nil {
		panic("mcp: WithActorExtractor: function must not be nil")
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

// WithActorFromContext returns a ServerOption that reads the actor id
// from the request context using fn — typically a wrapper around
// [auth.UserID] from httpx/middleware/auth. This is the recommended
// way to wire actor identity: the auth middleware has already
// verified the caller's credentials and a context value cannot be
// forged by a remote client.
//
// fn must not be nil. An empty return is recorded as [AnonymousActor].
func WithActorFromContext(fn func(context.Context) string) ServerOption {
	if fn == nil {
		panic("mcp: WithActorFromContext: function must not be nil")
	}
	return WithActorExtractor(func(r *http.Request) string {
		return fn(r.Context())
	})
}

// WithActorFromHeader returns a ServerOption that reads the actor id
// from the named request header.
//
// SECURITY WARNING: any caller able to reach this service can set the
// header to an arbitrary value. Use this only when ALL of the
// following are true:
//
//  1. the service is exclusively reachable via a reverse proxy
//     (Oathkeeper, ingress, mesh sidecar) that you control;
//  2. that proxy strips the header from inbound requests and
//     re-stamps it from a verified identity (mTLS CN, verified JWT
//     subject) before forwarding;
//  3. the proxy's own connection to this service is mutually
//     authenticated (mTLS, sidecar-only network).
//
// In every other case prefer [WithActorFromContext]. An empty header
// is recorded as [AnonymousActor].
func WithActorFromHeader(header string) ServerOption {
	if header == "" {
		panic("mcp: WithActorFromHeader: header must not be empty")
	}
	return WithActorExtractor(func(r *http.Request) string {
		return r.Header.Get(header)
	})
}

// WithTenantExtractor sets the function that resolves a tenant id
// from a context. The default uses [tenant.FromContext]. Override
// only when the kit's tenant package is not the source of truth for
// your service.
func WithTenantExtractor(fn func(ctx context.Context) (string, bool)) ServerOption {
	if fn == nil {
		panic("mcp: WithTenantExtractor: function must not be nil")
	}
	return func(c *serverConfig) { c.tenantExtractor = fn }
}

// WithMaxRequestBytes caps the request body the Server will read.
// Default: 1 MiB. A JSON-RPC client that sends a malformed gigabyte
// of garbage shouldn't be able to OOM the process. Panics on
// non-positive values.
func WithMaxRequestBytes(n int64) ServerOption {
	if n <= 0 {
		panic("mcp: WithMaxRequestBytes requires a positive value")
	}
	return func(c *serverConfig) { c.maxRequestBytes = n }
}

// WithStrictAuditTimeout caps how long a synchronous strict-mode audit
// append may run before its bounded context deadline trips. A hung
// audit store would otherwise pin the tool-call goroutine indefinitely
// after the tool's side effects already happened. Default: 5s. Must be
// > 0. Has no effect in async mode (see [WithAsyncAuditTimeout]).
func WithStrictAuditTimeout(d time.Duration) ServerOption {
	if d <= 0 {
		panic("mcp: WithStrictAuditTimeout requires a positive value")
	}
	return func(c *serverConfig) { c.strictAuditTimeout = d }
}

// WithStrictAudit controls the Server's behaviour when an action
// logger is configured but the request context carries no tenant.
//
// Default: true (fail-closed). The JSON-RPC caller receives a
// -32603 internal error and the tool does NOT execute. This
// preserves the audit invariant that "every executed tool call
// produced a signed entry."
//
// WithStrictAudit(false) restores the legacy behaviour: the Server
// logs a warn-level message, skips the audit entry, and runs the
// tool anyway. Use only when operators have explicitly accepted the
// audit gap (e.g. a dev environment without tenant middleware). In
// production this opens a fail-open path where a tool executed
// against an unscoped request leaves no signed evidence.
//
// Has no effect when no action logger is configured (audit is
// already opt-in there).
func WithStrictAudit(strict bool) ServerOption {
	return func(c *serverConfig) { c.strictAudit = strict }
}

// WithAsyncAudit controls whether the action-log append runs on the
// request hot path or in a background goroutine pool.
//
// Default: false. The append runs synchronously between dispatch and
// response-write — a slow audit store extends MCP latency, but a
// crash between the two cannot lose the entry.
//
// WithAsyncAudit(true) hands appends to a bounded worker pool that
// performs the append using context.WithoutCancel of the request
// context plus a per-task timeout (see [WithAsyncAuditTimeout]). The
// pool is sized by [WithAsyncAuditWorkers]. When the queue saturates,
// the oldest-fitting append is recorded as DROPPED via a counter
// surfaced through [Server.AsyncAuditDropped] and the request still
// returns success — async mode is best-effort by definition.
//
// Use async mode when MCP latency dominates over single-request
// durability — e.g. high-RPS read-only tools where the client
// retries on its own and a missed entry is acceptable. Pair with
// [Server.Stop] so workers drain on graceful shutdown.
//
// Has no effect when no action logger is configured.
func WithAsyncAudit(async bool) ServerOption {
	return func(c *serverConfig) { c.asyncAudit = async }
}

// WithAsyncAuditWorkers sets the number of background workers
// performing async audit appends. Default: 4. Must be > 0.
func WithAsyncAuditWorkers(n int) ServerOption {
	if n <= 0 {
		panic("mcp: WithAsyncAuditWorkers requires a positive value")
	}
	return func(c *serverConfig) { c.asyncAuditWorkers = n }
}

// WithAsyncAuditQueue sets the bounded queue depth for async audit
// appends. When the queue is full, new appends are dropped (counter
// increment) rather than spawning unbounded goroutines. Default: 256.
// Must be > 0.
func WithAsyncAuditQueue(n int) ServerOption {
	if n <= 0 {
		panic("mcp: WithAsyncAuditQueue requires a positive value")
	}
	return func(c *serverConfig) { c.asyncAuditQueue = n }
}

// WithAsyncAuditTimeout caps how long a single async audit append may
// run before its context deadline trips. Default: 5s. Prevents a hung
// audit store from pinning workers indefinitely. Must be > 0.
func WithAsyncAuditTimeout(d time.Duration) ServerOption {
	if d <= 0 {
		panic("mcp: WithAsyncAuditTimeout requires a positive value")
	}
	return func(c *serverConfig) { c.asyncAuditTimeout = d }
}

// Server collects registered tools and serves the JSON-RPC surface.
// Reuses the kit's HTTP middleware stack — auth, tenant,
// idempotency, rate limit, audit — applied externally to the
// JSON-RPC endpoint.
//
// Construct via [NewServer]. Register tools with [Register]. Mount
// the [Server.HTTP] handler on the same mux as the REST API.
type Server struct {
	cfg serverConfig

	mu    sync.RWMutex
	tools map[string]*toolEntry

	// auditQueue is a bounded channel that workers drain. Senders
	// never close it; only Stop closes auditDone to signal that no
	// new appends will be enqueued. Workers exit when the channel
	// drains and auditDone is closed.
	//
	// Race-safety: enqueue and Stop are mutually exclusive on
	// auditStopMu. Enqueue takes the read lock, re-checks
	// auditStopped, and sends to auditQueue while still holding the
	// read lock. Stop takes the write lock, flips auditStopped, and
	// closes auditDone before releasing — so any send that observes
	// auditStopped == false has serialized before Stop's close, and
	// the send to a still-open auditQueue cannot collide with a
	// concurrent shutdown.
	auditQueue   chan auditJob
	auditDone    chan struct{}
	auditStopMu  sync.RWMutex
	auditStopped atomic.Bool
	auditWG      sync.WaitGroup
	auditDropped atomic.Int64
}

type auditJob struct {
	entry    actionlog.Entry
	tool     string
	tenantID string
}

// toolEntry is the internal registration record for a Tool. The
// dispatch closure boxes the typed Handler so the Server doesn't
// need to track In/Out generic parameters at runtime.
type toolEntry struct {
	tool     Tool
	dispatch dispatchFunc
}

// dispatchFunc is the type-erased dispatch shape. It accepts the raw
// JSON params bytes, decodes them, validates, calls the handler, and
// returns the encoded response.
type dispatchFunc func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error)

// NewServer creates a Server with the given options.
func NewServer(opts ...ServerOption) *Server {
	cfg := defaultServerConfig()
	for _, o := range opts {
		o(&cfg)
	}
	s := &Server{cfg: cfg, tools: make(map[string]*toolEntry)}
	if cfg.asyncAudit {
		s.startAuditWorkers()
	}
	return s
}

func defaultServerConfig() serverConfig {
	return serverConfig{
		logger: slog.Default(),
		// Default actor extractor must NOT trust any header — any
		// caller can set X-Actor-Id and forge the audit trail. Wire
		// WithActorFromContext (or WithActorFromHeader for
		// reverse-proxy-stamped headers) to record real actor ids.
		actorExtractor: func(_ *http.Request) string {
			return AnonymousActor
		},
		tenantExtractor:    defaultTenantExtractor,
		maxRequestBytes:    1 << 20,
		strictAudit:        true,
		strictAuditTimeout: 5 * time.Second,
		asyncAudit:         false,
		asyncAuditWorkers:  4,
		asyncAuditQueue:    256,
		asyncAuditTimeout:  5 * time.Second,
	}
}

// startAuditWorkers spins up a bounded worker pool for async audit
// appends. Workers drain remaining entries after [Server.Stop] closes
// auditDone, then exit.
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
			// Drain remaining queued jobs after Stop signal.
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
				"tool", job.tool,
				"tenant_id", job.tenantID,
				"panic", rec,
			)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.asyncAuditTimeout)
	defer cancel()
	if err := s.appendActionLog(ctx, job.entry, job.tool, job.tenantID); err != nil {
		s.cfg.logger.Warn("mcp: async audit append failed",
			"tool", job.tool,
			"tenant_id", job.tenantID,
			"err", err,
		)
	}
}

// Stop drains in-flight async audit appends and shuts down the worker
// pool. Safe to call multiple times. No-op when async audit is not in
// use. Returns when ctx expires or the workers have drained, whichever
// comes first; remaining queued jobs after ctx expires are abandoned.
//
// Race-safety: Stop takes auditStopMu.Lock so it cannot run concurrently
// with any [Server.enqueueAuditJob] critical section. Once Stop returns
// from the locked block, every subsequent enqueue observes
// auditStopped == true and drops without touching auditQueue.
func (s *Server) Stop(ctx context.Context) error {
	if !s.cfg.asyncAudit {
		return nil
	}
	s.auditStopMu.Lock()
	if s.auditStopped.Load() {
		s.auditStopMu.Unlock()
		return nil
	}
	s.auditStopped.Store(true)
	close(s.auditDone)
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
// appends that were dropped because the bounded queue was saturated
// when the request handler tried to enqueue. Surfaces as a counter
// for operators to alert on saturation.
func (s *Server) AsyncAuditDropped() int64 {
	return s.auditDropped.Load()
}

// Tools returns a copy of the registered tool catalog, sorted by
// name for deterministic ordering. Used by `tools/list`. The
// returned slice is safe to mutate.
func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.tools))
	for _, e := range s.tools {
		out = append(out, e.tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// register installs a [toolEntry] under the given name. Returns an
// error on a duplicate name or invalid tool name.
func (s *Server) register(name string, entry *toolEntry) error {
	if name == "" {
		return errors.New("mcp: Register: tool name must not be empty")
	}
	if !validToolName(name) {
		return fmt.Errorf("mcp: Register: invalid tool name %q (must not contain whitespace)", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.tools[name]; dup {
		return fmt.Errorf("mcp: Register: tool %q already registered", name)
	}
	s.tools[name] = entry
	return nil
}

// lookup returns the dispatch entry for the named tool.
func (s *Server) lookup(name string) (*toolEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.tools[name]
	return e, ok
}

// validToolName allows non-whitespace printable ASCII plus '/'.
// JSON-RPC method names with whitespace would round-trip oddly
// through some clients, and the spec doesn't require us to support
// them.
func validToolName(name string) bool {
	for _, r := range name {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return false
		}
	}
	return true
}

// Register adds a [Handler] as an MCP tool with the given name. The
// input/output schemas are auto-generated from the In/Out types via
// reflection unless overridden by [WithInputSchema] /
// [WithOutputSchema].
//
// Registration is idempotent only across distinct names — calling
// Register twice with the same name returns an error rather than
// silently replacing the previous handler. A double registration
// almost always means a typo or a duplicate wire-up; surfacing it
// at startup is cheaper than chasing a phantom dispatch in prod.
//
// Register returns an error when:
//   - the input or output type contains a cycle (self-reference)
//     that would produce a non-terminating schema;
//   - the input or output type cannot be reflected into a schema
//     (e.g. unsupported type kinds);
//   - the name is empty or contains whitespace;
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
		o(&cfg)
	}

	var inZero In
	var outZero Out

	inSchema := cfg.inputSchema
	if len(inSchema) == 0 {
		schema, err := GenerateSchema(reflect.TypeOf(inZero))
		if err != nil {
			return fmt.Errorf("mcp: Register %q: input schema: %w", name, err)
		}
		inSchema = schema
	}

	outSchema := cfg.outputSchema
	if len(outSchema) == 0 {
		schema, err := GenerateSchema(reflect.TypeOf(outZero))
		if err != nil {
			return fmt.Errorf("mcp: Register %q: output schema: %w", name, err)
		}
		outSchema = schema
	}

	desc := cfg.description
	if desc == "" {
		desc = defaultDescription(reflect.TypeOf(inZero), name)
	}

	if cfg.destructive {
		// Vendor-extension: clients that understand the kit's MCP
		// dialect see `x-destructive: true` and can prompt for
		// confirmation. The flag is metadata; the kit's
		// httpx/middleware/approval is the actual gate.
		schema, err := withVendorExtension(inSchema, "x-destructive", true)
		if err != nil {
			return fmt.Errorf("mcp: Register %q: annotate input schema: %w", name, err)
		}
		inSchema = schema
	}

	tool := Tool{
		Name:         name,
		Description:  desc,
		InputSchema:  inSchema,
		OutputSchema: outSchema,
	}

	dispatch := buildDispatch[In, Out](h)
	return s.register(name, &toolEntry{tool: tool, dispatch: dispatch})
}

// defaultDescription derives a description from the input type's
// name. The result is intentionally bland — callers should override
// via [WithToolDescription] for anything user-facing.
func defaultDescription(in reflect.Type, name string) string {
	if in == nil || in.Name() == "" {
		return "Tool: " + name
	}
	return "Tool " + name + " (input: " + in.Name() + ")"
}

// withVendorExtension parses an existing schema, sets a top-level
// extension key, and re-emits it. Vendor extensions outside the
// JSON-Schema spec are passed through verbatim to clients that
// understand them.
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

// truncateReason caps an error message at MaxReasonLength bytes to
// keep audit rows compact. UTF-8 boundary safety: we trim at the
// last valid rune boundary rather than potentially splitting a
// multi-byte sequence.
func truncateReason(s string) string {
	if len(s) <= MaxReasonLength {
		return s
	}
	// Walk back from MaxReasonLength via DecodeLastRuneInString until
	// we land on a valid rune boundary. RuneError with size <= 1 means
	// the prefix ends mid-multibyte sequence; trim that byte and retry.
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
