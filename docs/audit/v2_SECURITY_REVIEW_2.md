# v2.0.0 — second-pass security review (deep)

**Reviewer**: security-reviewer agent (second pass)
**Branch**: main @ HEAD (post-2937115)
**Scope**: every middleware in `httpx/middleware/`, every gRPC interceptor in `grpcx/interceptor/`, the JWT/JWKS surface in `security/jwtutil/`, the `app.Builder` composition (esp. `WithProductionDefaults`), the tenant scoping wrappers in `data/cache/tenant` and `data/idempotency/tenant`, and the canonical `examples/agentic-service`. The first pass focused on the load-bearing primitives; this pass targeted the class of bug exemplified by the v2 auth fail-open (commit 2937115) — "absence of a signal silently grants access" — across the full middleware stack and the consumer-facing surface.

## Verdict

**Do NOT tag v2.0.0 as-is.** The auth fix in 2937115 closed the worst instance of the "missing-signal ⇒ allow" anti-pattern, but the same shape recurs in: the **internal ops port that exposes Prometheus metrics on 0.0.0.0 by default** in production environments (CRITICAL); the **production-defaults validator that does not require TLS** even when `WithProductionDefaults()` is called (CRITICAL); the **tenant-scoping cache & idempotency wrappers that allow cross-tenant key collisions when a tenant ID contains `:`** (CRITICAL — silent cross-tenant data leak); the **idempotency middleware that collapses to shared-key mode at runtime when `userExtractor(r)` returns ""** (HIGH); the **gRPC auth interceptor that ships with no `RequirePermission`/`RequireScope`/`IsTrustedS2S` analogue at all** (HIGH — every gRPC service rolls its own authorization); the **canonical agentic-service example that mounts `/mcp` and `/admin/*` with no authentication, no rate limit, no CSRF, and reads spoofable `X-Actor` headers as the audit-trail actor** (HIGH — copy-paste hazard); the **production-defaults validator that does not require `WithJWTAudience`** (HIGH — confused-deputy unmitigated by default); and the **budget middleware that fails open when its KeyFunc returns `(_, false)`** (HIGH — a service that wires `WithTenantBudget` without `WithMultiTenant` enforces no budget at all). Several MEDIUM findings (signedrequest's MemoryNonceStore as the only kit-supplied store + no warning in multi-instance deploys; JWT module's literal `KIT_ENV=="production"` guard inconsistent with `kitcfg.IsDevelopment` elsewhere; `RequirePermission("")` not panicking; auditlog mw not behind recover).

The first audit was scoped to "data primitives" and missed every one of these. The class of bug — *fail-open by absence-of-signal, hidden behind a "this is documented as opt-in" rationale* — is endemic in the kit, not isolated to one middleware. Fix the CRITICAL items below before tagging.

---

## CRITICAL findings

### C-1. Internal ops port (`/metrics`, `/health`, `/ready`) binds to `0.0.0.0` by default with no authentication on `/metrics`

**File**: `app/config.go:28-34`, `app/config.go:62-64`, `httpx/healthhttp/handler.go:64-79`, `app/builder.go:980-987`

**What's wrong**:
- `InternalConfig.Addr()` (config.go:28-34) defaults to `0.0.0.0` when `Host == ""`.
- `LoadBaseConfig` (config.go:62-64) constructs `InternalConfig{Port: internalPort}` *without setting Host*, so Host is `""` → resolves to `0.0.0.0`.
- `LoadBaseConfig` (config.go:65) defaults `Environment` to `"production"`.
- `NewInternalHandler` (healthhttp/handler.go:74) mounts `GET /metrics → promhttp.Handler()` with no authentication wrapper.
- `app/builder.go:980-987` creates the internal server from this addr unconditionally.
- There is no `WithInternalHandler` option or middleware-injection point on the internal port.

**Attack scenario**: Any production service built with `app.Builder` that runs on a network without a strictly-enforced NetworkPolicy (raw EC2, Compose, dev clusters, accidentally-routable VPC) exposes the full Prometheus metrics surface to the internet on port 9090. Prometheus metrics emitted by the kit include: `http_requests_total{method,path,status}` (route patterns leak the API surface), `tenant_*` labels in many backends (tenant IDs leak in the clear — see `redmetrics`), `grpc_server_handled_total{grpc_method,grpc_code}` (RPC topology), goroutine/heap stats (process fingerprinting), and any application-defined metrics that include user-supplied labels. An unauthenticated curler can map the entire service.

**The "this is for Docker healthcheck" rationale (line 23) is the same fail-open justification 2937115 fixed**: the comment acknowledges the bind exposure but the kit doesn't enforce that callers actually network-isolate the port.

**Suggested fix** (one of):
- (a) Default `InternalConfig.Host` to `127.0.0.1`. Operators who genuinely need 0.0.0.0 (Docker healthcheck through host networking) opt in explicitly via `INTERNAL_HOST=0.0.0.0`. The `kit-doctor` healthcheck can also dial loopback.
- (b) In `validateProductionDefaults`, panic when `Internal.Host == "0.0.0.0" || Internal.Host == ""` and a flag like `WithProductionInternalExposed()` was not called. Mirror the `WithJWTAllowAnyIssuer` opt-in pattern.
- (c) Add a `WithInternalAuth(mw)` builder option that wraps `/metrics` (at minimum) in a basic-auth or shared-secret middleware. Default to no-op in dev.

