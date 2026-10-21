# Changelog

## v2.19.0 — 2026-10-22

- **feat(observability/auditlog): free-text search.** `Filter.Search` keeps
  events whose actor, action, resource or metadata values (not keys) contain
  the term, ignoring case; `auditlog.SearchText` and `MatchesSearch` define
  the match for every store. `Logger.List` rejects terms over
  `MaxSearchBytes` (256) with `ErrSearchTooLong`. Keyset paging is unchanged:
  the term is one more condition beside the cursor.
- **feat(observability/auditlog/postgres): `search_text` with a trigram
  index.** Migration 20261022000001 creates `pg_trgm` if missing (a trusted
  extension: the migrating role needs CREATE on the database, or the
  extension must already be on the search_path), adds `search_text`, fills it
  for existing rows and indexes it with GIN `gin_trgm_ops`; `Query` matches
  it with `ILIKE`. The store writes the column on append, so apply the
  migration before deploying this version. `search_text` is derived for
  search and not part of the HMAC chain.

## v2.18.0 — 2026-10-21

- **feat(httpx/webapp): serve a built single-page web app.** `webapp.New`
  serves an `fs.FS` (usually `go:embed`): files by path and `index.html` for
  every other path; precompressed `.br`/`.gz` siblings by `Accept-Encoding`
  (br wins a tie) with `Content-Encoding` and `Vary`; a strong ETag per
  coding; `WithImmutable` prefixes cached for a year, where a missing file is
  404 instead of the app. GET and HEAD only.
- **feat(httpx/stack): `WithStaticPaths`.** GET and HEAD requests under the
  named path prefixes skip the timeout middleware, so large built files
  stream instead of going through its 1 MiB buffer (which answers 500 above
  it).
- **fix(httpx/compress): equal weights pick the registered encoder.** Among
  codings the client accepts with the same q, the encoder registered first
  wins, as `WithEncoder` documents; before, the client's order did, so
  `gzip, deflate, br` got gzip with brotli registered first. The negotiation
  moved to `httpx/internal/acceptencoding`, shared with `webapp`.

## v2.17.0 — 2026-10-21

- **feat(data/queue/riverqueue): scheduled jobs and a pending check.**
  `Publisher.EnqueueAt` and `EnqueueTxAt` insert a job that runs no earlier
  than the given time (River `ScheduledAt`), inside the caller's
  transaction for the Tx variant; dedupe by `Message.ID` covers scheduled
  jobs. `Publisher.Pending` and `PendingTx` report whether a job with a
  message ID still waits, is scheduled, runs or retries, so a caller can find
  work River discarded and enqueue it again.

## v2.16.3 — 2026-10-21

- **fix(httpx/timeout): a response replaced by 500 for exceeding the
  buffer is logged.** The timeout middleware answers 500 when a handler's
  response is larger than its buffer (1 MiB unless `WithMaxBufferSize`); it
  now logs that at error level with the limit, on the configured logger or
  `slog.Default`. Before, the 500 carried no cause in the logs.
- **build: Go 1.27.3** (GO-2026-6809, crypto/x509 distrusts compromised
  intermediates).
- **deps: github.com/rabbitmq/amqp091-go v1.14.0** (GO-2026-6700, frame
  limit before negotiation) in `app/amqp`, `infra/messaging/amqpbackend` and
  the test kits.

## v2.16.2 — 2026-10-21

- **fix(app): the public listener defaults to every interface with IPv6 and
  IPv4.** `SERVER_HOST` defaults to empty (`:PORT`, one dual-stack socket)
  instead of `0.0.0.0`. `ServerConfig.Addr` and `InternalConfig.Addr`
  bracket IPv6 literals (`::1` gives `[::1]:PORT`; bracketed hosts are taken
  as is); before, `SERVER_HOST=::1` produced an address that did not parse.

## v2.16.1 — 2026-10-21

- **fix(httpx, app/http): `WithReloadingTLS` no longer panics in the default
  HTTP client module.** The kit transport accepted no `InsecureSkipVerify`,
  also not the reloading client config from `netutil.ReloadingClientTLS`,
  which verifies chain and hostname against the live CA pool in
  `VerifyConnection`. Kit HTTP clients now accept that form, like the NATS
  and AMQP backends; `InsecureSkipVerify` without `VerifyConnection` still
  panics.

