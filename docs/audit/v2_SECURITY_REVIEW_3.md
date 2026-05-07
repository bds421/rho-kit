# v2.0.0 — third-pass security review (post-dev-mode-removal)

**Reviewer**: security-reviewer agent (third pass)
**Branch**: main @ HEAD (post-c113451 — dev-mode-removal landed)
**Scope**: regression check on the 22 prior findings closed between v2_SECURITY_REVIEW.md and v2_SECURITY_REVIEW_2.md (plus the auth fail-closed fix in 2937115 and the M-1 RedisNonceStore from v2_SECURITY_REVIEW.md), and a fresh hunt for fail-open shapes introduced by the rename / restructure that landed in c113451 (`feat!: remove development mode; production settings always apply`).

---

## Verdict

**Do NOT tag v2.0.0 as-is.** Two new findings emerged in the post-dev-mode-removal surface — both MEDIUM, both real fail-open / hygiene gaps that fit the user's "no findings, including trivial ones" bar:

- **M-A (NEW)**: `validateProductionSafety` only catches `Internal.Host == "0.0.0.0"` (literal IPv4 wildcard). An operator who sets `INTERNAL_HOST=[::]` (IPv6 wildcard) or any other unspecified-address form binds the unauthenticated `/metrics` endpoint to all interfaces and the validator passes silently. The C-1 fix's intent ("default-deny non-loopback bind") is satisfied for the common case but leaks for IPv6.
- **M-B (NEW)**: `infra/sqldb/pgx`'s `Config.AllowPlaintext` opt-out is silently honored when an operator constructs `Config` from a struct literal in production code — the field's doc says "tests only" but the code path applies the opt-out regardless of caller. There is no startup gate distinguishing test fixtures from production configs.

The 22 prior findings + auth fail-closed + M-1 are all confirmed closed; the rename did not regress any of them. Fix M-A and M-B (and ideally adopt the recommendations under "Items checked, no findings → defence-in-depth notes") before tagging.

---

## Regression check

For each prior finding: status + current file:line of the fix.