**5-line failing test**:
```go
func TestInternalConfig_DefaultsToLoopback(t *testing.T) {
    cfg, _ := app.LoadBaseConfig(8080)
    require.NotEqual(t, "0.0.0.0", cfg.Internal.Host,
        "internal ops port must not bind to 0.0.0.0 by default — exposes /metrics publicly")
}
```

---

### C-2. `WithProductionDefaults()` does NOT require TLS — services serve plaintext HTTP in production with no warning

**File**: `app/validate.go:61-88`, `app/builder.go:776-779`, `app/builder.go:1064-1071`

**What's wrong**: `validateProductionDefaults` enforces JWT-issuer, Postgres-sslmode, and tracing-sample-rate, but has NO check that `cfg.TLS.Enabled()` returns true. Then in `Build` (builder.go:776), `cfg.TLS.ServerTLS()` returns `(nil, nil)` when *any* of `TLS_CA_CERT`/`TLS_CERT`/`TLS_KEY` is empty (netutil/tls.go:22-24, 73). At builder.go:1066-1067 the code conditionally adds `httpx.WithTLSConfig(serverTLS)` only when `serverTLS != nil`. **When TLS is partially configured (operator dropped one env var), the kit silently falls back to plaintext HTTP — no panic, no warn log, no error.** And `WithProductionDefaults()` has no clause to catch this.

**Attack scenario**: An operator deploys with `KIT_ENV=production` and `WithProductionDefaults()`, expecting the kit to fail loudly on misconfig. They typo `TLS_CERT_PATH` instead of `TLS_CERT` (the env var the kit reads). `TLSConfig.Enabled()` returns false, server starts on `:8080` with no TLS, JWTs and tenant headers fly in cleartext over whatever path the request takes. If KIT_ENV-gated migrations/seeders leak credentials into logs, those land in plaintext audit logs.

**Suggested fix**: In `validateProductionDefaults`, after the JWT block, add:
```go
if !b.cfg.TLS.Enabled() {
    return fmt.Errorf("production: TLS must be configured (TLS_CA_CERT, TLS_CERT, TLS_KEY) — partial configuration silently falls back to plaintext HTTP")
}
```
Add a `WithProductionAllowPlaintext()` opt-out for services explicitly fronted by an external TLS terminator (Oathkeeper, ALB), modeled on `WithJWTAllowAnyIssuer`. The opt-out forces the operator to acknowledge.

**5-line failing test**:
```go
func TestProductionDefaults_RequiresTLS(t *testing.T) {
    b := app.New("svc", "v1", app.BaseConfig{}).WithProductionDefaults() // empty TLSConfig
    err := b.Validate()
    require.Error(t, err, "production validator must reject empty TLS config")
}
```

---

### C-3. Tenant-scoping wrappers (`data/cache/tenant`, `data/idempotency/tenant`) allow cross-tenant key collisions when tenant ID contains `:`

**File**: `data/cache/tenant/tenant.go:58-64`, `data/idempotency/tenant/tenant.go:69-75`, `core/tenant/tenant.go:44-49`

**What's wrong**: `scopedKey` in both wrappers builds the namespaced key as `"tenant:" + string(id) + ":" + raw`. The colon is **the separator AND a permitted character in the tenant ID** — `coretenant.NewID` only rejects empty (tenant.go:44). So:
- tenant `a:b`, key `c`     → `"tenant:a:b:c"`
- tenant `a`,   key `b:c`   → `"tenant:a:b:c"`

Two distinct (tenant, key) pairs map to the same backend slot. **Silent cross-tenant data leak in the cache wrapper. Cross-tenant idempotency replay in the idempotency wrapper (tenant `a` retries an Idempotency-Key the attacker controls in tenant `a:b` and gets the attacker's cached response with attacker's body).**

The package docs sell these wrappers as the kit's primary cross-tenant isolation mechanism: cache/tenant.go:9-11 says "Forgetting the prefix is a silent data-leak ... Centralising the prefix removes the class of bug." That guarantee is broken when tenant IDs are operator-supplied (multi-tenant SaaS where customers self-name workspaces).

**Attack scenario**: 
1. SaaS allows customers to name their workspace. Attacker creates workspace `victim:secret-key` (or any name containing `:`).
2. Attacker makes request as their workspace, primes the cache with `Set(ctx, "x", attackerData)`. Cache key is `tenant:victim:secret-key:x`.
3. Victim workspace is named `victim`. Victim makes request that calls `Get(ctx, "secret-key:x")`. Cache key is `tenant:victim:secret-key:x` — **collides**. Victim reads attacker's data (or vice versa).