## v2.16.0 — 2026-10-19

- **feat(auth/oauth2): `WithSignInHook` and `Session.Attributes`.** An
  application resolves the person in its own directory once per sign-in with
  the fresh, verified ID token and keeps the result on the session; the hook
  can refuse the sign-in (`ErrSignInRefused`, 403). The memory and Redis
  session stores keep the attributes.
- **feat(app/oidc): `SessionAttributes`.** Reads the attributes of the
  request's session in handlers, like `SessionClaims`.

## v2.15.0 — 2026-10-19

- **feat(httpx/middleware/stack): `WithTrustedProxies`.** The access log
  records the client from X-Forwarded-For when the request comes from a
  trusted reverse proxy, instead of logging the proxy for every request.

## v2.14.0 — 2026-10-19

- **feat(httpx/middleware/stack): `WithLogAttrs`.** Adds per-request
  attributes to the access log and to the request logger, for example the id
  of the API key a request authenticated with.

## v2.13.1 — 2026-10-19

- **fix(security/netutil): `WithAllowedNetworks` accepts the IPv6 loopback**
  (`::1/128`); it lies inside the deprecated IPv4-compatible range but carries
  no IPv4 address.

## v2.13.0 — 2026-10-19

- **feat(infra/sqldb/pgx): `Config.PrepareConn`.** Runs whenever a connection
  is taken from the pool, with the caller's context, so per-request session
  state such as the tenant read by row-level security policies is set on
  every checkout without touching call sites.

## v2.12.1 — 2026-10-19

- **fix(security/netutil): narrower `WithAllowedNetworks`.** IPv6 networks
  must be /48 or narrower, and ranges that carry IPv4 addresses
  (IPv4-mapped and -compatible, NAT64, Teredo, 6to4) are refused; in v2.12.0
  a wide IPv6 network could reach arbitrary IPv4 hosts through them. Do not
  use `WithAllowedNetworks` from v2.12.0.

## v2.12.0 — 2026-10-19

- **feat(security/netutil): `WithAllowedNetworks`.** The SSRF guard can allow
  private or reserved addresses inside named networks (no wider than /8 or
  /16) while every other private range stays blocked; the narrow alternative
  to `WithAllowPrivateIPs` for internal targets such as an on-premise SIEM.

## v2.11.0 — 2026-10-19

- **feat(observability/auditlog): `Logger.Tail` for log shipping.** Reads
  events in append (chain) order after a [`Position`], optionally for one
  tenant, and returns the position of the last event, so a shipper can
  forward every event exactly once and resume after a crash. Implemented by
  the memory and Postgres stores (`seq` column); other stores return
  `ErrTailUnsupported`.

## v2.10.1 — 2026-10-19

- **feat(httpx/middleware/stack): `WithWebSocketPaths`.** The stack wraps
  every route in the buffering timeout middleware, so a WebSocket handler
  behind it (realtime/centrifuge) answered 500 because the connection could
  not be hijacked. WebSocket upgrades on the named exact paths now bypass
  the timeout; upgrades elsewhere and plain requests on those paths keep it.

## v2.10.0 — 2026-10-19

Supersedes the v2.9.0 tags of `security`, `realtime/centrifuge`, `infra`,
`httpx` and `infra/outbox/postgres`. Those tags were created from branches
that were never merged into `main`; do not depend on v2.9.0. All changes
are additive except the timeout overflow fix.

- **fix(observability/auditlog/postgres): append-only roles.** Appends and
  retention serialise on the chain advisory lock only; the tail read no longer
  uses `SELECT FOR UPDATE`, so the table can be granted to application roles
  as INSERT/SELECT. Retention now runs inside that lock as well.
- **fix(observability/auditlog/postgres): verify events with JSON metadata.**
  The store keeps the exact signed metadata bytes in a new `signed_metadata`
  column (migration 20261019000001) and returns them for verification and
  queries, because JSONB normalizes formatting and numbers. Previously the
  first event carrying metadata failed verification. New events are signed
  with `FormatExactMetadata`, which binds the exact metadata bytes and the
  format version into the HMAC, so moving a new event onto the legacy
  encoding is detected. Rows from before the migration verify as before.