| ID | Title | Status | Current fix location |
|----|-------|--------|----------------------|
| Auth fail-closed (2937115) | `RequirePermission` / `RequireScope` no longer pass-through on missing claim | **still closed** | `httpx/middleware/auth/auth.go:249-271` (RequirePermission), `:277-302` (PermissionByMethod), `httpx/middleware/auth/scope.go:26-44` (RequireScope), trusted-S2S marker stamping at `httpx/middleware/auth/auth.go:200` |
| C-1 (v2-2) | Internal ops port binds to 0.0.0.0 by default, exposes /metrics | **still closed (with new gap, see M-A below)** | Default loopback at `app/config.go:37-43`; validator at `app/validate.go:97-99`; opt-out `WithInternalNonLoopback` at `app/builder.go:212-227` |
| C-2 (v2-2) | Production-defaults skip TLS check | **still closed** | Validator at `app/validate.go:89-91`; opt-out `WithoutTLS` at `app/builder.go:229-242` |
| C-3 (v2-2) | Cross-tenant key collision via `:` in tenant ID | **still closed** | Validator at `core/tenant/tenant.go:65-79` (forbidden bytes incl. `:`); length-prefix scoping at `data/cache/tenant/tenant.go:71-78` and `data/idempotency/tenant/tenant.go:76-83` |
| H-1 (v2-2) | Idempotency middleware collapses to shared-key on empty userID | **still closed** | `httpx/middleware/idempotency/idempotency.go:243-255` returns 400 when extractor returns `""` |
| H-2 (v2-2) | gRPC auth lacks RequirePermission/RequireScope/IsTrustedS2S | **still closed** | `grpcx/interceptor/auth.go:212-303` (RequirePermissionUnary/Stream, RequireScopeUnary/Stream); `:305-372` (MTLSAuthUnary/Stream); `:191-200` (IsTrustedS2S) |
| H-3 (v2-2) | Example agentic-service is copy-paste hazard | **still closed** | `examples/agentic-service/internal/app/app.go:1-29` (giant SECURITY header); per-handler SECURITY warnings at `:62-69` (HMAC), `:159-173` (dangerousAction), `:204-209` (budgetStatus); slowloris timeouts at `:97-102` |
| H-4 (v2-2) | Budget middleware fails open without WithMultiTenant | **still closed** | Validator at `app/validate.go:62-64` rejects `WithTenantBudget` without `WithMultiTenant` |
| H-5 (v2-2) | WithProductionDefaults does not require WithJWTAudience | **still closed** | Validator at `app/validate.go:82-84`; opt-out `WithoutJWTAudience` at `app/builder.go:244-256` |
| H-6 (v2-2) | `authz.SubjectFromHeader` reads spoofable header | **still closed** | Deprecation warn-once at `httpx/authz/authz.go:135-154`; safe alternatives `SubjectFromTrustedHeader` at `:171-178` and `SubjectFromContext` at `:202-215`; `SubjectFromUntrustedHeader` (for tests) at `:117-133` |
| H-7 (v2-2) | MCP default actor extractor reads spoofable X-Actor-Id | **still closed** | Default returns `AnonymousActor` at `httpx/mcp/mcp.go:321-327`; opt-in helpers `WithActorFromContext` at `:174-190` (recommended), `WithActorFromHeader` at `:192-216` (with SECURITY WARNING in doc) |
| H-8 (v2-2) | JWT module KIT_ENV literal-match drift | **still closed** | KIT_ENV reads removed entirely from `app/jwt_module.go` and `security/jwtutil/jwtutil.go`; pairing now enforced unconditionally at `app/validate.go:75-84` |
| M-1 (v1) | signedrequest ships only MemoryNonceStore | **closed** | `httpx/middleware/signedrequest/redis/redis.go:1-114` (RedisNonceStore using SET NX EX); doc at `httpx/middleware/signedrequest/redis/doc.go:1-46` |
| M-1 (v2-2) | Same as v1 M-1 (re-flagged as MEDIUM in v2 audit) | **closed** | See above |
| M-2 (v2-2) | `RequirePermission("")` / `RequireScope("")` do not panic | **still closed** | Panic at `httpx/middleware/auth/auth.go:250-257` (RequirePermission), `:278-283` (PermissionByMethod), `httpx/middleware/auth/scope.go:27-29` (RequireScope), `:54-56` (RequireScopeStrict) |
| M-3 (v2-2) | JWT permissions claim malformed → silent empty | **still closed** | Warn at `security/jwtutil/jwtutil.go:135-138` (also for scopes at `:144-147`) |
| M-4 (v2-2) | Tenant middleware silently passes safe-method GETs | **still closed** | `WithRequiredOnSafeMethods` opt-in at `httpx/middleware/tenant/tenant.go:64-84`; gate at `:108-121` |
| M-5 (v2-2) | gRPC logging accepts unvalidated correlation/request IDs | **still closed** | `isValidID` at `grpcx/interceptor/logging.go:107-118` (mirrors `idutil.IsValid`); applied via `adoptOrGenerate` at `:85-101` (regenerates on invalid) |
| M-6 (v2-2) | Auditlog HTTP middleware uses raw r.RemoteAddr | **still closed** | `WithTrustedProxies` at `httpx/middleware/auditlog/auditlog.go:45-53`; `clientip.ClientIPWithTrustedProxies` resolver at `:87-92` |
| M-7 (v2-2) | Auditlog HTTP middleware does not run on panics | **still closed** | Deferred audit + panic re-raise at `httpx/middleware/auditlog/auditlog.go:103-118`; `panic` metadata at `:150-152` |
| M-8 (v2-2) | Budget middleware default scope is unset | **still closed** | Default `scope: "tenant"` at `httpx/middleware/budget/budget.go:101` |
| M-9 (v2-2) | CSRF SkipCheck-via-Bearer bypasses Origin allowlist | **still closed** | Origin check before skip-check in legacy double-submit at `httpx/middleware/csrf/csrf.go:265-279`; same ordering in session-bound flow at `:367-378` |