**Suggested fix** (one of):
- (a) In `coretenant.NewID`, validate against any character that's a separator in any kit subsystem: at least `:`, ideally restrict to a positive allowlist (alphanumeric, `-`, `_`, `.`).
- (b) In `scopedKey`, hex-encode or url-encode the tenant ID portion. Cost is one allocation per call.
- (c) Use a length-prefix: `"tenant:" + strconv.Itoa(len(id)) + ":" + id + ":" + raw`. Robust against any character.

**5-line failing test**:
```go
func TestScopedKey_ColonInTenantIDCollision(t *testing.T) {
    ctx1 := tenant.WithID(context.Background(), tenant.ID("a:b"))
    ctx2 := tenant.WithID(context.Background(), tenant.ID("a"))
    require.NotEqual(t, scopedKey(ctx1, "c"), scopedKey(ctx2, "b:c"),
        "cross-tenant key collision via colon in tenant ID")
}
```

---

## HIGH findings

### H-1. Idempotency middleware collapses to shared-key mode when `userExtractor(r)` returns "" — silent cross-user response replay

**File**: `httpx/middleware/idempotency/idempotency.go:210-235`, `httpx/middleware/idempotency/idempotency.go:397-409`

**What's wrong**: The startup panic at line 210-212 only verifies that *either* `userExtractor != nil` OR `allowSharedKeys == true`. At runtime (line 232-234), the middleware calls `userExtractor(r)` and uses whatever string it returns — including `""`. `fingerprintKey` (line 397-409) only includes the userID in the hash when *non-empty* (line 404 `if userID != ""`). So:

- Authenticated user A: `userExtractor` returns `"alice-uuid"` → key includes alice-uuid.
- Anonymous request (or one whose extractor failed silently — auth context missing, JWT issued without `sub`, request ordered before auth middleware): `userExtractor` returns `""` → key collapses to `(method, path, rawKey)`.

Two anonymous requests with the same Idempotency-Key share a cache slot. **An anonymous request with a guessed key can read a previous anonymous request's cached response body.** Worse: if there's *any* code path where a logged-in user's `userExtractor` returns `""` (e.g., a route mounted before auth middleware, or one that goes through an alt-auth path that doesn't populate the same context key the extractor reads), then logged-in user A's response body is replayable by an attacker who guesses the Idempotency-Key.

This is the **same class of bug** as the auth fail-open: "absence of a signal" silently downgrades to a less-safe mode.

**Suggested fix**: At construction, if `userExtractor != nil`, also require `WithAllowSharedKeys` to be explicitly set OR change the runtime semantics to:
- If `userExtractor != nil` AND it returns `""`, **return 400** ("idempotency requires authenticated request") rather than collapsing the key.
- Document that mixed extractor (sometimes returns user, sometimes "") is not supported.

**5-line failing test**:
```go
func TestIdempotency_EmptyUserCollapsesKey(t *testing.T) {
    extractor := func(r *http.Request) string {
        if r.Header.Get("X-User") != "" { return r.Header.Get("X-User") }
        return ""  // ← realistic: extractor failed to find user
    }
    store := idem.NewMemoryStore()
    h := Middleware(store, WithUserExtractor(extractor))(echoBodyHandler())
    // Request 1: anonymous (no X-User), captures response
    req1 := httptest.NewRequest("POST", "/x", strings.NewReader(`{"v":1}`))
    req1.Header.Set("Idempotency-Key", "k")
    rec1 := httptest.NewRecorder()
    h.ServeHTTP(rec1, req1)
    // Request 2: also anonymous, same key — replays request 1's body
    req2 := httptest.NewRequest("POST", "/x", strings.NewReader(`{"v":2}`))
    req2.Header.Set("Idempotency-Key", "k")
    rec2 := httptest.NewRecorder()
    h.ServeHTTP(rec2, req2)
    require.NotEqual(t, rec1.Body.String(), rec2.Body.String(),
        "two anonymous requests share a cache slot — fail open")
}
```

---

### H-2. gRPC auth interceptor has no `RequirePermission`/`RequireScope`/`IsTrustedS2S` analogue — every gRPC service rolls its own authorization

**File**: `grpcx/interceptor/auth.go` (whole file)

**What's wrong**: The HTTP side has `RequirePermission`, `PermissionByMethod`, `RequireScope`, `RequireScopeStrict`, `IsTrustedS2S`, `WithTrustedS2S`, and the trusted-S2S marker that 2937115 added to make S2S/RBAC composition fail-closed. The gRPC side has only `AuthUnary` and `AuthStream` that validate the JWT and stuff `userID`/`permissions`/`scopes` into context. Then the handler is on its own.

This is a fail-open default *by absence*: developers wiring gRPC will write per-handler `if !slices.Contains(perms, "x") { return forbidden }` checks, and any handler that forgets the check is wide open. Worse, there's no equivalent of `IsTrustedS2S` — gRPC services that want S2S composition (verified mTLS client cert + permitted CN + bypass RBAC) have to re-implement the trusted-S2S marker themselves, and they will get it wrong because the kit doesn't show them how.

The skip-method allow-list (`WithSkipMethods`) is the only first-class gRPC authorization primitive, and it's an inverted default — the safe direction would be "deny by default, allow-list authorized methods per role".