- **fix(app/tracing, observability): OpenTelemetry OTLP exporters v1.45.0**
  (GO-2026-6505).
- **test(infra/storage/storagetest): MinIO conformance tests are opt-in.**
  The pinned MinIO images are no longer published, so they run only with
  `-tags "integration minio"` against an image you provide.
- **feat(security/jwtutil): `SigningProvider.PublicJWKS()`.** Exports the
  current public signing key as a JWKS document with the kid that `Sign`
  stamps, for in-process verifiers or a JWKS endpoint.
- **feat(infra/outbox): `Relay.Wake()`.** Nonblocking, coalescing poll hint
  for use after commit or from a LISTEN/NOTIFY listener. Periodic polling
  stays authoritative.
- **feat(infra/outbox/postgres): `NewWithOptions` + `WithClaimRoutes`.**
  Restricts claiming, pending counts, stale recovery and retention to exact
  (topic, routing key, message type, schema version) tuples. `New` is
  unchanged and still claims everything.
- **feat(security/jwtutil): `WithSigningStringClaims` +
  `SigningProvider.SignWithStringClaims`.** Issuer-side counterpart of
  `WithStringClaims`: a bounded allowlist of extra string claims. `Sign` and
  `SignContext` are unchanged.
- **feat(realtime/centrifuge): lifecycle hooks and limits.**
  `WithVerifiedConnectPolicy`, `WithVerifiedRefreshPolicy` (client-side
  refresh with re-verification), `WithConnectObserver`,
  `WithDisconnectObserver`, `WithSubscriptionReadyObserver`,
  `WithPublicationWritePolicy`, `WithClientQueueMaxSize`,
  `WithClientChannelLimit`, `WithMaxMessagesInFrame`, `Node.Ready`.
  `github.com/centrifugal/protocol` becomes a direct dependency.
- **feat(httpx): native HTTP/2 configuration.** `NewServer` sets
  `http.Server.HTTP2`/`Protocols` instead of the deprecated
  `x/net/http2.ConfigureServer`; the TLS stream and frame limits are
  unchanged and caller HTTP/2 settings are preserved.
- **fix(httpx/middleware): flush errors survive recorders.**
  `ResponseRecorder.FlushError` and `SupportsNativeFlush` let
  `http.ResponseController.Flush` report the underlying error.
- **fix(httpx/middleware/timeout): oversized buffered responses fail.**
  Behaviour change: a response over the buffer cap is now replaced by a
  JSON 500 instead of a truncated 200, even if the handler ignores
  `ErrResponseTooLarge`.
- **chore: `golang.org/x/net` v0.60.0 in httpx; workspace toolchain and CI
  `go-version` go1.27.2.** Minimum `go` directive stays 1.26.8.
- **feat(infra/sqldb/migrate): `Config.TableName`.** Selects the goose
  version table so several independently owned migration sets can share one
  database. Empty keeps `goose_db_version` (`migrate.DefaultTableName`).
  The name must be an unquoted, optionally schema-qualified identifier.
- **feat(app/postgres): `WithMigrationSet(table, fs)`.** Registers further
  migration sets, each tracked in its own version table, applied in
  registration order during `Init`. `WithMigrations` is unchanged; a table
  registered twice panics at `Module`.
- **feat(realtime/centrifuge): history recovery, shared backends, server
  publish.** `WithSubscriptionRecovery` sets native recovery/positioning per
  channel after authorization succeeded (denials and metrics unchanged).
  `WithBackend` + `RedisBackend` install a broker/presence manager in
  `Start` for multiple replicas and close it after `Stop`; non-loopback
  Redis needs TLS and a password unless `PlaintextUnsafe`. The Redis
  implementation is part of the existing centrifuge module, so no new direct
  dependency. `Node.Publish` wraps the native publish with redacted errors.
