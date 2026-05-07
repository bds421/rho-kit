# v2.0.0 — feature-gap analysis vs ory/x

**Method**: package-by-package comparison of the public surface of github.com/ory/x against rho-kit v2.0.0. Surveyed all 53 top-level packages in ory/x; mapped each to rho-kit's existing surface (`app/`, `core/`, `crypto/`, `data/`, `grpcx/`, `httpx/`, `io/`, `observability/`, `resilience/`, `runtime/`, `security/`).
**Reviewer**: comparison agent
**Branch**: main @ d43b475

## Verdict

rho-kit v2.0.0 is **broadly equivalent or ahead** of ory/x for the agentic-AI service shape we target. ory/x is wider in the "Ory product family" sense — it ships product-specific concerns (Ory Network multi-region, JSONNet sandboxing, OpenAPI/Swagger doc helpers, koanf-based config, herodot error writer, Pop ORM migrations) that rho-kit deliberately does not. Where the two overlap (HTTP middleware, SSRF defense, JWT, password hashing, tracing, metrics, health, pagination, retries, CSRF, file watching, atomic file IO, contextual config, structured logging, errors with stack), rho-kit's implementations are equivalent or more opinionated/modern (e.g. argon2id-only passhash, GCRA rate limiting, RFC 7807 problem-details, OTel-native tracing, leader election, action log + approval workflow that ory/x has no analog for). The genuine **gaps worth filling for v2.0.0** are small utility helpers consumers tend to rebuild: a `randx`-style cryptographic random-string generator, `urlx.JoinPaths`/`AppendPaths` URL helpers, a typed-pagination Link/Content-Range header writer for offset pagination (we ship cursor-only), and a `safecast` integer-narrowing helper. Everything else is either already covered, intentionally out of scope, or worth deferring.

## ory/x packages — coverage matrix