**Suggested fix**: Provide `RequirePermissionUnary(perm string)`, `RequirePermissionStream`, `RequireScopeUnary`, and an `mTLSAuthUnary(provider, allowedCNs)` that mirrors `RequireS2SAuth` — including stamping a trusted-S2S marker on the gRPC context. Document the composition order.

**No simple failing test** — this is an API gap; the failing test is "any consumer service has a gRPC handler that forgot to check permissions". Recommend grepping consumer codebases for the pattern.

---

### H-3. Canonical example `examples/agentic-service` is a copy-paste security hazard

**File**: `examples/agentic-service/internal/app/app.go`, `examples/agentic-service/cmd/agentic-service/main.go`

**What's wrong**: The example is documented as "the canonical v2.0.0 stack" (cmd/main.go:1-2). Anyone learning the kit will start from this. It demonstrates:

1. **`/mcp` (line 51) mounted with NO middleware** — no auth, no rate limit, no CSRF, no tenant. The MCP server's strict-audit guard will refuse to dispatch (because no tenant on context), but a developer will see that and add `WithStrictAudit(false)` rather than wire tenant middleware properly.
2. **`/admin/dangerous-action` (line 52) has no authentication**. The handler reads `X-Tenant-Id` from a header (line 110) and `X-Actor` from a header (line 117). Both are fully spoofable. The handler creates an `approval.Request` with `Actor: r.Header.Get("X-Actor")` — **the audit trail records whatever the attacker says**. An attacker can frame any user for any approval request.
3. **`/admin/budget` (line 53) is unauthenticated** — anyone can query any tenant's budget remaining.
4. **No HTTPS** — line 56 `Addr: ":8080"`. No TLS config.
5. **No `ReadTimeout`/`WriteTimeout`/`IdleTimeout`** — slowloris-vulnerable. Only `ReadHeaderTimeout` is set.
6. **No `MaxBodySize`** — the MCP server caps its own body, but the `/admin/*` handlers are wide open.

The `Run` function comment (line 28-32) excuses this by saying "in a real service this would call app.Builder.WithMultiTenant / .WithTenantBudget / ..." — but there's no example that *actually shows the right way*. So consumers will copy this verbatim.

**Suggested fix**: Either (a) make the example actually use `app.Builder` end-to-end (the right way), or (b) put a giant `// SECURITY: this example is illustrative ONLY ...` header on every handler and a `panic("DO NOT USE IN PRODUCTION — see TODO list at top")` if `KIT_ENV=production`. Option (a) is much better.

---

### H-4. Budget middleware fails open when KeyFunc returns `(_, false)` — silently no-ops if `WithTenantBudget` is wired without `WithMultiTenant`

**File**: `httpx/middleware/budget/budget.go:108-113`, `httpx/middleware/budget/budget.go:42-50`, `app/v2_modules.go:43-48`, `app/builder.go:929-934`

**What's wrong**: The budget middleware's hot path (line 108-113):
```go
key, ok := cfg.key(r)
if !ok {
    next.ServeHTTP(w, r)
    return
}
```
And the default key func is `TenantKeyFunc()` (line 42-50), which returns `("", false)` whenever `tenant.FromContext(r.Context())` reports no tenant.

Now look at `app/v2_modules.go:43-48` — `budgetMiddleware()` returns `httpxbudget.Middleware(...)` whenever `b.budgetSpec != nil`, regardless of whether `b.tenantSpec != nil`. There's no validation that `WithTenantBudget` and `WithMultiTenant` are wired together.

The `app/builder.go:924-926` comment says "1. budget — charges per-tenant; furthest from the network so rejections still see tenant ctx populated. 2. tenant — extracts tenant ID into ctx for budget + handler" — the author *knows* budget needs tenant. But there's no check.

**Attack scenario**: A consumer calls `b.WithTenantBudget(redisBudget)` and forgets `b.WithMultiTenant(...)`. The service starts. Every inbound request hits `budgetMiddleware → tenantKeyFunc → no tenant on ctx → ok=false → next.ServeHTTP`. Budget is never consumed. The operator believes budgets are enforced because the code is wired; they're not.

This is documented in the budget package doc (line 41 "Requests without a tenant pass through unchanged: enforce required-tenant in a separate middleware"). But "documented as opt-in" is exactly the rationale that hid the auth fail-open for so long.

**Suggested fix**: 
- (a) Validate at builder time: if `b.budgetSpec != nil` and `b.tenantSpec == nil`, panic.
- (b) Or change budget middleware: when the key func returns `(_, false)`, return 500 ("budget misconfigured: no key for request"). Force the consumer to either configure tenant or supply a custom KeyFunc.
- (c) Or rename `TenantKeyFunc()` to make the dependency explicit (e.g., `RequireTenantKeyFunc()` that 500s instead of passing through).

**5-line failing test**:
```go
func TestBudget_FailsOpenWithoutTenant(t *testing.T) {
    b := &fakeBudget{allowed: false /* "would reject if asked" */, remaining: 0}
    h := Middleware(b)(okHandler()) // default TenantKeyFunc, no tenant in ctx
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
    require.NotEqual(t, http.StatusOK, rec.Code,
        "budget middleware silently passes through when KeyFunc has no tenant")
}
```