---

## New findings

### MEDIUM

#### M-A. `validateProductionSafety` only catches IPv4 wildcard (`0.0.0.0`); IPv6 wildcard `[::]` and other unspecified-address forms bypass the check

**File**: `app/validate.go:97-99`

**What's wrong**: The C-1 fix asserts the check `b.cfg.Internal.Host == "0.0.0.0"` and panics when set without `WithInternalNonLoopback`. The literal-string comparison only matches the IPv4 wildcard. Operators on IPv6-only or dual-stack hosts who set `INTERNAL_HOST=[::]` (the standard IPv6 wildcard form) get a successful bind to every IPv6 interface and the validator passes silently. Same for `INTERNAL_HOST=0.0.0.0` typed as `00.00.00.00` (Go's net.Listen accepts both forms — the equivalence is at the kernel layer, not the string layer the validator runs at).

The intent of C-1 — "default-deny non-loopback bind for the unauthenticated /metrics endpoint" — is satisfied for IPv4 wildcards but leaks for IPv6. The doc on `InternalConfig.Host` (`app/config.go:23`) says "set to '0.0.0.0' only when the network is strictly isolated" — the operator following that doc on an IPv6-only host has no analogous warning.

**Attack scenario**: A team migrating to IPv6 sets `INTERNAL_HOST=[::]` in their staging deployment and rolls it to production unmodified. Validator passes. `/metrics` is reachable on every IPv6 interface — including any IPv6-routable management network the operator forgot was wired to the host. An attacker on the same VLAN (or anyone on the public IPv6 internet if NetworkPolicy is missing) reads tenant labels, route patterns, and process fingerprinting. Same blast radius as C-1's original IPv4 case.

**Suggested fix**: Replace the literal-string check with `net.ParseIP(b.cfg.Internal.Host).IsUnspecified()` — Go's stdlib helper handles `0.0.0.0`, `::`, `0:0:0:0:0:0:0:0`, and the IPv4-mapped variants in one call. Concretely:

```go
if h := strings.Trim(b.cfg.Internal.Host, "[]"); h != "" {
    if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() && !b.allowInternalNonLoopback {
        return fmt.Errorf("Internal.Host=%q binds to all interfaces ... call WithInternalNonLoopback ...", b.cfg.Internal.Host)
    }
}
```

Strip the brackets first because `Internal.Host` is the host portion stored bracket-less by convention but operators may include them.

**5-line failing test**:

```go
func TestBuilder_Validates_RejectsExposedInternalIPv6(t *testing.T) {
    cfg := BaseConfig{
        Internal: InternalConfig{Host: "::", Port: 9090},
        TLS:      validTLSForTest(),
    }
    err := New("svc", "v1", cfg).WithoutJWTAudience().Validate()
    require.Error(t, err, "IPv6 wildcard bind exposes /metrics on every IPv6 interface; must require WithInternalNonLoopback")
}
```

---

#### M-B. `infra/sqldb/pgx.Config.AllowPlaintext` is silently honored regardless of caller; "tests only" doc is unenforced

**File**: `infra/sqldb/pgx/pgx.go:40-77`

**What's wrong**: The dev-mode-removal commit added `Config.AllowPlaintext bool` at line 45 with the doc "Use only for tests against a local fixture (testcontainers, embedded postgres) where TLS is impractical and the connection never crosses a network boundary. Production deployments must leave this false." The check at line 73 honors the flag without any further gate:

```go
if !cfg.AllowPlaintext {
    if err := requireTLS(cfg.DSN); err != nil { return nil, err }
}
```