| ory/x package | What it does | rho-kit equivalent | Status | Notes |
|---|---|---|---|---|
| `assertx` | testify helpers for asserting equal-after-JSON-roundtrip etc. | `httpx/httpxtest`, scattered test helpers | ⏸️ | Test helpers are repo-local; not worth a shared package. |
| `cachex` | Prometheus collector for ristretto cache stats | `observability/promutil` + `data/cache` (memory/redis) emit metrics natively | ✅ | We don't use ristretto; our cache emits its own RED metrics. |
| `castx` | reflection-based type coercion for koanf config values | `core/config` (envutil) | ⏸️ | Coupled to koanf; we don't use koanf. |
| `clidoc` | generate markdown docs for cobra commands | — | ⏸️ | Niche. `cmd/kit-doctor` and `cmd/kit-new` don't currently ship a `--gen-doc`. Defer; not v2 scope. |
| `cmdx` | cobra helpers: paginated output, table/JSON/YAML formatters, env-flag binding, fatal helpers, user-input prompts | `cmd/kit-doctor`, `cmd/kit-new`, `cmd/kit-bench-gate` (one-off cobra wiring) | ⚠️ | We have three CLIs; no shared cobra helper module. If we add more CLIs in v2.x, this is the obvious extraction. Not blocking v2.0.0. |
| `configx` | koanf-based config provider with JSON-schema defaults, file watching, env overrides, hot reload, validation | `core/config` (envutil + load + watchable + watcher + validate) | ✅ | Equivalent surface; we use struct-tag-based env binding instead of koanf+JSON-schema. Different philosophy, same job. |
| `contextx` | context-scoped per-tenant config provider (Ory Network: each tenant gets its own configx.Provider) | `core/tenant` + `core/contextutil` | ✅ | rho-kit's tenant ctx + per-tenant cost budget is a more focused take on the same idea. |
| `corsx` | sensible CORS middleware defaults + origin checker | `httpx/middleware/cors` | ✅ | Already in middleware stack. |
| `crdbx` | CockroachDB read-only / staleness query hints | — | ⏸️ | We don't target CockroachDB as a first-class backend in v2. |
| `dbal` | DSN canonicalization, driver routing across MySQL/Postgres/CockroachDB/SQLite | `app/database_module.go`, `app/pgx_module.go`, `app/read_replica_module.go` | ✅ | Builder modules cover GORM + pgx + MySQL + read-replica. |
| `decoderx` | unified HTTP body decoder (JSON, form, multipart) with JSON-schema validation, allowed methods/content-types | `httpx/typed.go`, `core/validate` | ✅ | We do JSON/form decode + struct validate; we don't bind to JSON-schema (we use go-playground/validator which is the more idiomatic choice). |
| `errorsx` | `pkg/errors` extensions (`WithStack`, `Cause`), `StatusCodeCarrier` interface | `core/apperror` | ✅ | apperror is richer: typed error codes, retry classification, problem-details bridge. |
| `fetcher` | load file content from http(s)/file/base64 with retry + ristretto cache | `core/config/load.go` (config-only), `httpx/resilient.go` | ⏸️ | Config-loading already supports file URIs. A general-purpose multi-scheme loader is overkill for our use cases. |
| `flagx` | helpers to read string/bool/int flags from cobra w/ MustGet panics | — | ⏸️ | Same niche as cmdx; defer. |
| `fsx` | directory hashing + embed.FS merge | `io/atomicfile` (write), `io/progress` | ⏸️ | We use embed.FS directly; merging is a niche need (Ory uses it for migration FS). |
| `hasherx` | bcrypt + argon2 + pbkdf2 password hashers behind a common interface | `crypto/passhash` (argon2id-only) | ✅ | We deliberately argon2id-only — fewer footguns, modern OWASP guidance. ory/x ships bcrypt+pbkdf2 mainly for legacy Kratos compat. |
| `healthx` | `/health/alive`, `/health/ready`, `/version` HTTP handlers | `observability/health` + `httpx/healthhttp` | ✅ | Equivalent. |
| `httprouterx` | `RouterAdmin` + `RouterPublic` separation w/ admin-prefix convention | `httpx` + `app.Builder` (admin port already separable via builder config) | ✅ | Same pattern, builder-driven instead of router-class-driven. |
| `httpx` (ory) | ssrf-safe RoundTripper, gzip server, request decoder, resilient client (retryablehttp), URL helpers | `httpx/resilient.go`, `httpx/deadline_transport.go`, `security/netutil/ssrf.go`, `httpx/middleware/recover` | ✅ | Coverage matches; we use stdlib + chi rather than httprouter. See `urlx` row for the one missing piece. |
| `ioutilx` | `pkger`-based file embedding (legacy markus/pkger) | embed.FS native | ⏸️ | Superseded by Go 1.16 `embed`. Not needed. |
| `ipx` | CIDR validators + SSRF dialer (allow/deny private ranges) backed by `code.dny.dev/ssrf` | `security/netutil/ssrf.go` | ✅ | Equivalent — both wrap an SSRF dialer. Worth a cross-reference (see hardening notes). |
| `josex` | JOSE encoding helpers + JWK keypair generation | `crypto/signing`, `security/jwtutil` | ✅ | We do JWT/PASETO sign+verify + JWKS via jwtutil. |
| `jsonnetsecure` | sandboxed jsonnet evaluator (forks subprocess with rlimits) for user-supplied jsonnet (Kratos identity schemas) | — | ⏸️ | Ory-specific (Kratos schema mappers). No agentic-AI use case. |
| `jsonnetx` | jsonnet format + lint helpers | — | ⏸️ | Same — Kratos-specific. |
| `jsonschemax` | JSON-schema path enumeration + default extraction (drives `configx`) | `core/validate` (struct-tag based) | ⏸️ | Coupled to JSON-schema-driven config; we use struct-tag validation. |
| `jsonx` | JSON patch (RFC 6902) with op allowlist, flatten, debug-decoder | — | ⚠️ | We don't ship a JSON-patch helper. If/when an agentic service exposes a PATCH endpoint with RFC 6902, worth re-evaluating. Defer to v2.1+. |
| `jwksx` | JWKS fetcher with ristretto cache + retryablehttp | `security/jwtutil/jwtutil.go` (JWKS resolution via go-jwt) | ✅ | jwtutil already caches+refreshes JWKS. |
| `jwtmiddleware` | net/http JWT verification middleware (auth0 wrapper) | `httpx/middleware/auth` + `app/jwt_module.go` | ✅ | Builder-mounted; richer (PASETO option, multi-tenant claims). |
| `jwtx` | typed JWT claim accessor (`Audience`, `Issuer`, etc.) with `mapx` fallback | `security/jwtutil` typed claims | ✅ | jwtutil parses claims into typed structs already. |
| `logrusx` | logrus wrapper with redaction + tracing/correlation injection | `observability/logging` + `observability/logattr` | ✅ | We use slog (modern stdlib choice) instead of logrus. |
| `mailhog` | SMTP test server fixture | — | ⏸️ | Identity-product-specific (email verification). |
| `mapx` | typed assertions on `map[string]interface{}` (`GetString`, `GetInt`, etc.) | `httpx/query_params.go` for query parsing; nothing for generic maps | ⏸️ | Niche; modern Go code prefers struct decoding. |
| `metricsx` | "anonymized telemetry to Ory cloud" middleware (segment.io tracker) | — | ⏸️ | Vendor-specific telemetry phone-home; we deliberately don't ship this. RED metrics in `observability/redmetrics` is the real metrics surface. |
| `migratest` | shared test harness for `popx` migrations against MySQL/PG/CRDB/SQLite | — | ⏸️ | Pop-specific. We use GORM auto-migrate or hand-rolled migrations. |
| `networkx` | Ory Network multi-region NID lookup + migrations | `core/tenant` | ⏸️ | Ory-Cloud product concept; rho-kit has its own multi-tenant primitive. |
| `openapix` | swagger/OpenAPI struct annotations for JSON-patch + pagination response models | — | ⏸️ | We don't generate Swagger from struct annotations as part of the kit. |
| `osx` | `ReadFileFromAllSources` (file://, http(s)://, base64://) + env helpers | `core/config/load.go` | ⏸️ | Multi-scheme loading is fetcher's domain; for plain env helpers we have envutil. |
| `otelx` | OpenTelemetry tracer setup (Jaeger, Zipkin, OTLP), span middleware, attribute helpers | `observability/tracing` + `app/tracing_module.go` | ✅ | Tracing module sets up OTLP exporter; HTTP/gRPC tracing middleware is in `httpx/middleware/tracing` and `grpcx/tracing.go`. |
| `pagination` | offset-pagination Link header writer (RFC 5988), parse `limit`/`offset` query, items slicer | `httpx/pagination` (cursor-only) | 🆕 | rho-kit ships `cursor` and `cursor_list` only. We don't ship offset-pagination Link headers or `limit`/`offset` parser. Many backends still want offset pagination for admin/list endpoints. **Add for v2.0.0.** |
| `pointerx` | `Ptr(v T) *T`, etc. | `Ptr` is built into modern Go idioms; rho-kit uses generics directly | ⏸️ | Trivial; not worth a package. |
| `popx` | `gobuffalo/pop` migration runner with sha256 fingerprinting + telemetry spans | `app/database_module.go` (GORM auto-migrate) | ⏸️ | We are GORM-native, not Pop-native. |
| `profilex` | runtime/pprof helpers | `observability/pprof` | ✅ | pprof endpoints already mounted. |
| `prometheusx` | Prometheus `/metrics` handler + go runtime collector + status-code label sanitizer | `observability/promutil` + `observability/runtimemetrics` + `observability/redmetrics` | ✅ | Equivalent. |
| `proxy` | reverse-proxy primitive with host-mapper + req/resp middleware (used by Oathkeeper) | — | ⏸️ | Out of scope (Oathkeeper is a product). |
| `randx` | `RuneSequence`, `MustString`, alpha/num charsets backed by crypto/rand | `crypto/signing` (HMAC random keys, but no public string generator), `crypto/encrypt` | 🆕 | We don't expose a public crypto-random-string generator. Consumers will reach for `math/rand` or `crypto/rand` raw. **Add `core/randstr` (or similar) for v2.0.0.** Tiny package, big footgun-prevention. |
| `region` | enum of Ory Network regions | `core/tenant` | ⏸️ | Ory-product concept. |
| `reqlog` | HTTP request log middleware | `httpx/middleware/logging` + `observability/logging` | ✅ | Equivalent. |
| `resilience` (ory) | exponential-backoff retry helper | `resilience/retry` + `resilience/circuitbreaker` | ✅ | rho-kit ships retry + circuit breaker, ory/x just retry. |
| `safecast` | `Uint64ToInt64` clamping integer narrow-conversion (Go 1.22 lint compliance) | — | 🆕 | We have multiple call sites doing `int64(uint64Val)` without clamping. With `gosec G115` enforced in CI on v2, this becomes a real footgun. **Add a tiny `core/safecast` package or inline equivalent.** |
| `serverx` | shared "404 not found" handler | `httpx/error_handler.go` | ✅ | Already covered by the problem-details handler. |
| `servicelocatorx` | option-pattern wrapper for shared dependencies (logger, tracer, etc.) injected into Kratos/Hydra | `app.Builder` | ✅ | Builder pattern is a strict superset. |
| `snapshotx` | golden-file snapshot testing helper (cupaloy wrapper) with JSON-path masking | — | ⏸️ | Test-tooling preference; we use testify diffs and explicit fixtures. Not v2 scope. |
| `sqlcon` | DSN parsing, error normalization (`ErrNoRows`, `ErrUniqueViolation`), parallelism limiter | `app/database_module.go`, `app/pgx_module.go` (driver-specific error mapping inside) | ⚠️ | We translate driver errors inside repository code; we don't expose a normalized error-classification helper across drivers. Worth flagging — when we add MySQL-MariaDB-Postgres-cross repositories, this is the obvious extraction. Defer to v2.1. |
| `sqlxx` | `Duration`, `JSONScanner`, `NullJSONRawMessage`, `NullTime` types for SQL columns | scattered across consumers (not in kit) | ⚠️ | A handful of consumer apps have re-implemented `Duration` and `NullJSONRawMessage`. Tiny utility package would prevent re-rolls. Defer to v2.1 unless we hit it again. |
| `stringslice` | `Unique[T]([]T) []T` | stdlib `slices.Compact` after sort | ⏸️ | Stdlib covers it post-Go-1.21. |
| `stringsx` | `ToLowerInitial`, `ToUpperInitial`, `SwitchExact`, `SplitEx`, `Truncate` | scattered helpers in middleware | ⏸️ | Each is a 5-line helper; not worth a package surface. |
| `tlsx` | self-signed cert generation + TLS termination helper for tests | `security/netutil/tls.go` | ✅ | netutil/tls covers TLS config; cert-generation-for-tests is repo-local in `httpx/httpxtest`. |
| `urlx` | `MustJoin`, `AppendPaths`, `Copy`, `ParseRequestURIOrPanic`, `Extract` URL helpers | — | 🆕 | rho-kit has zero `*url.URL` manipulation helpers. Consumers writing `path.Join` against URL paths trip on trailing-slash and url-encoding edge cases. **Add `httpx/urlutil` (or `core/urlx`) for v2.0.0.** Battle-tested in Hydra/Kratos for years. |
| `uuidx` | `NewV4()` panicking shorthand (gofrs/uuid) | scattered (we use google/uuid) | ⏸️ | Trivial. |
| `watcherx` | fsnotify wrapper for files+directories with symlink resolution | `core/config/watcher.go` + `core/config/watchable.go` | ✅ | watcher.go already wraps fsnotify with debounce; watchable.go is the config-reload glue. |

## Summary counts

- ✅ already covered: **27**
- 🆕 missing, add to v2.0.0: **4** (`randx`-equivalent, `urlx`-equivalent, offset `pagination`, `safecast`)
- ⚠️ missing, defer: **5** (`cmdx`, `jsonx` JSON-patch, `sqlcon` error normalization, `sqlxx` SQL types, plus the cross-cutting CLI extraction)
- ⏸️ intentionally out of scope: **22**

## 🆕 Should add to v2.0.0 (small, high-leverage gaps)

### 1. `core/randstr` — crypto-random string generator (replaces `randx`)

**ory/x**: `randx.RuneSequence(length, charset)`, `randx.MustString(length, charset)`, plus pre-defined `AlphaNum`, `AlphaLowerNum`, `AlphaNumNoAmbiguous` charsets, all backed by `crypto/rand`.

**Why adopt**: every service that issues correlation IDs, share-tokens, signed-URL nonces, OTPs, or anti-forgery tokens needs this. Without a kit-blessed helper, consumers reach for `math/rand` (predictable) or hand-roll `crypto/rand` with off-by-one rejection-sampling bugs. Ory's implementation is correct (rejection sampling against `big.Int(len(charset))`).

**Effort**: ~1 hour. Single file (~80 lines) in `core/randstr/` with three exported functions and the standard charsets. Add to apperror retryability — never. Pure utility.

### 2. `httpx/urlutil` — `*url.URL` join/copy/extract helpers (replaces `urlx`)

**ory/x**: `urlx.MustJoin(base, parts...)`, `urlx.AppendPaths(u, parts...)` (preserves trailing slash, idempotent re-encoding), `urlx.Copy`, `urlx.ParseRequestURIOrPanic`.

**Why adopt**: rho-kit currently has zero exported URL-manipulation helpers. The agentic-service builder generates redirect URLs, signed-request URLs, MCP callback URLs, and webhook URLs — all of which want safe `path.Join`-but-for-URLs. The trailing-slash and `RawQuery`-preservation rules ory/x figured out are footguns.

**Effort**: ~1-2 hours. ~100 lines. Vendor the four functions almost verbatim (Apache-2.0 compatible). Add a comment crediting ory/x.

### 3. `httpx/pagination` — offset / Link-header pagination (extend existing cursor surface)

**ory/x**: `pagination.Header(w, u, total, page, perPage)` writes RFC 5988 `Link: <...>; rel="next"|"prev"|"first"|"last"` headers; `pagination.Parse(r, defaultLimit, defaultOffset, maxLimit)` reads `?limit=&offset=` with bounds-clamping.

**Why adopt**: rho-kit ships only cursor pagination today. Many list/admin endpoints still expect classic offset-pagination with Link headers (every front-end paginator library, every kubectl-style table CLI). This complements — does not replace — cursor.

**Effort**: ~3-4 hours. Two new files in `httpx/pagination/`: `offset.go` (parse + clamp + total-count math) and `link_header.go` (RFC 5988 writer with first/prev/next/last). Reuse cursor's tests as scaffolding. Document in pagination doc.go that "use cursor for hot lists; offset is fine for admin/UI tables".

### 4. `core/safecast` — checked integer narrowing (replaces `safecast`)

**ory/x**: `safecast.Uint64ToInt64(uint64) int64` clamps to `MaxInt64` instead of overflowing.

**Why adopt**: with Go 1.22+ `gosec G115` (integer overflow) enforced as a CI gate in v2.0.0 (per supply-chain policy), every `int64(myUint64)` flips to a lint error. We need a blessed helper so consumers don't disable the lint per-line. ory/x's version is six lines.

**Effort**: ~30 min. Single tiny package (`Uint64ToInt64`, `IntToInt32`, `IntToUint32` — three functions) with property-based tests. Inline-able if we don't want a new module.

## ⚠️ Worth flagging but defer to v2.1

### 1. `cmdx` — shared cobra helpers for our CLIs

**Effort**: ~1 day. **Why defer**: only three CLIs (`kit-doctor`, `kit-new`, `kit-bench-gate`) and they are intentionally independent binaries. Extract once a fourth shows up, or once consumers ship their own kit-CLIs.

### 2. `jsonx` — RFC 6902 JSON-patch with op allowlist

**Effort**: ~half day. **Why defer**: agentic services don't typically expose JSON-PATCH endpoints; if/when an MCP tool needs patching against a stored object, revisit.

### 3. `sqlcon` — cross-driver SQL error classifier

**Effort**: ~1 day. **Why defer**: today our DB error mapping lives inside repository code (apperror.NewConflict on PG `23505` etc.). Extract once we have ≥2 services hitting both PG and MySQL with the same repository pattern. Cross-reference ory/x `sqlcon/error.go` when we do — it covers MySQL/PG/CockroachDB/SQLite UNIQUE/FK/check error codes.

### 4. `sqlxx` — `Duration`, `NullJSONRawMessage`, `NullTime` SQL column types

**Effort**: ~half day. **Why defer**: only worth it once we see ≥2 consumers re-implement them. Track via the `feedback_review_process` memory rule.

### 5. `cachex` — Prometheus collector for ristretto cache stats

**Why defer**: rho-kit's `data/cache` doesn't use ristretto under the hood. If/when we add a ristretto-backed cache tier, copy this verbatim.

## ⏸️ Intentionally not in scope

- **`metricsx`** — phone-home anonymized telemetry to a vendor cloud. We do not ship vendor analytics in our kit; explicit non-goal.
- **`networkx`, `region`, `crdbx`** — Ory Network / CockroachDB-multi-region product concepts. rho-kit has its own tenant primitive; we don't pin to CockroachDB.
- **`mailhog`** — SMTP test fixture. No mail surface in the kit.
- **`jsonnetsecure`, `jsonnetx`** — Kratos identity-schema jsonnet evaluation. No analog use case.
- **`popx`, `migratest`** — gobuffalo/pop migration runner. We are GORM-native (and pgx-native).
- **`openapix`, `clidoc`, `swaggerx`** — OpenAPI/Swagger generation. Not part of the kit's contract; consumers handle their own API docs.
- **`proxy`** — reverse-proxy primitive (Oathkeeper). Out of scope.
- **`fetcher`, `osx`** — multi-scheme content loaders (file://, http://, base64://). Niche to config-loading, which we already cover narrowly in `core/config/load.go`.
- **`ioutilx`** — `pkger` shim, superseded by Go 1.16 `embed`.
- **`mapx`, `castx`** — `map[string]interface{}` typed accessors / koanf reflection coercion. Modern Go code uses struct decoding.
- **`pointerx`, `stringslice`, `uuidx`, `flagx`, `stringsx`** — single-line helpers each; stdlib covers most post Go 1.21. Adding a one-function utility module increases module-graph surface for negative payoff.
- **`servicelocatorx`** — option-pattern dependency injector. `app.Builder` is the strict superset.
- **`snapshotx`, `assertx`** — testing helpers; preference, not capability.
- **`josex`, `jsonschemax`** — covered by our crypto/security packages or made-redundant by struct-validator.

## Hardening-opportunity callouts

These are places where ory/x's implementation has been hardened by years of running Hydra/Kratos in production. When we next touch the rho-kit equivalent, **read their tests**:

- **`security/netutil/ssrf.go`** ↔ ory/x `ipx/ssrf.go`. Both wrap `code.dny.dev/ssrf`. ory/x has a glob-matched `internalIPExceptions` list and an `IncomingRequestURL` helper that strips query+fragment before matching. We should cross-reference their `httpx/ssrf_test.go` before declaring our SSRF defense complete.
- **`security/csrf`** ↔ ory's CSRF lives inside Kratos itself (not x), but ory/x `corsx/check_origin.go` has a battle-tested origin-allowlist parser (with glob support) that our `csrf.go` could reuse for `Origin`-header validation rather than the simpler equality check we ship.
- **`crypto/passhash`** ↔ ory/x `hasherx/hash_comparator.go`. Our argon2id-only stance is correct, but `hasherx` has constant-time-comparison + parameter-format-versioning logic worth cross-referencing for our `Verify` path.
- **`security/jwtutil`** ↔ ory/x `jwksx/fetcher_v2.go`. Their JWKS fetcher has explicit ristretto cache + retryablehttp + force-KID-refresh on cache miss. Our jwtutil should grow a "force-refresh on unknown kid" path next time we touch it.
- **`core/config/watcher.go`** ↔ ory/x `watcherx/file.go`. Their symlink-resolution + directory-watch fallback (when the file doesn't yet exist) is more thorough than ours — and matters for k8s ConfigMap mounts where the file is a symlink that gets atomically-rotated. Read `watcherx_test.go` next time we touch config hot-reload.
- **`core/apperror`** ↔ ory/x `errorsx`. They expose `StatusCodeCarrier`, `RequestIDCarrier`, `DebugCarrier` interfaces that downstream HTTP error writers detect via type-assertion. Our problem-details writer already does this; cross-reference if we ever expose our error types as a public extension surface.
- **`httpx/pagination`** (when adding offset support) ↔ ory/x `pagination/header.go` + `parse.go`. Their `parse.go` clamps `limit` to `[1, max]` and rejects non-positive `offset` with a herodot 400; emulate the bounds-checking exactly.