---

### H-5. `WithProductionDefaults` does NOT require `WithJWTAudience` — confused-deputy unmitigated by default

**File**: `app/validate.go:62-65`, `security/jwtutil/jwtutil.go:51-55` (doc), `app/builder.go:372-377`

**What's wrong**: The kit's own `KeySet` doc (jwtutil.go:51-55) says: *"REQUIRED for multi-service deployments — without it, a token issued for service A is silently valid at service B as long as both trust the same signer. Standard JWT confused-deputy mitigation (RFC 7519 §4.1.3)."*

Yet `validateProductionDefaults` (validate.go:62-65) only requires `WithJWTIssuer` or `WithJWTAllowAnyIssuer`. Audience is unchecked. So a production service can ship with `WithJWT(...)` + `WithJWTIssuer(...)` and accept tokens minted for *any* sibling service that trusts the same JWKS.

**Attack scenario**: Two microservices, `users` and `billing`, both trust the same Oathkeeper JWKS. `users` issues tokens with `aud: "users"`. `billing` issues tokens with `aud: "billing"`. Without audience pinning on either service, a low-privilege user with a `users` token can replay it against `billing` and get whatever `billing` grants based on the token's permissions claim. The kit's "production" mode does not catch this.

**Suggested fix**: In `validateProductionDefaults`, require `b.jwtAudience != ""` when `b.jwksURL != ""`. Add an explicit `WithJWTAllowAnyAudience()` opt-out for genuinely multi-audience deployments (rare).

**5-line failing test**:
```go
func TestProductionDefaults_RequiresJWTAudience(t *testing.T) {
    b := app.New("svc", "v1", app.BaseConfig{}).WithProductionDefaults().
        WithJWT("https://example.com/.well-known/jwks.json").
        WithJWTIssuer("https://issuer.example.com")
    require.Error(t, b.Validate(), "production must require WithJWTAudience to mitigate confused-deputy")
}
```

---

### H-6. `authz.SubjectFromHeader` reads spoofable header as authoritative subject — same anti-pattern auth.go fixed

**File**: `httpx/authz/authz.go:115-120`

**What's wrong**:
```go
func SubjectFromHeader(header string) SubjectFunc {
    return func(r *http.Request) string {
        return r.Header.Get(header)
    }
}
```
A consumer who writes `authz.RequirePermission(policy, "delete", res, authz.SubjectFromHeader("X-User-Id"))` is one helper away from auth bypass — anyone can set `X-User-Id: admin` and become admin. The kit *recently fixed* the `X-User-Id` header trust in `auth.go` for exactly this reason (the JWT-only mode rejects `X-User-Id` outright).

The function is documented benignly ("reads a header value") with no warning. It exists alongside the safer `SubjectFromContext`.

**Suggested fix**: Either remove `SubjectFromHeader` (its use case — TLS-terminating ingress sets a trusted header — should be served by an explicit `SubjectFromTrustedHeader(header, []net.IPNet)` that verifies `r.RemoteAddr` is in the trusted-proxy list), or wrap it with a giant doc-warning panic in non-dev when used.

---

### H-7. MCP default actor extractor reads spoofable `X-Actor-Id` — audit-log actor field is attacker-controlled

**File**: `httpx/mcp/mcp.go:262-268`, `httpx/mcp/actionlog.go:100-103`

**What's wrong**: Default `actorExtractor` reads `X-Actor-Id` header verbatim (mcp.go:262-268). The audit log records this as the `Actor` field of the signed entry (actionlog.go:100-103). **An attacker can frame any user for any tool call by setting `X-Actor-Id: alice`.** The signed-store guarantees the *integrity* of the audit entry, not the *authenticity* of the actor.

The doc (mcp.go:137-143) does say "Services that already place the actor on context via auth middleware should override" — but the *default* is dangerous. The `examples/agentic-service` example doesn't override. So consumers learning from the example get this default.