A production caller who copy-pastes the integration_test.go usage `Config{DSN: dsn, AllowPlaintext: true}` (line 36, 44, 70) into their service main gets a working pgx pool with no TLS check. The kit emits no warning, no log, no audit signal. The "tests only" hint is a comment, not a guard.

This is the same fail-open shape the user explicitly called out — a `Without*`-style opt-out that lacks active acknowledgement. Compare to `app.Builder.WithoutTLS` which sets a separate `allowPlaintext` flag AND makes the operator restate intent at the call site (`.WithoutTLS()` is a separate method call). `pgx.Config.AllowPlaintext: true` is one struct-field assignment that doesn't even need to appear on its own line — it can be hidden in the middle of a 30-line config-loader function.

**Attack scenario**: A developer writes integration tests using `Config{DSN: localDSN, AllowPlaintext: true}`. During a refactor, the service's prod config-loader is consolidated with the test setup. The shared loader passes through `AllowPlaintext: cfg.AllowPlaintext`. An operator setting `DB_ALLOW_PLAINTEXT=true` in a production env (because the loader exposes it) gets a plaintext Postgres connection over the network — no panic, no warning, no log. The kit's "production-safe defaults are unconditional" promise (per the package doc at line 17-18) is violated.

The risk is amplified by the doc lying: the package comment at line 14-18 says *"There is no KIT_ENV escape hatch — production-safe defaults are unconditional."* — but the `AllowPlaintext` field IS a production-honored escape hatch. The doc statement is technically true (no KIT_ENV) but operationally false (an env-driven config with `AllowPlaintext` opens the same hole).

**Suggested fix** (one of):

(a) Rename the field to make abuse loud: `AllowPlaintext_TestsOnly bool` so a code review immediately catches the production callsite. The Go convention is to suffix with `Unsafe` or `_TEST` — pick one and stick to it.

(b) Add a build-tag gate: only honor `AllowPlaintext` under `//go:build test`. Production binaries see the field but it has no effect (the constant `allowPlaintextEnabled` is `false`). Tests build with `-tags test` and the gate flips. This is the strongest fix because the field's effect is statically eliminated from production binaries.

(c) Require an additional acknowledgement: refuse the connection unless the DSN host is also loopback (`127.0.0.1`, `::1`, `localhost`). Production DSNs almost never point at loopback; tests almost always do. This eliminates the cross-network-boundary risk while keeping the field useful for testcontainers.

Option (b) is best; option (c) is the smallest-diff fix.

**5-line failing test** (with option (c) applied):

```go
func TestPgxConfig_AllowPlaintext_RejectsRemoteHost(t *testing.T) {
    cfg := Config{DSN: "postgres://u:p@10.0.0.5:5432/db?sslmode=disable", AllowPlaintext: true}
    _, err := Connect(context.Background(), cfg)
    require.Error(t, err, "AllowPlaintext must reject remote hosts; only loopback DSNs are honored")
}
```

---

## Items checked, no findings

Each line documents an audit step that did not surface a regression or new bug. Provided so coverage is auditable.

### Files re-read end-to-end (no regressions)

