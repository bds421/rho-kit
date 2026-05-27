# v2.x Implementation Plan — missing-package roadmap + saga persistence

This document is the implementation blueprint for the work flagged in the
2026-05-27 v2 readiness review. It is the source-of-truth for what each
package owes, what order to land them in, and what is out-of-scope.

Each package gets its own section with: **Goal**, **Public API sketch**,
**File layout**, **External dependencies** (with allowlist note),
**Integration points**, **Test plan**, **Risks**, and **Effort**.

Status legend:
- `[planned]` — designed, not started
- `[in-flight]` — branch/PR open
- `[done]` — merged on `main`

## Implementation order (dependency-first, simple-first)

1. **T1.1** `httpx/middleware/compress` — no deps, smallest, clear win
2. **T2.1** Prometheus exemplars in `observability/redmetrics` — in-place, no new module
3. **T1.2** `grpcx/client` — mirrors grpcx server symmetrically
4. **T1.3** `infra/sqldb/readreplica` — contained
5. **T1.4a** `infra/secrets` umbrella (interface + cache) — pure
6. **T2.2** `observability/pyroscope` — small, optional dep
7. **T3.1** `data/cron/pgstore` — persistent cron
8. **T1.4b** `infra/secrets/{awssm,gcpsm,vaultkv}` — one module each
9. **T4.2** webhook dispatcher (outbound)
10. **T3.2** `runtime/saga` persistence — most complex, needs state-machine design
11. **T4.1** OAuth2/OIDC client
12. **T4.3** notifications — **recommend existing**: docs pointer at
    [nikoksr/notify](https://github.com/nikoksr/notify) and
    [wneessen/go-mail](https://github.com/wneessen/go-mail); no kit
    code under `notify/` (same rationale as T4.1.5 OAuth2 refactor).

---

## T1.1 — `httpx/middleware/compress` (gzip + brotli) [planned]

**Goal.** Standard HTTP response-compression middleware with safe defaults.
Sit in the kit middleware stack between handler and serializer.

**Public API.**
```go
package compress

type Option func(*config)

// Middleware compresses qualifying responses based on Accept-Encoding.
// Defaults: gzip + brotli enabled; content-type allowlist limited to
// text/* + application/{json,xml,javascript}; min size 1 KiB; preserves
// Vary: Accept-Encoding regardless of whether body was compressed.
func Middleware(opts ...Option) func(http.Handler) http.Handler

func WithGzipLevel(level int) Option                // default gzip.DefaultCompression
func WithBrotliLevel(level int) Option              // default brotli.DefaultCompression (4)
func WithMinSize(bytes int) Option                  // default 1024
func WithContentTypes(types ...string) Option       // replaces the allowlist
func WithoutGzip() Option
func WithoutBrotli() Option
func WithLogger(l *slog.Logger) Option
```

**File layout.**
```
httpx/middleware/compress/
  doc.go
  compress.go
  compress_test.go
  example_test.go
  AGENTS.md
  CHANGES.md
```

**External deps.** Add `github.com/andybalholm/brotli` to dependency-allowlist.
Gzip uses stdlib.

**Integration.**
- Slot into `httpx/middleware/stack` Default chain (after logging, before
  handler). Keep it OFF by default in stack.Default — opt-in via
  `stack.Default(handler, logger, stack.WithCompress())`. Compression
  surprises if mandatory.
- Decision-tree row in AGENTS.md.
- Recipe in `docs/ai/http.md`.

**Test plan.**
- Accept-Encoding negotiation: prefer brotli > gzip > identity per q-value.
- Min-size threshold: small bodies pass through.
- Content-type allowlist: image/png passes through, text/html compresses.
- Already-encoded responses (Content-Encoding header present) pass through.
- Range requests with If-Range pass through (compression breaks ETag).
- Flush(): chunked transfer encoding works end-to-end.
- Vary header is set even on non-compressed responses.

**Risks.**
- `http.ResponseWriter` doesn't implement Flusher uniformly; need the
  wrapper to forward Flush(), Hijack() (for WebSocket upgrade — must pass
  through!), Push() (HTTP/2 server push).
- Brotli library has streaming-API quirks; use the writer form.
- Buffering: when below min-size threshold, must buffer until Flush() or
  end; pick a hard ceiling (e.g. 256 KiB) above which we give up and
  start streaming uncompressed.

**Effort.** ~300 LOC + tests. 4-6 hours.

---

## T2.1 — Prometheus exemplars in `observability/redmetrics` [planned]

**Goal.** Histogram observations carry `traceID` exemplars so Grafana can
deep-link a slow request from a Prometheus panel directly to its trace.

**Public API.**
```go
// New option on the existing metric constructors:
func WithExemplars() MetricsOption // and the existing WithHTTPRegisterer etc.

// The httpx/middleware/redmetrics handler automatically populates
// exemplars from the active OTel SpanContext when WithExemplars is on.
// gRPC unary/stream metrics interceptors do the same.
```

**File layout.** Modify in-place:
```
observability/redmetrics/
  metrics.go        ← add ExemplarObserver branch
  exemplar.go       ← new: trace-ID extraction from ctx
  exemplar_test.go  ← new
  doc.go            ← add # Exemplars section
```

**External deps.** None new; uses existing `go.opentelemetry.io/otel/trace`.

**Integration.**
- Backwards-compatible: histograms without WithExemplars behave
  identically.
- `httpx/middleware/redmetrics` + `grpcx/interceptor/metrics` auto-thread
  span context.

**Test plan.**
- Histogram observed with no active span: no exemplar attached.
- Histogram observed with active span: exemplar carries trace_id +
  span_id as labels.
- Concurrent observations don't corrupt exemplar state.
- Prometheus text-format scrape output contains `# {trace_id="..."}`
  exemplar comments.

**Risks.**
- `prometheus.HistogramOpts.NativeHistogramBucketFactor` + exemplars
  interact subtly; pin to classic histograms unless caller opts in.
- Exemplar storage in prom client is bounded; high-cardinality
  trace IDs need sampling.

**Effort.** ~150 LOC + tests. 3 hours.

---

## T1.2 — `grpcx/client` [planned]

**Goal.** Symmetric to `grpcx.NewServer`. Production-default gRPC client
construction with chained interceptors and mTLS auto-wire.

**Public API.**
```go
package client // import path: github.com/bds421/rho-kit/grpcx/v2/client

type Option func(*clientConfig)

// NewClient returns a *grpc.ClientConn dialing target with kit defaults:
// keepalive, default-deadline injection, recovery, retry, logging,
// metrics, and mTLS from the supplied *tls.Config (or insecure for
// loopback-only targets with explicit WithInsecure()).
func NewClient(target string, opts ...Option) (*grpc.ClientConn, error)

func WithTLSConfig(*tls.Config) Option
func WithInsecure() Option                              // loopback only, panics on non-loopback
func WithUnaryInterceptors(...grpc.UnaryClientInterceptor) Option
func WithStreamInterceptors(...grpc.StreamClientInterceptor) Option
func WithRetryPolicy(retry.Policy) Option               // uses resilience/retry
func WithDefaultTimeout(time.Duration) Option           // mirrors server-side
func WithMetricsRegisterer(prometheus.Registerer) Option
func WithLogger(*slog.Logger) Option
func WithKeepaliveParams(keepalive.ClientParameters) Option
```

**File layout.**
```
grpcx/client/
  doc.go
  client.go
  client_test.go
  interceptor/
    recovery.go
    retry.go
    logging.go
    metrics.go
    deadline.go
    tests
  AGENTS.md
  CHANGES.md
  go.mod  ← own module? Yes if it needs to be importable without server
```

**External deps.** None new. Uses google.golang.org/grpc (already allowed).

**Integration.**
- New `app/grpc.ClientFor(infra, target, opts...)` getter that pulls
  mTLS from `infra.ClientTLS` (kit-resolved client cert) automatically.
- Recipe in `docs/ai/http.md` ("Outbound gRPC calls").
- Decision-tree row.

**Test plan.**
- Loopback insecure dial works.
- Non-loopback insecure panics (mirrors httpx + redis safety).
- TLS dial with kit client cert verifies against test server.
- Retry policy retries on UNAVAILABLE, not on PERMISSION_DENIED.
- Default deadline propagates through to handler ctx.

**Risks.**
- gRPC keepalive client defaults are user-hostile; need careful tuning
  for both long-running streams and short unary RPCs.
- Streaming-RPC retry semantics are non-trivial — retry only works
  before any messages have been received; document loudly.

**Effort.** ~600 LOC + tests. 8-10 hours.

---

## T1.3 — `infra/sqldb/readreplica` [planned]

**Goal.** Route SELECT-only workloads to read replicas while keeping
writes on the primary, with automatic failover when a replica is
unhealthy.

**Public API.**
```go
package readreplica

type Config struct {
    Primary  *pgxpool.Pool        // required
    Replicas []*pgxpool.Pool      // 0 = single-pool mode (pass-through)
}

type RoutingPool struct { /* opaque */ }

func New(cfg Config, opts ...Option) (*RoutingPool, error)

// Acquire returns a connection. Honors AcquireOption to hint read/write:
//   - default: primary
//   - WithReadOnly(): a healthy replica (round-robin), falling back to
//     primary if all replicas are down
func (p *RoutingPool) Acquire(ctx context.Context, opts ...AcquireOption) (*pgxpool.Conn, error)

func WithReadOnly() AcquireOption
func WithStickyTxn() AcquireOption  // keep subsequent acquires on the
                                     // same node within a logical txn

// Health-check loop runs internally; replicas marked unhealthy after
// N consecutive failures, removed from rotation, re-probed periodically.
func WithHealthInterval(d time.Duration) Option
func WithMaxConsecutiveFailures(n int) Option
func WithLogger(*slog.Logger) Option
func WithMetricsRegisterer(prometheus.Registerer) Option

// Lifecycle: implements lifecycle.Component for Builder integration.
func (p *RoutingPool) Start(context.Context) error
func (p *RoutingPool) Stop(context.Context) error
```

**File layout.**
```
infra/sqldb/readreplica/
  doc.go
  readreplica.go
  health.go
  metrics.go
  readreplica_test.go
  example_test.go
  AGENTS.md
  CHANGES.md
  go.mod
```

**External deps.** Uses `github.com/jackc/pgx/v5` (already allowed).

**Integration.**
- `app/postgres.WithReadReplicas(...)` option on the existing
  `postgres.Module(cfg, opts...)`.
- `infra.ReadReplicaPool(infra)` getter on app.Infrastructure.
- Decision-tree row "Postgres-backed CRUD with read replicas".

**Test plan.**
- Read-only acquire round-robins across healthy replicas.
- Replica health-check failure removes from rotation; later success
  re-adds.
- All replicas down: read-only acquire falls back to primary with a
  WARN log.
- Sticky-txn keeps subsequent acquires on the same pool.

**Risks.**
- Replication lag: a read-after-write on a replica may not see the
  write. Document loudly; provide `WithReadAfterWriteWindow(d)` that
  forces primary for d after any write on this connection.
- Connection-pool sizing: each replica has its own pool; default
  per-replica pool size should be primary/N.
- pgxpool's `Acquire` returns a *pgxpool.Conn that knows its parent
  pool; our wrapper must hide that or proxy it correctly.

**Effort.** ~500 LOC + tests. 8 hours.

---

## T1.4 — `infra/secrets` + backends [planned]

**Goal.** Pluggable secret-loader (AWS Secrets Manager, GCP Secret
Manager, HashiCorp Vault KV) for secrets that arrive *as values*, not
KEKs. Distinct from `crypto/envelope/*` (which wraps DEKs).

### T1.4a — `infra/secrets` umbrella

**Public API.**
```go
package secrets

type Secret struct {
    Value     secret.String  // zeroizable
    Version   string         // backend-specific (AWS VersionId, etc.)
    FetchedAt time.Time
}

type Loader interface {
    Get(ctx context.Context, key string) (Secret, error)
}

// CachedLoader wraps any Loader with a TTL + single-flight on cache miss.
// Refresh runs in background after WithRefreshAfter; foreground fetch
// blocks only on first miss or hard expiry.
func NewCachedLoader(inner Loader, opts ...CacheOption) *CachedLoader

func WithCacheTTL(d time.Duration) CacheOption          // default 10m
func WithCacheRefreshAfter(d time.Duration) CacheOption // default 5m
func WithCacheLogger(*slog.Logger) CacheOption
func WithCacheMetricsRegisterer(prometheus.Registerer) CacheOption

// RotatingProvider exposes a credential rotation hook for SDKs that
// accept callback-style credentials (pgx PasswordProvider, go-redis
// CredentialsProvider, etc.). It wraps a Loader to surface fresh values
// on each call.
func NewRotatingProvider(loader Loader, key string) func() (string, error)
```

**File layout.**
```
infra/secrets/
  doc.go
  loader.go
  cache.go
  rotating.go
  errors.go        // ErrSecretNotFound, ErrSecretLoaderUnavailable
  loader_test.go
  cache_test.go
  AGENTS.md
  CHANGES.md
  go.mod
```

**External deps.** None for umbrella; backends pull SDKs.

**Test plan.**
- Cached loader: cache hit returns inline.
- Cached loader: stale-while-revalidate triggers background refresh.
- Cached loader: hard expiry blocks foreground until fetch completes.
- Single-flight: concurrent first misses share one upstream fetch.
- Rotating provider: each call returns the latest cached value.

### T1.4b — backends

Each backend is its own go module (matches `crypto/envelope/awskms`
pattern):

```
infra/secrets/awssm/         ← github.com/aws/aws-sdk-go-v2/service/secretsmanager
infra/secrets/gcpsm/         ← cloud.google.com/go/secretmanager
infra/secrets/vaultkv/       ← github.com/hashicorp/vault/api
```

Each ships:
- `New(client, opts...)` taking the upstream client (caller-constructed
  so they own the AWS/GCP/Vault session lifecycle).
- Compile-time check: `var _ secrets.Loader = (*Loader)(nil)`.
- Integration test under `//go:build integration` against the
  upstream's localstack / fake / vault-dev.

**External deps to add to allowlist.**
- `github.com/aws/aws-sdk-go-v2/service/secretsmanager`
- `cloud.google.com/go/secretmanager`
- `github.com/hashicorp/vault/api`

**Effort.** 6 hours umbrella + 4 hours per backend = 18 hours total.

---

## T2.2 — `observability/pyroscope` [planned]

**Goal.** Continuous CPU/memory profiling without rolling your own
pprof export loop. Plugs into the kit lifecycle so it stops cleanly on
SIGTERM.

**Public API.**
```go
package pyroscope

type Config struct {
    ServerAddress string         // required, e.g. "http://pyroscope:4040"
    AppName       string         // required, e.g. "my-service"
    Tags          map[string]string
    ProfileTypes  []ProfileType  // default: CPU + AllocObjects + InuseObjects
    UploadRate    time.Duration  // default 15s
}

// Component returns a lifecycle.Component that starts/stops the profiler.
// Registers itself via Builder.With(pyroscope.Module(cfg)).
func Module(cfg Config) app.Module

// Component for direct lifecycle wiring (non-Builder).
func Component(cfg Config, opts ...Option) (lifecycle.Component, error)

func WithLogger(*slog.Logger) Option
```

**File layout.**
```
observability/pyroscope/
  doc.go
  pyroscope.go
  module.go
  pyroscope_test.go
  AGENTS.md
  CHANGES.md
  go.mod
```

**External deps.** Add `github.com/grafana/pyroscope-go` to allowlist.

**Risks.**
- Pyroscope's Go client has a runtime overhead; document the ~1-2% CPU
  cost and the option to disable in tests via `WithoutAutoStart()`.
- Tag cardinality: warn against tagging by user-ID.

**Effort.** ~250 LOC + tests. 4 hours.

---

## T3.1 — `data/cron/pgstore` [planned]

**Goal.** Persist cron schedules in Postgres so a service restart does
not require operators to re-register jobs. Pairs with existing
`runtime/cron` scheduler.

**Public API.**
```go
package pgstore

type Store struct { /* opaque, holds *sql.DB or *pgxpool.Pool */ }

func New(db *sql.DB, opts ...Option) *Store

type ScheduleRecord struct {
    Name       string    // primary key
    Spec       string    // cron expression
    Enabled    bool
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

func (s *Store) Add(ctx context.Context, rec ScheduleRecord) error
func (s *Store) Remove(ctx context.Context, name string) error
func (s *Store) Enable(ctx context.Context, name string, enabled bool) error
func (s *Store) List(ctx context.Context) ([]ScheduleRecord, error)
func (s *Store) Get(ctx context.Context, name string) (ScheduleRecord, error)

func WithTableName(name string) Option

// runtime/cron integration:
func (s *Store) ApplyTo(scheduler *cron.Scheduler, jobs map[string]cron.Job) error
```

**File layout.**
```
data/cron/pgstore/
  doc.go
  store.go
  migrations/
    20260601000001_create_cron_schedules.sql
  store_test.go
  AGENTS.md
  CHANGES.md
  go.mod
```

**External deps.** Uses pgx (already allowed). Migration via existing
`cmd/kit-migrate` mechanism.

**Risks.**
- Schedule "Job" itself can't be persisted (it's a Go func). The model
  is: code declares a *map* of job names → handlers, and the store
  records which schedules are enabled. This matches asynq's
  worker-side model.
- Add/Remove must be idempotent for multi-replica services.

**Effort.** ~400 LOC + migration + tests. 6 hours.

---

## T3.2 — `runtime/saga` persistence [planned]

**Goal.** Remove the deferral in `runtime/saga/doc.go`. Land a durable
executor: state survives crashes, forward actions ride a queue,
compensations ride the outbox.

**Design choices.**

The current `runtime/saga` ships `Run(ctx, def, input)` which executes
everything in-process. We add:

```go
package saga

type State int
const (
    StatePending State = iota
    StateRunning
    StateCompensating
    StateCompleted
    StateFailed
)

type Instance struct {
    ID            string         // saga-instance ID (caller-supplied or UUIDv7)
    Definition    string         // Definition.Name
    State         State
    CurrentStep   int            // index of next forward step to run
    Compensated   []int          // indices of compensations already run
    Input         json.RawMessage
    StepResults   []json.RawMessage  // output of each forward step, kept for compensate
    LastError     string
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

type StateStore interface {
    Put(ctx context.Context, inst Instance) error
    Get(ctx context.Context, id string) (Instance, error)
    ListResumable(ctx context.Context, after time.Duration) ([]Instance, error)
    Delete(ctx context.Context, id string) error
}

type ExecutorOption func(*executorConfig)
type DurableExecutor struct { /* opaque */ }

func NewDurableExecutor(def *Definition, store StateStore, opts ...ExecutorOption) *DurableExecutor

// Start kicks off a new saga instance. Returns the instance ID
// after persisting State=Pending; the actual execution runs in the
// background queue-worker loop.
func (e *DurableExecutor) Start(ctx context.Context, input any) (string, error)

// Resume picks up any in-flight instance left in StateRunning or
// StateCompensating by a crashed previous process.
func (e *DurableExecutor) Resume(ctx context.Context) error

// WithQueue wires forward-action dispatch through a queue (asynq /
// river) so each step survives a crash.
func WithQueue(q QueueDispatcher) ExecutorOption

// WithOutbox wires compensation dispatch through an outbox so
// rollback events are not lost on broker outage.
func WithOutbox(o OutboxDispatcher) ExecutorOption
```

Persistence package:

```go
package sagapg // data/saga/pgstore
func New(db *sql.DB, opts ...Option) *Store
// Implements saga.StateStore.
```

**File layout.**
```
runtime/saga/
  saga.go             ← existing in-memory Run (unchanged)
  executor.go         ← NEW durable executor
  state.go            ← NEW state types
  doc.go              ← UPDATED: remove deferral, document durable path
  executor_test.go
data/saga/pgstore/
  doc.go
  store.go
  migrations/
    20260601000002_create_saga_instances.sql
  store_test.go
  go.mod
```

**Integration.**
- `data/queue/redisqueue` and `data/queue/riverqueue` already exist;
  the queue adapter is a thin shim mapping `QueueDispatcher.Enqueue` to
  `Client.Enqueue`.
- `infra/outbox` already exists; the outbox adapter wraps
  `outbox.Relay.Enqueue`.

**Test plan.**
- Forward step crashes mid-way: Resume picks up at CurrentStep.
- Compensation crashes: Resume picks up Compensated tail.
- Idempotency: re-running a completed step (because the queue
  redelivered) is a no-op.
- Step result JSON is round-trippable.

**Risks.**
- Step actions become "messages enqueued + handled later" — the action
  must be expressible as queue payload (JSON-serializable input/output).
  Document loudly: durable sagas trade closure-style steps for explicit
  data steps.
- Compensation must be idempotent (outbox can redeliver).

**Effort.** ~800 LOC + migration + tests + AGENTS.md. 12-16 hours.

---

## T4.1 — `auth/oauth2` (OAuth2/OIDC client) [planned]

**Goal.** Relying-party OAuth2/OIDC flow: discover, redirect, callback,
exchange, refresh, session.

**Public API.**
```go
package oauth2 // import path: github.com/bds421/rho-kit/auth/v2/oauth2

type Config struct {
    Issuer       string  // OIDC issuer URL (we discover endpoints)
    ClientID     string
    ClientSecret secret.String
    RedirectURL  string  // must match registered URI exactly
    Scopes       []string
    UsePKCE      bool    // default true; only off if provider can't support it
}

type Client struct { /* opaque */ }

func NewClient(ctx context.Context, cfg Config, opts ...Option) (*Client, error)

// HTTP handlers — drop into a router:
//   GET  /oauth/login    → redirects to provider
//   GET  /oauth/callback → exchanges code, sets session
//   POST /oauth/logout   → clears session
func (c *Client) Handlers() http.Handler

// Session-store interface for callback persistence.
type SessionStore interface {
    Put(ctx context.Context, sessionID string, session Session) error
    Get(ctx context.Context, sessionID string) (Session, error)
    Delete(ctx context.Context, sessionID string) error
}

type Session struct {
    UserID       string
    AccessToken  secret.String
    RefreshToken secret.String
    Expiry       time.Time
    Claims       map[string]any
}

func WithSessionStore(SessionStore) Option
func WithStateStore(StateStore) Option  // OIDC state/nonce CSRF guard
func WithLogger(*slog.Logger) Option
func WithHTTPClient(*http.Client) Option
```

**File layout.**
```
auth/oauth2/
  doc.go
  client.go
  discovery.go
  pkce.go
  session.go
  handlers.go
  client_test.go
  AGENTS.md
  CHANGES.md
  go.mod
```

**External deps.** Add `golang.org/x/oauth2` (only allowed for client
flow; not yet on allowlist).

**Risks.**
- Token refresh races: two requests refreshing in parallel must not
  both consume the refresh token.
- Provider quirks (Google's prompt= behavior; Auth0's audience parameter)
  — document loudly that this client targets the OIDC standard; provider
  adapters live in `auth/oauth2/contrib/{auth0,keycloak,cognito}`.

**Effort.** ~700 LOC + tests. 12 hours.

---

## T4.2 — webhook dispatcher (outbound) [planned]

**Goal.** Send outbound webhooks with retry, HMAC signing, and
replay-protection nonces. Pairs with the existing
`httpx/middleware/signedrequest` (which is the *receiver* side).

**Public API.**
```go
package webhook // import path: github.com/bds421/rho-kit/httpx/v2/webhook

type Dispatcher struct { /* opaque */ }

type Config struct {
    HTTPClient *http.Client     // required (use httpx.NewResilientHTTPClient)
    Signer     signing.Signer   // from crypto/signing
}

type Delivery struct {
    URL         string
    Body        []byte
    ContentType string
    Headers     http.Header  // additional caller-supplied headers
    IdempotencyKey string    // optional; signed into the request
}

func New(cfg Config, opts ...Option) *Dispatcher

func (d *Dispatcher) Send(ctx context.Context, del Delivery) error

// Async path: queue + retry policy.
func (d *Dispatcher) Enqueue(ctx context.Context, del Delivery) error
func WithQueue(q QueueDispatcher) Option        // shared interface with saga
func WithRetryPolicy(retry.Policy) Option
func WithSigningHeader(name string) Option      // default "X-Signature"
func WithTimestampSkew(d time.Duration) Option  // default 5m; receiver-aligned
func WithLogger(*slog.Logger) Option
func WithMetricsRegisterer(prometheus.Registerer) Option
```

**File layout.**
```
httpx/webhook/
  doc.go
  dispatcher.go
  signing.go
  webhook_test.go
  AGENTS.md
  go.mod
```

**External deps.** None new.

**Effort.** ~500 LOC + tests. 8 hours.

---

## T4.3 — notifications (email / SMS / push / chat) [recommend-existing]

**Decision.** The kit does NOT ship its own notify umbrella. Use the
audited ecosystem libraries directly:

  - [github.com/nikoksr/notify](https://github.com/nikoksr/notify)
    — multi-channel Notifier umbrella + ~25 service backends (email,
    SMS, push, Discord, Slack, Telegram, MS Teams, Pushbullet, etc).
  - [github.com/wneessen/go-mail](https://github.com/wneessen/go-mail)
    — modern email-only library on top of net/smtp, with stronger
    MIME handling, DKIM, and explicit TLS controls than the stdlib.
  - Provider-native SDKs (aws-sdk-go-v2 SES/SNS, sendgrid-go, twilio-go,
    firebase.google.com/go/messaging, sideshow/apns2) for callers
    needing per-message delivery feedback or provider-specific features.

**Rationale.** Same as T4.1.5 (the OAuth2 refactor): writing a custom
notify umbrella would re-implement message composition, SMTP/HTTP
quirks, retry semantics, MIME, transport security, and provider-specific
auth — every line of that is a security-bug surface that no single
project can match against established, audited libraries.

**Kit value-add (documentation only).** The root AGENTS.md decision
tree row points callers at nikoksr/notify or go-mail with the trade-off
notes. No kit code ships under `notify/`. Services wire whichever
library matches their channel set and own the dep weight.

**Effort.** ~30 minutes (documentation only — done in commit that
adopts this approach).

---

## Cross-cutting requirements (every new package)

For each new package the implementation MUST include:

1. **doc.go** with the "USE THIS WHEN / DO NOT USE FOR / sibling
   packages" structure established in `observability/auditlog`.
2. **AGENTS.md** in the package directory (per-package agent guide).
3. **CHANGES.md** for the v2.0 changelog entries.
4. **go.mod** (if it's a new module) and entry added to `go.work`.
5. **Allowlist update** for every new direct external dependency.
6. **Decision-tree row** in root `AGENTS.md`.
7. **Recipe section** in `docs/ai/<topic>.md`.
8. **Unit tests + AGENTS.md operational-readiness entry**.
9. **`make check-dependency-allowlist` + `make check-dependency-boundaries`
   pass.

The kit's existing `make release-candidate` target enforces most of these.

## Effort summary

| Item     | LOC est. | Hours est. |
|----------|---------:|-----------:|
| T1.1 compress           |  300 |  4-6  |
| T2.1 exemplars          |  150 |  3    |
| T1.2 grpcx/client       |  600 |  8-10 |
| T1.3 readreplica        |  500 |  8    |
| T1.4a secrets umbrella  |  300 |  6    |
| T2.2 pyroscope          |  250 |  4    |
| T3.1 cron/pgstore       |  400 |  6    |
| T1.4b secrets backends  | 900  | 12    |
| T4.2 webhook dispatcher |  500 |  8    |
| T3.2 saga persistence   |  800 | 12-16 |
| T4.1 oauth2/oidc        |  700 | 12    |
| T4.3 notify (docs only — recommend nikoksr/notify + wneessen/go-mail) | 0 | ~0.5 |
| **TOTAL**               | **~5400** | **~86-106** |

That's ~3 weeks of sustained focused work for a single experienced
engineer, or 6-8 weeks alongside other responsibilities. This document
is the source of truth so any session — human or AI — can pick up at
the next `[planned]` item.