**Suggested fix**: Default `actorExtractor` to `func(r *http.Request) string { return auth.UserID(r.Context()) }` (from the kit's own auth middleware). If the auth middleware isn't installed and `UserID` returns "", record `"anonymous"` and refuse to dispatch in strict mode (extending the existing audit-precheck). Make `WithActorFromHeader(header)` an explicit opt-in that documents the trust requirement.

---

### H-8. JWT module's KIT_ENV=production guard is literal-string match, inconsistent with `kitcfg.IsDevelopment`

**File**: `app/jwt_module.go:40-42`, `security/jwtutil/jwtutil.go:262-270`, `core/config/config.go` (`IsDevelopment`)

**What's wrong**: `app/jwt_module.go:40` uses `os.Getenv("KIT_ENV") == "production"` — strict equality, only matches the exact string `"production"`. By contrast, `security/jwtutil/jwtutil.go:267` uses `!kitcfg.IsDevelopment(env)` — a more comprehensive check that fires for any non-dev environment.

So a service with `KIT_ENV=prod` (common abbreviation), `KIT_ENV=PRODUCTION`, `KIT_ENV=staging`, or `KIT_ENV=production-eu` will pass the `app/jwt_module.go` check (which is only inside `app.Builder` path) but fail the `security/jwtutil` check (which fires from `NewProvider` directly). The two layers disagree on what "production" means.

**Attack scenario**: An operator standardises on `KIT_ENV=prod` (saving 6 characters in their helm values). They use `app.Builder.WithJWT(...)` without `WithJWTIssuer`. The `app/jwt_module.go` check at line 40 passes (because "prod" != "production"). The `jwt_module.Init` then calls `jwtutil.NewProvider` (jwt_module.go:76) — which *does* fire the panic via `kitcfg.IsDevelopment`. So the user experiences a panic from the wrong place. Less bad than a silent bypass, but it still leaks the inconsistency.

The bigger risk: a future refactor that drops the `jwtutil.NewProvider` panic (because "the app layer already enforces it") would silently allow `KIT_ENV=prod` services to skip issuer enforcement entirely.

**Suggested fix**: Replace `os.Getenv("KIT_ENV") == "production"` with `!kitcfg.IsDevelopment(os.Getenv("KIT_ENV"))` to match the lower layer.

---

## MEDIUM findings

### M-1. `signedrequest.Middleware` ships only `MemoryNonceStore`; multi-instance deployments silently get per-replica replay protection

**File**: `httpx/middleware/signedrequest/noncestore.go:8-16`, `httpx/middleware/signedrequest/signedrequest.go:122-124`

The `Middleware` constructor panics on nil `NonceStore` — good. But the kit only ships `MemoryNonceStore`, which the doc itself describes as "single-instance only". A multi-replica deployment using `MemoryNonceStore` has per-replica replay windows: an attacker who captures a signed request can replay it against a *different* replica within the TTL. There is no warning at construction time, no Redis-backed `NonceStore` shipped, and no example showing how to write one safely.

**Fix**: Ship a `RedisNonceStore` (using `SET NX EX` with the nonce). Add a startup-time check: if the kit can detect from the deployment that there are multiple replicas (via `KIT_REPLICA_COUNT` env or similar) AND the configured store is `*MemoryNonceStore`, log a fatal warning.

### M-2. `RequirePermission("")` and `RequireScope("")` do not panic

**File**: `httpx/middleware/auth/auth.go:242-256`, `httpx/middleware/auth/scope.go:26-41`

A misconfigured route or empty config var that yields `RequirePermission("")` will check whether the permission set contains the empty string. If the JWT issuer ever includes `""` in the permissions array (defensive list construction, accidental empty-element push), the empty-permission check passes. Both `RequirePermission` and `RequireScope` should panic at construction when their argument is empty.

### M-3. JWT permissions claim type-flexibility silently downgrades to nil — fail-closed but masks malformed-token telemetry

**File**: `security/jwtutil/jwtutil.go:127-138`

When the `permissions` claim is wrong type (a string instead of array, a map, etc.), `tok.Get` returns an error and `claims.Permissions` is nil. The auth middleware then fail-closes (good). But the kit only logs at Debug level (line 131 `slog.Debug("jwt: permissions claim absent or invalid", ...)`) — operators won't see this. A misconfigured issuer that ships malformed `permissions` for a class of users will silently lock them out without producing a visible error. Bump to `Warn` and include a metric counter (`jwt_malformed_claims_total{claim="permissions"}`) so operators see the rate.

### M-4. Tenant middleware silently accepts requests with no tenant on safe HTTP methods even when `WithRequired(true)`

**File**: `httpx/middleware/tenant/tenant.go:77-97`

Lines 87-91: GET/HEAD/OPTIONS bypass the tenant requirement unconditionally. Documented as "health and discovery endpoints stay reachable pre-auth", but the bypass applies to *every* GET handler — including state-revealing ones (`GET /api/admin/users` lists all users). A handler that calls `tenant.FromContext` on a GET will see no tenant and either error out (good) or default to listing across all tenants (bad).

The current design conflates "this method is idempotent" with "this method is unauthenticated/pre-auth". They are not the same. Fix: split into two predicates — `WithRequired(required bool)` for state-changing methods (current behavior) and `WithRequiredOnSafeMethods(predicate func(r *http.Request) bool)` so consumers can opt-in to tenant enforcement for non-health GETs.

### M-5. gRPC logging interceptor accepts unvalidated correlation/request IDs from metadata — log-injection vector

**File**: `grpcx/interceptor/logging.go:67-79`

`extractIDs` reads `correlationIDKey` / `requestIDKey` from incoming metadata and stuffs the values directly into context (which then flows into structured logs). The HTTP equivalent (`requestid.WithRequestID`, `correlationid.WithCorrelationID`) calls `idutil.IsValid` to reject IDs containing control characters. The gRPC version does not. Slog's JSON encoder will escape control bytes, so this isn't a *log poisoning* vulnerability per se, but it's a defense-in-depth gap and the asymmetry with HTTP is surprising. Apply the same `idutil.IsValid` check.

### M-6. Auditlog HTTP middleware uses raw `r.RemoteAddr` (with port, no proxy honor)

**File**: `httpx/middleware/auditlog/auditlog.go:81`

`Event.IPAddress: r.RemoteAddr` records the raw host:port, ignoring trusted proxies. Behind any TLS-terminating ingress, every audit log line records the proxy IP, not the real client. Misleading forensics. Use the same `clientip.ClientIPWithTrustedProxies` resolver pattern as the access log middleware.

### M-7. Auditlog HTTP middleware does not run audit on panics — partial audit gap

**File**: `httpx/middleware/auditlog/auditlog.go:58-87`

The middleware calls `next.ServeHTTP(rec, r)` then `l.Log(...)` afterwards. If `next` panics, the audit Log call is never reached. The recover middleware (typically wrapping this) catches the panic and writes a 500 — but the audit entry is missing. So the audit log lies: panics produce 500 responses with no audit record.

Fix: `defer` the `l.Log` call, and rely on a separate `panicked bool` flag so the deferred call records `Status: "failure"` even when the handler panicked.

### M-8. Budget middleware default scope name is unset — debug headers omit scope on rejection

**File**: `httpx/middleware/budget/budget.go:130-145`

Minor UX issue: `WithScope("")` is allowed and the rejection response then omits the `X-Budget-Scope` header. Operators reading their dashboards see anonymous 429s. Default to "tenant" (matching `httpx/middleware/ratelimit/tenant`) when no scope is set.

### M-9. CSRF SkipCheck-via-Bearer bypasses Origin allowlist

**File**: `httpx/middleware/csrf/csrf.go:275-289`

The skip-check fires before the origin allowlist check (line 275 then line 284). A consumer that uses both `WithSkipCheck(HasBearerToken)` and `WithAllowedOrigins(...)` expects defense-in-depth: bearer-token requests skip CSRF cookie matching, but unfamiliar-origin POSTs are still rejected. Today, a bearer-bearing POST from any origin passes. Reorder: run the origin check first, then the skip-check.

---

## LOW findings

### L-1. `auth.go:182-183` reads `r.TLS.PeerCertificates` without re-checking `VerifiedChains`

`requireHeaderUser` is only reached after `verifyClientCert` succeeded (which checks both), so this is safe in context. But the access pattern is fragile — a future refactor that splits the verify/header steps could regress to PeerCertificates-only. Add a defensive re-check or document the precondition with a comment.

### L-2. `httpx/sign` ignores `crypto/rand.Read` error in nonce generator

**File**: `httpx/sign/sign.go:138`

`_, _ = rand.Read(b[:])` — on Linux this never fails. But theoretically, on a misconfigured system, the nonce becomes all zeros, defeating replay protection. Surface the error: panic at construction or fail the RoundTrip.

### L-3. Timeout middleware bypasses on `Upgrade: websocket` header — generic bypass for *any* endpoint

**File**: `httpx/middleware/timeout/timeout.go:78-81`

Any client can send `Upgrade: websocket` to skip the per-request timeout, even on routes that aren't WebSocket endpoints. The handler probably doesn't actually do a WS upgrade, but it runs unbounded. Recommendation: only bypass when the *route* opted into WebSocket via configuration, not when the *header* says so.

### L-4. `GET /metrics` does not Cache-Control no-store

**File**: `httpx/healthhttp/handler.go:74`

`promhttp.Handler()` doesn't set Cache-Control. A misconfigured CDN/proxy could cache and serve stale metrics. Wrap with a no-store header.

### L-5. JWKS Provider has 1 MB body cap but no header limit

**File**: `security/jwtutil/jwtutil.go:388`

The body read uses `io.LimitReader(resp.Body, 1<<20)` — good. But the JWKS endpoint response could carry pathological headers. The `defaultHTTPClient` (line 326) uses default `MaxResponseHeaderBytes` (1 MB), which is plenty in practice; not a real issue but worth noting if hardening further.

---

## Items checked, no findings

Each line documents an audit step that did not surface a bug. Provided so you can verify coverage.

- `httpx/middleware/recover/recover.go` — re-raises `http.ErrAbortHandler` correctly; `recordingWriter` flags wroteHeader before delegating; Hijack delegates safely.
- `httpx/middleware/cors/cors.go` — delegates to `jub0bs/cors`, which enforces credentials/wildcard restriction.
- `httpx/middleware/cspnonce/cspnonce.go` — fail-closed on rand.Read error; nonce is base64-safe; templates get HTMLAttr type.
- `httpx/middleware/maxbody/maxbody.go` — single-purpose, panics on non-positive limit.
- `httpx/middleware/secheaders/secheaders.go` — `shouldSetHSTS` correctly gates on `r.TLS != nil` OR trusted-proxy + `X-Forwarded-Proto: https`. WithoutHSTS is explicit opt-out.
- `httpx/middleware/requestid/requestid.go` — validates incoming X-Request-Id via `idutil.IsValid` (rejects control chars).
- `httpx/middleware/correlationid/correlationid.go` — same.
- `httpx/middleware/clientip/clientip.go` — defaults to loopback-only trusted proxies; `ParseTrustedProxiesStrict` for fail-loud parsing.
- `httpx/middleware/ratelimit/ratelimit.go` — sharded fixed-window, atomic per-shard mutex; LRU caps memory; cleanup is bounded.
- `httpx/middleware/ratelimit/keyed.go` — same algorithmic safety.
- `httpx/middleware/ratelimit/tenant/tenant.go` — fail-closed on missing tenant (400) AND on limiter error (500).
- `httpx/middleware/approval/approval.go` — `next` is intentionally unused; tenant required (400 on absent); body capped at 64 KB by default.
- `httpx/middleware/idempotency/internal` paths — body fingerprint correctly hashed when enabled; identity-bearing response headers stripped before caching.
- `httpx/middleware/signedrequest/signedrequest.go` — verify order is correct (signature → nonce); body bounded by `WithBodyMaxSize`; canonical includes method/path/host/ts/nonce/body-hash plus pinned headers.
- `httpx/middleware/signedrequest/noncestore.go` — `SeenOrStore` is atomic per call; sweep is bounded.
- `httpx/middleware/tracing/tracing.go` — span name updated to r.Pattern post-dispatch (no cardinality blowup); hijacked connections marked as 101 Switching Protocols.
- `httpx/middleware/logging/logging.go` — accepts custom resolver; `clientip.ClientIP` defaults to loopback-only.
- `httpx/middleware/metrics/metrics.go` — uses `r.Pattern` for label (cardinality safe); skips hijacked.
- `httpx/middleware/stack/chain.go` — panics on nil handler.
- `grpcx/interceptor/recovery.go` — defer-recover before handler; both unary and stream mirrored.
- `grpcx/interceptor/deadline.go` — only tightens deadlines (never extends past caller's); deadline cap reachable per-call.
- `grpcx/interceptor/metrics.go` — labels are method + grpc.Code (no cardinality risk from request data).
- `grpcx/server.go` — keepalive enforcement policy default rejects overly aggressive client pings; recovery installed by default; deadline interceptor optional but recommended.
- `httpx/budget/budget.go` (outbound RoundTripper) — refunds on transport error; reconcile is best-effort; no auth-leak (doesn't touch Authorization).
- `httpx/sign/sign.go` — body bounded; re-installs body reader after read; secret copied (not aliased to caller).
- `httpx/reqsign/transport.go` — clones the request (no caller mutation).
- `security/jwtutil/jwtutil.go` — `Verify` calls jwx with `WithValidate(true)` (exp/nbf checked); JWKS body capped at 1 MB; clock skew 30s; `KeySet()` returns nil on stale (fail-closed downstream).
- `security/jwtutil/jwtutil.go` — `ParseKeySet` rejects empty key sets; `tok.Subject()` empty rejected; `uuidPattern` enforced in HTTP auth.
- `security/csrf/csrf.go` (Issuer for session-bound CSRF) — HMAC-bound to session; TTL bounded.
- `security/netutil/tls.go` — TLS 1.3 minimum; client cert verification via `VerifyClientCertIfGiven` or `RequireAndVerifyClientCert`.
- `data/idempotency/pgstore` — table name validated by regex (line 26, 67); all queries use `$1` placeholders. (Sprintf only interpolates the validated table name.)
- `data/actionlog/postgres`, `data/approval/postgres` — no `Sprintf` SQL construction.
- `cmd/kit-doctor`, `cmd/kit-new`, `cmd/kit-bench-gate` — no security-sensitive surface.
- `httpx/healthhttp/handler.go` — health/ready/metrics route mounting (auth concern flagged in C-1; mounting itself is fine).
- `examples/app` (the simpler example) — uses `app.Builder` correctly; no spoofable headers.

---

## Items the first audit covered, double-checked

- HMAC compare paths use `hmac.Equal` (csrf, signedrequest, sign) — confirmed.
- Postgres approval store wraps `Decide`/`MarkExecuted` in `SELECT … FOR UPDATE` — confirmed.
- Redis budget Lua is atomic — confirmed (not re-read; first audit's verification stands).
- MCP `recordActionLog` no-tenant fail-open (H-2 in v1) — fix confirmed in `httpx/mcp/actionlog.go:45-63` (`auditPrecheck` denies in strict mode).
- `secret.String` redaction (H-1 in v1) — value-receiver issue still present per first audit; re-audit recommended after fix.
- Tenant-middleware composition order (signedrequest → tenant → budget → handler) — confirmed at `app/builder.go:929-937`.

---

**Bottom line for tagging v2.0.0**: C-1 (internal port exposure), C-2 (production-defaults skips TLS), and C-3 (cross-tenant key collision) are show-stoppers — each one is a "default-deny" violation as severe as the auth fail-open 2937115 fixed. H-1 (idempotency cross-user replay), H-3 (example service insecure), H-4 (budget fail-open), H-5 (audience not required), and H-7 (MCP actor spoofable) are within the same class. Fix the CRITICALs and at least H-3, H-4, H-5 before tagging.