- `app/validate.go` — every check previously gated on `WithProductionDefaults()` now runs unconditionally inside `validateProductionSafety` (called by `Validate`, called by `Run`). Each tightening individually relaxable via an explicit `Without*()` call that flips a typed boolean. Single point of acknowledgement per relaxation.
- `app/builder.go:769-782` — `Run()` calls `Validate()` before any infrastructure spins up. There is no public `Build()` that bypasses validation.
- `app/builder.go:212-256` — three opt-outs (`WithInternalNonLoopback`, `WithoutTLS`, `WithoutJWTAudience`) are wired to the validator via boolean fields (`allowInternalNonLoopback`, `allowPlaintext`, `jwtAllowAnyAudience`). Each is an explicit method call; an operator who omits the call gets the strict check. (The fourth, `WithoutJWTIssuer`, sets `jwtAllowAnyIssue` at `:408-412`.)
- `app/jwt_module.go:55-71` — `allowAnyIssuer` branch maps to `jwtutil.WithAllowAnyIssuer()` and emits a warn log; the unreachable `default` branch is defensive and only fires if the Builder validator is bypassed (which is impossible through the public API). KIT_ENV reads are gone.
- `app/config.go` — `Internal.Host == ""` resolves to `127.0.0.1` in `Addr()` (loopback default); `LoadBaseConfig` defaults `INTERNAL_HOST` to `""`, so the safe path is the default. `Environment` defaults to `"production"` but is now purely informational (used only by tracing/logging tags).
- `security/jwtutil/jwtutil.go` — `Verify` calls `jwt.Parse` with `WithValidate(true)`, `WithAcceptableSkew(30s)`, optional `WithIssuer`/`WithAudience`. `KeySet()` returns nil when stale (max-stale window enforced). `WithAllowAnyIssuer` is a documented opt-in. KIT_ENV reads are gone; the kit's downstream verifiers fail-closed when `KeySet()` returns nil.
- `security/jwtutil/config.go` — only loads `JWKS_URL` and `JWT_CACHE_TTL_MINUTES`; no security gating on env strings.
- `security/netutil/tls.go` — `ServerTLS` returns `(nil, nil)` when partial config (matches doc); TLS 1.3 minimum; client-cert verification mode is explicit (`VerifyClientCertIfGiven` default, `RequireAndVerifyClientCert` opt-in).
- `infra/sqldb/pgx/pgx.go` — `requireTLS` rejects `disable`/`prefer`/`allow` and empty sslmode unconditionally. KIT_ENV reads are gone. (See M-B above for the `AllowPlaintext` opt-out concern.)
- `infra/messaging/buffered_publisher.go` — `NewBufferedPublisher` panics when `stateFile == ""` and `WithEphemeralBuffer()` was not called (line 162-164). The opt-out is an explicit, documented call. KIT_ENV reads are gone.
- `httpx/middleware/csrf/csrf.go` — Origin allowlist check before SkipCheck in both legacy double-submit and session-bound paths (M-9 fix). Panic on missing secret unless `WithDevSecret()` opt-in (line 202-209). Panic on `SameSite=None && !secure` (line 217-219). KIT_ENV reads are gone.
- `httpx/middleware/auth/auth.go` — Trusted-S2S marker stamped only on the verified-mTLS branch (`requireHeaderUser` line 199-201, gated on `verifyClientCert` returning true at line 103). `RequirePermission`/`PermissionByMethod` panic on empty arg (M-2 fix). Fail-closed semantics confirmed (lines 249-271, 277-302) — no permissions claim AND no S2S marker → 403, never pass-through.
- `httpx/middleware/auth/scope.go` — `RequireScope` panics on empty arg (line 27-29). Fail-closed: no scopes AND no S2S marker → 403 (line 36-40).
- `core/secret/secret.go` — All redaction methods use **value receivers** (lines 158-187): `String()`, `GoString()`, `MarshalJSON`, `MarshalText`, `LogValue`, `Format`. Package doc at lines 18-25 explicitly explains the value-receiver rationale. By-value usage of `secret.String` is now safe. v1's H-1 fix verified.
- `core/tenant/tenant.go:65-79` — `forbiddenBytes = ":/\n\r\t\x00"`; `ValidateID` rejects all of them; `NewID` calls `ValidateID`. `NewIDUnchecked` and direct cast bypass validation (documented escape hatch for already-validated DB columns).
- `data/cache/tenant/tenant.go:71-78` — Length-prefix scoped key `tenant:<len>:<id>:<raw>` defeats the `:` collision regardless of how the ID was constructed. C-3 closed at the wrapper layer too (defence-in-depth).
- `data/idempotency/tenant/tenant.go:76-83` — Same length-prefix scheme, same defence-in-depth. C-3 closed.
- `httpx/middleware/tenant/tenant.go` — `HeaderExtractor("X-Tenant-Id")` is the default; nil header panics at construction. `WithRequiredOnSafeMethods(true)` requires safe methods to also have a tenant (M-4 fix). Default keeps the safe-method bypass.
- `httpx/middleware/budget/budget.go:101` — Default scope is `"tenant"` (M-8 fix). Backend errors surface as 503 (line 121-126); rejection writes proper headers (line 136-151).
- `httpx/middleware/idempotency/idempotency.go:243-255` — Empty userID returns 400 (H-1 fix). Construction panics at line 222-224 unless `WithUserExtractor` OR `WithAllowSharedKeys`.
- `httpx/middleware/auditlog/auditlog.go` — `WithTrustedProxies` (M-6) and panic-recording deferred audit (M-7) both in place. `clientIPFunc` defaults to `clientip.ClientIPWithTrustedProxies` resolver (line 87-92).
- `httpx/middleware/timeout/timeout.go` — WebSocket bypass requires explicit `WithWebSocketUpgradeBypass()` opt-in (L-3 fix at line 19-26, gate at line 94-97).
- `httpx/middleware/signedrequest/signedrequest.go` — Verify order correct: timestamp → signature decode → key resolve → body read → MAC compare → nonce store. `nonceStore == nil` panics at construction.
- `httpx/middleware/signedrequest/noncestore.go` — `MemoryNonceStore` unchanged; sweep cadence bounded.
- `httpx/middleware/signedrequest/redis/redis.go` — `RedisNonceStore` uses `SET NX EX` atomically; nil client panics; ttl<=0 panics. Failure surfaces as middleware 500 (no fail-open). M-1 closed.
- `httpx/sign/sign.go:138-147` — `defaultNonce` panics on `crypto/rand` error (L-2 fix).
- `httpx/healthhttp/handler.go:79, 87-92` — `noStoreHandler` wraps `/metrics` with `Cache-Control: no-store` (L-4 fix). `/health` and `/ready` JSON handler also sets `no-store` at line 31.
- `httpx/authz/authz.go:135-154` — `SubjectFromHeader` is now deprecated (warns once via `sync.Once`). `SubjectFromTrustedHeader` (line 156-178) and `SubjectFromContext` (line 202-215) are the safe alternatives. `SubjectFromUntrustedHeader` (line 117-133) is the explicit-naming variant for tests. H-6 closed.
- `httpx/middleware/recover/recover.go` — `http.ErrAbortHandler` re-raised; `recordingWriter` flags `wroteHeader` before delegating; Hijack delegates safely. No regression.
- `httpx/middleware/cors/cors.go` — Delegates to `jub0bs/cors`, panics on invalid config.
- `httpx/middleware/secheaders/secheaders.go` — `shouldSetHSTS` correctly gates on `r.TLS != nil` OR `WithForceHSTS` OR (`WithTrustedProxiesForProto` AND `X-Forwarded-Proto: https`). `WithoutHSTS` is an explicit opt-out.
- `httpx/middleware/ratelimit/ratelimit.go` — Sharded fixed-window, atomic per-shard, LRU caps memory. Cleanup is bounded (`maxCleanupPerShard = 1000`).
- `httpx/middleware/ratelimit/keyed.go` — Same algorithmic safety as IP limiter.
- `httpx/middleware/ratelimit/tenant/tenant.go` — Fail-closed on missing tenant (400) AND on limiter error (500). Nil limiter panics at construction.
- `httpx/middleware/approval/approval.go` — `next` intentionally unused (documented); tenant required (400 on absent); body capped at 64 KiB by default.
- `httpx/middleware/cspnonce/cspnonce.go` — (Not re-read; no behavioural change since v2-2.)
- `httpx/middleware/maxbody/maxbody.go` — (Not re-read; no behavioural change since v2-2.)
- `httpx/middleware/requestid/requestid.go`, `correlationid/correlationid.go` — (Not re-read; no behavioural change since v2-2.)
- `httpx/middleware/clientip/clientip.go` — Defaults to loopback-only trusted proxies; `ParseTrustedProxiesStrict` for fail-loud parsing.
- `httpx/mcp/mcp.go:321-327` — Default actor extractor returns `AnonymousActor` (no header trust). H-7 closed.
- `httpx/mcp/mcp.go:174-216` — `WithActorFromContext` (recommended), `WithActorFromHeader` (with SECURITY WARNING in doc, line 195-208).
- `httpx/mcp/mcp.go:255-279` — `WithStrictAudit(false)` is an explicit opt-out for the audit-precheck gate; default is true (fail-closed).
- `httpx/mcp/server.go:202-218` — `auditPrecheck` gates dispatch on tenant presence in strict mode; the JSON-RPC -32603 error surfaces and the tool DOES NOT execute. v1 H-2 closed correctly.
- `httpx/mcp/server.go:300-338` — `mapErrorToRPC` default branch logs full error server-side and returns generic "internal error" to caller (M-1 from v1 closed). `errUnknownField` wrapped at line 261 / mapped at line 305-311 (L-4 from v1 closed).
- `httpx/mcp/actionlog.go:45-63` — `auditPrecheck` correctly returns false (refuse dispatch) in strict mode; loose mode warns and returns true.
- `grpcx/interceptor/auth.go` — Trusted-S2S marker stamped only by `MTLSAuthUnary`/`Stream`'s mTLS branch (line 405-412), never by the JWT branch. `verifyClientCertGRPC` requires `len(VerifiedChains) > 0` (line 433). `RequirePermissionUnary`/`Stream` and `RequireScopeUnary`/`Stream` panic on empty args. H-2 closed.
- `grpcx/interceptor/recovery.go` — Defer-recover before handler; both unary and stream mirrored.
- `grpcx/interceptor/deadline.go` — Only tightens deadlines (never extends past caller's); deadline cap reachable per-call.
- `grpcx/interceptor/logging.go:107-118` — `isValidID` rejects non-printable ASCII (control characters); generates fresh ID via `contextutil.NewID` when invalid. M-5 closed.
- `grpcx/interceptor/metrics.go` — Labels are method + grpc.Code (no cardinality risk from request data).
- `grpcx/server.go` — Keepalive enforcement default rejects overly aggressive client pings; recovery installed by default; deadline interceptor optional.
- `infra/messaging/natsbackend/natsbackend.go:215-227, 255-257` — `MaxDeliver` defaults to `5` (v1 H-3 closed).
- `examples/agentic-service/internal/app/app.go` — Giant SECURITY header at lines 1-29; HMAC secret has explicit demo-only warning at line 60-69; `dangerousAction` documents its production-only requirements at line 161-173 and uses non-spoofable `"demo-actor"` literal at line 184; `budgetStatus` documents same at line 204-209; slowloris timeouts at line 97-102. H-3 closed.

### Stale-reference check

- `grep -rn "WithProductionDefaults\|WithProductionAllowPlaintext\|WithProductionInternalExposed\|WithJWTAllowAnyIssuer\|WithJWTAllowAnyAudience"` returns zero hits across the whole tree. No stale references in code, tests, or docs.
- `grep -rn "KIT_ENV"` returns only documentation strings ("there is no KIT_ENV escape hatch") — no functional reads remain in the security-critical path.
- Test sites that previously used `KIT_ENV=development` to weaken checks have been migrated: `app/jwt_module_test.go:30-31` documents the migration; `app/production_defaults_test.go` uses explicit `.WithoutTLS()` / `.WithoutJWTAudience()` opt-outs throughout.
- `app/grpc_module_test.go:213`, `app/validate_test.go:22`, `app/module_test.go:337,373` all use the new opt-out methods.

### Defence-in-depth notes (not findings; raised for the next-pass discussion)

These are not regressions or fail-open bugs — but the user said "no findings, including trivial ones", so flag for completeness:

- `infra/sqldb/config.go:327, 334` and `infra/messaging/amqpbackend/config.go:117` and storage backends still call `config.IsDevelopment(environment)` to gate sslmode / TLS / weak-credential checks. These are consumer-explicit env-string passes, not silent KIT_ENV reads, and they're not on the `app.Builder.Run()` validation path (which uses `validateProductionSafety` exclusively). But the dev-mode-removal commit's claim "production settings always apply" is accurate only for the Builder path; consumer-direct callers of `Fields.Validate(envPrefix, environment, "postgres")` still honor the environment parameter. Worth a doc clarification that the Builder is the canonical entry; raw `Fields.Validate` is a lower-level escape hatch.
- The deprecation warn-once on `authz.SubjectFromHeader` is per-process, so if a service constructs the middleware once at boot the warning fires once and is forgotten. Consider promoting to an `slog.Error` (still not a panic, but more visible in operational dashboards) or adding a Prometheus counter so the rate is queryable.
- `jwt_module.go:69` warn-log on the unreachable default branch is correct, but the message says "verifying tokens from any authority" while the actual behaviour is that the option `WithAllowAnyIssuer` is appended. The two say the same thing, but operators reading the log might wonder if they should set the env that was just removed. Consider a one-line message tweak: "no Builder validator ran — invoking jwtutil with WithAllowAnyIssuer; this branch is reachable only via direct app-package import".
- `app/config.go:75` defaults `Environment` to `"production"`, which is informational but means a service running locally without the env set will tag its tracing spans / log lines as "production". Mild operator confusion, not a security issue. Document or default to `"unknown"`.

---

## What was checked vs. v2_SECURITY_REVIEW_2.md scope

Re-read end-to-end (per the audit prompt):
- HTTP middleware: auth, scope, tenant, budget, idempotency, csrf, cors, secheaders, auditlog, timeout, ratelimit (ratelimit + keyed + tenant), signedrequest (signedrequest + noncestore + redis), approval, recover, mcp.
- gRPC interceptors: auth, recovery, deadline, logging, metrics; grpcx/server.go.
- Auth/JWT/TLS: jwtutil/jwtutil.go, jwtutil/config.go, netutil/tls.go, httpx/authz/authz.go.
- App / Builder: config.go, builder.go, validate.go, jwt_module.go, v2_modules.go, builder_helpers.go.
- MCP: mcp.go, server.go, actionlog.go.
- Tenant scoping: core/tenant, data/cache/tenant, data/idempotency/tenant.
- Infra: sqldb/pgx, messaging/buffered_publisher, messaging/natsbackend.
- Examples: examples/agentic-service/internal/app/app.go.

Cross-cutting checks:
- No remaining `WithProductionDefaults` / `WithProduction*` / `WithJWTAllow*` references (rename complete).
- No `KIT_ENV` reads in security-critical paths.
- Validator runs on every `Run()` call (no `Build()` escape).
- Every `Without*` opt-out verified to gate the corresponding check via a typed boolean read by the validator.

---

**Bottom line for tagging v2.0.0**: M-A (IPv6 wildcard escape from C-1) and M-B (silent `AllowPlaintext` honoring in pgx) are real fail-open shapes. Neither is exploitable by default — both require an operator to make a specific configuration choice — but both fit the class of bug the user has already paid for the kit to refuse: "absence of an explicit signal silently relaxes a check". Fix M-A by switching the validator from `==` to `net.IP.IsUnspecified()`. Fix M-B by either renaming the field, build-tag-gating it, or restricting it to loopback DSNs. After that, the regression check is clean and the surface is ready to tag.