- **feat(auth/oauth2): token refresh and RP-initiated logout.**
  `WithTokenRefresh(leeway)` refreshes an expiring access token inside
  `SessionFromRequest` (single-flight per session, persisted, refreshed ID
  token verified for the same subject). A 4xx refusal or invalid ID token
  deletes the session and returns `ErrSessionNotFound`; transport/5xx
  failures keep an unexpired session. `WithRPInitiatedLogout(uri)` makes
  `POST /oauth/logout` answer 200 with `LogoutResponse{LogoutURL}` built from
  the discovered `end_session_endpoint`; without it logout stays 204.
  `Session.IDToken` records the raw verified ID token. `golang.org/x/sync`
  becomes a direct dependency.
- **feat(auth/oauth2/redis): `WithEncryption(keys...)`.** AES-256-GCM (via
  `crypto/encrypt`) for stored sessions and login state, bound to the Redis
  key; the first key encrypts, all keys decrypt. Off by default,
  recommended. Unencrypted or retired-key entries read as absent. The store
  also persists `Session.IDToken`.
- **feat(app/oidc): `SessionClaims` / `DecodeSessionClaims`.** Verified
  ID-token claims of the browser session, including object/array claims.
- **feat(security/identity): `MappingProfile.AllowMissingClaims`.** Absent
  selected claims stay empty instead of failing; malformed claims still
  fail. Default unchanged.
- **feat(app): `Builder.WithoutPublicServer()`.** Worker roles run without a
  public HTTP listener, router or public middleware; the internal
  health/readiness/metrics server, modules, background goroutines and
  runner-attached components still run. Rate-limit and public-TLS gates
  and `BaseConfig.Server` validation are skipped; `Router` and rate-limit
  modules are rejected. Default behaviour is unchanged.
- **fix(auth/oauth2): token refresh cannot undo a logout.** Refresh now
  writes through the optional `SessionUpdater.Update(ctx, id,
  expectedVersion, sess, ttl)` compare-and-set (new `Session.Version`). If
  the session was deleted or rewritten while the refresh was in flight, the
  refreshed tokens are discarded and the request is unauthenticated (a
  concurrent refresh's result is reused). `MemorySessionStore` implements it
  under its mutex, `auth/oauth2/redis` with WATCH/MULTI. Third-party stores
  without it fall back to re-checking existence before Put, which narrows
  but does not close the race.
- **feat(auth/oauth2): session security controls.** All opt-in; defaults
  unchanged.
  - `WithAbsoluteSessionLifetime(d)`: sessions end at `Session.CreatedAt+d`;
    refresh, sliding TTL and cookie expiry never go beyond it. Sessions
    without `CreatedAt` fail closed while it is set.
  - `WithIdleTimeout(d)`: sessions end after d without user-initiated
    requests. `Session.LastActivity` is written at most once per minute and
    only by compare-and-set (`NewClient` fails without a `SessionUpdater`
    store). `WithBackgroundRequest(ctx)` or the `X-Background-Request: 1`
    header (`WithBackgroundRequestHeader`) mark background traffic that
    does not extend the window.
  - `WithRefreshOutageGrace(d)`: transport/5xx refresh failures keep a
    session up to d past access-token expiry; 4xx still ends it.
  - `RefreshLocker` optional store interface + `WithRefreshLockWait(d)`:
    token refresh is serialized across replicas; a waiting replica reuses
    the other replica's tokens.
  - `WithBackChannelLogout()`: `POST /oauth/backchannel-logout` per OpenID
    Connect Back-Channel Logout 1.0 (signature, iss, aud, exp, iat window,
    jti replay, events, sid/sub, no nonce). New `SessionIndex` and
    `ReplayCache` store interfaces, `MemoryReplayCache`,
    `Client.DeleteSessionsBySubject`, `WithSessionRevocationHook`,
    `WithReplayCache`, `ErrSessionIndexUnsupported`, `Session.SID`.
  - Step-up: `Session.AMR`, `Session.AuthTime`; `/oauth/login` accepts
    allowlisted `prompt` and `max_age`, and the callback enforces max_age
    against auth_time (`ErrStaleAuthentication`, `StateEntry.MaxAge`).
- **feat(auth/oauth2/redis): session index, refresh lock, replay cache.**
  Hashed subject/sid index sets maintained with the session write,
  `TryLockRefresh` (SET NX PX, token-checked release), `Remember` (SET NX),
  and persistence of the new session fields.
- **feat(app/oidc): `WithBackChannelLogout` and `SessionAuthentication`.**
  Mounts the back-channel logout route; exposes amr/auth_time of the
  session for step-up checks.
- **feat(infra/sqldb/pgx): transaction in context.** `WithTx`,
  `TxFromContext`, `Querier` (sqlc pgx/v5 `DBTX` method set),
  `Pool.Querier(ctx)` and `Pool.InTx(ctx, fn)` (commit on nil, rollback on
  error or panic, nested calls join). `infra/outbox/postgres`
  `WithTx`/`TxFromContext`/`RequireTx` now delegate to it with the same
  context key (API unchanged), so outbox writes inside `InTx` join the
  transaction.

## v2.8.0 — 2026-09-29

Security maintenance: Go toolchain, gRPC and x/crypto, plus a localbackend
fix for Go ≥ 1.26.6. Minor release because the minimum Go version every
consumer must satisfy rises (1.26.2 → 1.26.8); no API changes. govulncheck
is clean in every workspace module.

- **fix(infra/storage/localbackend): escape detection broken on Go ≥ 1.26.6.**
  Go 1.26.6 changed how `os.Root.MkdirAll` reports a path component that is
  itself a symlink escaping the root. The rejection is now nested
  (`PathError{mkdirat, PathError{statat, "path escapes from parent"}}`), and
  `isEscapeError` only matched the outermost `PathError`. The write was
  **still refused** (os.Root held, and no bytes landed outside the root), but
  the refusal was misreported as a generic `create dirs failed` instead of the
  redacted `unsafe parent` error. As a result, `TestLocalBackend_Put/rejects_symlinked_parent`
  and both `TestTOCTOU_EscapingSymlinkComponentRefused` write cases failed on
  every patched toolchain. CI stayed green only because it ran Go 1.26.5.
  `isEscapeError` now walks the whole wrap chain. The new
  `TestIsEscapeError_ErrorShapes` pins both error shapes independently of the
  toolchain running the tests.
- **security(go): minimum Go 1.26.2 → 1.26.8 across all 111 modules;
  workspace toolchain go1.27.1; CI `go-version` 1.26.5 → 1.27.1.** govulncheck
  reported reachable standard-library advisories in every gRPC-using module:
  GO-2026-5026, -5037, -5039, -5856, -5972, -6090 and -6218 (`net/http`,
  `crypto/tls`, `net/url`, `encoding/asn1`, ...), all fixed in Go ≤ 1.26.6. The
  workspace pinned `toolchain go1.26.2` and CI ran 1.26.5, so builds, tests
  and vulncheck all ran on affected toolchains. Modules declare 1.26.8 (the
  lowest release clearing every advisory) rather than 1.27, so consumers are
  not forced onto a new major toolchain.
- **security(grpc): google.golang.org/grpc v1.83.0 → v1.83.2** in `grpcx`,
  `observability`, `app/tracing`, `app/grpc`, `crypto/envelope/gcpkms`,
  `infra/secrets/gcpsm`, `infra/leaderelection/etcd` and
  `infra/storage/gcsbackend`. Fixes GO-2026-6443 (server panic via a missing
  `:authority`/Host header; reachable from `app/grpc`) and GO-2026-6348.
  v1.83.2 is deliberately chosen over v1.84.0: the 1.84 line has no fixed
  stable release for GO-2026-6443 yet. Move to the first stable ≥ v1.85.0.
- **security(x/crypto): golang.org/x/crypto → v0.57.0** in the 30 modules that
  required an older version. Fixes GO-2026-6354 and GO-2026-6355 (fixed in
  v0.56.0), reachable from `infra/storage/sftpbackend`.
- **chore(tooling): golangci-lint v2.10.1 → v2.14.0, govulncheck v1.1.4 →
  v1.8.0.** Both older releases bundle an `x/tools` that cannot read Go 1.27
  export data (`export data version 4 is greater than maximum supported
  version 2`), so every lint run failed with typecheck errors on the new
  toolchain. v2.14.0's staticcheck flagged one new QF1001 in
  `riverqueue.validListenerSchema`; rewritten with De Morgan's law, same
  semantics (pinned by the existing `Public`/`1public` rejection cases).
  It also flagged SA1019 on `pyroscope.Config.AuthToken` (deprecated upstream
  in favour of `BasicAuthUser`/`BasicAuthPassword`). It is suppressed with a
  reason, because `observability/pyroscope.Config.AuthToken` is this kit's
  documented bearer-token API. **Follow-up:** add basic-auth fields before
  pyroscope-go removes `AuthToken`.
- **chore(riverqueue): tidy + allowlist `github.com/riverqueue/river/riverdriver`.**
  `dfe5ae9b` (listener schema) imports `riverdriver` directly but left it
  `// indirect`, so `check-tidy` failed on `main`. Tidying makes it a direct
  dependency; it is added to `docs/audit/dependency-allowlist.txt` next to the
  already-approved `river`, `riverpgxv5` and `rivertype` (same upstream
  project). **Needs reviewer sign-off per the allowlist policy.**

## v2.7.2 — 2026-09-04

Patch release for the HTTP middleware stack (module tag `httpx/v2.7.1`; no
other module changes).

- **fix(httpx/middleware/metrics).** The route-pattern slot that `CaptureRoute`
  fills for the metrics and tracing middleware was a plain string shared across
  goroutines. Under `timeout.Timeout` the inner chain runs on its own goroutine
  and the outer chain returns the moment the request context is cancelled
  (client disconnect, deadline), so `finishHTTPSpan` and the metrics `defer`
  could read the slot while `CaptureRoute` was still writing it: a data race,
  observed as a torn string read in a consumer's `-race` suite
  (sigma-tkgd `TestReplication_EndToEnd_Converges`, a replica long-poll
  cancelled at shutdown). The slot now holds the pattern behind an
  `atomic.Pointer[string]` with `set`/`get` accessors; the three call sites are
  unchanged in behaviour. No API change. Regression probe
  `TestCaptureRoute_ConcurrentOuterReadIsRaceFree` races an outer read against
  the capture 200 times and fails under `-race` on the previous code.

## v2.7.1 — 2026-08-06

Patch release for role-isolated River workers.

- **fix(riverqueue).** Add `DriverFromPoolWithListenerSchema` so workers can
  retain role-local leadership tables while subscribing to the shared
  LISTEN/NOTIFY namespace used by producers. The listener schema is an exact,
  validated unquoted PostgreSQL identifier and does not change executor,
  migration, or leadership schema selection.

## v2.7.0 — 2026-08-03

Object-storage exact-generation release (coordination tag `release/v2.7.0`).
Additive only; no v2 API or behavior changes for existing callers.

- **feat(storage).** Add paged exact-generation enumeration
  (`ExactVersionPageLister`, `ExactVersionCursor`, `ExactVersionPage`) as the
  recovery and export complement to the bounded legal-erasure listers. Cursors
  are opaque and bind key plus generation; delete markers and tombstones count
  against the page limit, and backends fail closed when a provider reports
  truncation without an advancing cursor. Implemented for the memory, local,
  and S3 backends and forwarded through the retry, circuit-breaker, and
  encryption decorators.
- **feat(storage).** Add `versionrelocation`, an opt-in decorator that resolves
  historical exact-generation identities to their current physical generation
  through a caller-owned resolver. Unversioned operations and physical
  enumeration remain unchanged; resolvers may move a generation but never
  rewrite an object key.
- **fix(storage/s3).** Use `WhenRequired` response checksum validation for
  explicitly configured S3-compatible endpoints, which commonly omit optional
  response checksum headers and otherwise produced one SDK warning per
  successful `GetObject`. Native AWS keeps its SDK/operator-selected policy.
- **chore(deps).** Refresh every direct external dependency to its latest
  minor/patch release, including the AWS SDK, smithy-go, gRPC, Prometheus
  client, jwx, goose, River, Kubernetes client, and the Google Cloud SDKs.

## v2.6.0 — 2026-07-22

Platform-foundations release (coordination tag `release/v2.6.0`).

- **feat(identity).** Add a canonical authenticated principal and consistent
  HTTP/gRPC, authorization, and audit-actor projection.
- **feat(auth).** Add standards-based OIDC relying-party wiring, hardened
  OAuth2 browser-flow state storage, and client-credentials lifecycle support.
- **feat(contracts).** Add versioned contract artifacts, compatibility checks,
  CI validation, and generated-service contract scaffolding.
- **feat(messaging).** Add a durable PostgreSQL transactional inbox paired
  with outbox schema-version handling and Docker-backed recovery coverage.
- **feat(tooling).** Add production scaffolding, contract and OIDC doctor
  rules, catalog reporting, ADRs, and release verification guidance.

## v2.5.1 — 2026-07-22

Patch release for S3-compatible object storage.

- **fix(storage/s3).** Keep large `PutObject` requests compatible with custom
  endpoints that reject optional AWS chunked checksums, while preserving the
  SDK/operator request and response checksum policies for native AWS S3.

## v2.5.0 — 2026-07-21

Repository-wide review-remediation and hardening release (coordination tag
`release/v2.5.0`). This release contains intentional v2 API and behavior
changes; downstreams should review the affected package documentation before
upgrading.

- **security and transport.** Harden JWT issuer/audience policy, HTTP and gRPC
  service identity and entitlement propagation, secret handling, KMS adapters,
  webhook SSRF protection, websocket liveness, and fail-closed middleware.
- **data and messaging.** Make tenant idempotency keys unforgeable, require
  atomic tenant approval mutations, correct budget refund windows, reject
  unknown message schemas by default, and return lifecycle errors from queue
  and stream consumers.
- **runtime and infrastructure.** Harden claim leases, leader callback drains,
  SQL pools, Redis locks, secrets caches, storage bounds, journal safety, saga
  recovery, retry redaction, audit chains, and readiness caching.
- **API cleanup.** Unify the redislock option export surface, make package-level
  websocket handling track connections by default, and retire dead storage
  capability APIs.
- **release engineering.** Synchronize all 107 workspace modules after the
  upstream dependency bump, enforce tidy-command failures, standardize Go
  1.26.5, and make the Redis queue double-process guard deterministic.
- **documentation.** Close completed review/backlog trackers while retaining
  durable operational runbooks and dashboard guidance.

## v2.4.0 — 2026-07-18

Additive storage and portability-fix release (coordination tag
`release/v2.4.0`). No breaking changes.

- **feat(storage).** Add portable, checksummed S3 multipart uploads with
  bounded part spooling, idempotent complete/abort, stale-upload pagination,
  retry/circuit-breaker capability preservation, and Docker MinIO plus
  opt-in AWS conformance tests.
- **ci.** Run release, CI, dashboard, and supply-chain workflows on the patched
  Go 1.26.5 toolchain.
- **fix(approval/postgres).** Normalize decision timestamps to PostgreSQL's
  microsecond precision before returning them, so idempotent reads reproduce
  the original decision exactly.
- **fix(auditlog).** Normalize event timestamps before chain signing so the
  PostgreSQL adapter can verify HMAC chains after a timestamp round trip.

## v2.3.1 — 2026-06-28

Patch release (coordination tag `release/v2.3.1`).

- **fix(auth).** Remove `looksLikePrefixedMachineToken` session skip heuristic that
  falsely rejected one-dot session bearer tokens with `usr_` prefixes or
  base64url underscores. Machine credentials still fall through via wire-shape
  (no dot) and prefix-specific chain strategies.

## v2.3.0 — 2026-06-28

Additive feature release across the `/v2` module set (coordination tag
`release/v2.3.0`). No breaking changes.

- **feat(auth).** Subject/Actor identity split for HTTP and gRPC; shared
  `security/identity` package; JWT service-actor mapping; gRPC metadata
  propagation; `FormatActorFromContext`; kit-doctor drift rule + autofix.
- **feat(auth).** OAuth access-token authenticator; unbound scoped API keys;
  `jwtutil.NormalizeSubjectID` for prefixed subject wire forms.

## v2.1.0 — 2026-06-10

Additive feature + fix release across the `/v2` module set (coordination tag
`release/v2.1.0`).

- **feat(apikey).** External / customer-facing API key support (issuance,
  hashing, lookup) with the `data/apikey/postgres` store and `app/apikey`
  Builder wiring.
- **fix(saga).** `runtime/saga` durable executor now resumes in-flight sagas
  concurrently with a bounded worker pool instead of serially.
- **chore(deps).** Workspace-wide minor/patch dependency bumps via Dependabot.

## v2.0.3 — 2026-06-08

Per-module patch. The `runtime` module is tagged at `runtime/v2.0.3` carrying
the saga concurrency fix ahead of the coordinated `v2.1.0` cut; not all modules
ship a `v2.0.3` tag.

## v2.0.2 — 2026-05-28

Patch release (coordination tag `release/v2.0.2`).

- **fix(resilience/bulkhead).** Decrement the in-flight counter before
  releasing the semaphore so concurrency accounting stays correct under load.
- **fix(data/cache).** `Delete` now calls `Wait()` like `Set` to drain the
  Ristretto buffer, so deletes are observable immediately.
- **chore(deps).** `golang.org/x/crypto` and `go-jose/v4` bumps; Dependabot
  enabled for version + security updates.
- **ci.** Dropped the CycloneDX SBOM workflow and `SUPPLY_CHAIN.md`; added a Go
  build cache.

## v2.0.1 — 2026-05-28

Release-engineering patch (coordination tag `release/v2.0.1`); no functional
code changes.

- Fixed stale `go.mod` pseudo-version pins so dependent modules resolve the
  versioned tags directly.
- Added a `check-tidy` gate to catch stale `go.mod` require lines.
- Allowlisted `go-jose/v4` (auth/oauth2 tests) and `gcpsm` / `gcpkms` for the
  `google.golang.org/api` boundary.

## v2.0.0 — 2026-05-12

The agentic-AI service backend release. Every `go.work` module is tagged at
`/v2` using Go semantic import versioning. Public API breaks vs. v1.x are
intentional — see [`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md) for
the full enumeration of breaking changes and the operational migration
sequence.

### Themes

- **Multi-tenant substrate.** Tenant-aware cache, idempotency, rate-limiter,
  and label-cardinality guard land as first-class primitives.
- **Cost and audit.** Per-tenant cost budgets (memory + Redis backends, inbound
  middleware, outbound `RoundTripper`), append-only signed action log, and an
  approval workflow.
- **MCP helpers.** Typed handlers as JSON-RPC tools with schema
  auto-generation; reuse the kit's full middleware stack.
- **Supply-chain hardening.** `govulncheck` + `osv-scanner`
  CI, direct dependency allowlist, heavy-SDK boundary gate, and threat
  model in `docs/audit/`. See
  [`docs/audit/THREAT_MODEL.md`](docs/audit/THREAT_MODEL.md).
- **Operational primitives.** RED metrics, Grafana dashboards (HTTP, gRPC, DB,
  Redis, Outbox, AMQP, rate-limit, storage), runbooks, `promtool` CI, and an
  operational-readiness coverage gate for every workspace module.
- **Crypto.** AWS KMS, Azure Key Vault, GCP KMS, and HashiCorp Vault Transit
  envelope-KEK adapters; PASETO; Argon2id password hashing; field encryption.
- **Credential rotation.** Provider-backed rotation hooks across pgx, Redis,
  AMQP, NATS, S3, Azure Blob, GCS, SFTP, CSRF, and signed HTTP requests, with
  bounded provider contexts where the kit owns startup/reconnect calls.
- **Builder integrations.** Golden-path `app.Builder` exposes every new
  primitive without per-service middleware wiring.
- **Breaking changes.** Background components are one-shot; manual lifecycle
  HTTP servers reject zero-value `http.Server`; Redis health checks are
  critical by default; ASVS registries / RED-metric default buckets / retry
  default policies are now accessors; CORS requires explicit origins; auth
  middleware fails closed; development-mode escape hatches removed. Full list
  in [`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md).

### Release engineering

- License finalized as **Apache 2.0**; `SECURITY.md` published at the repo
  root with the coordinated-disclosure policy.
- Release-candidate hardening: clarified release provenance, removed
  placeholder cryptographic material, added CODEOWNERS coverage for
  security-sensitive audit/release/workflow files, and trimmed completed
  audit artifacts from package docs.

See [`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md) for the full
enumeration of breaking changes, new primitives, and verification commands.

### Migration

For the per-symbol break list grouped by package (renames, signature changes,
removed APIs, wire-format changes), see
[`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md).
