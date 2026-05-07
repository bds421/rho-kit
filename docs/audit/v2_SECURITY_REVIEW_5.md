# v2.0.0 — fifth-pass security review (post-fourth-pass-fix)

**Reviewer**: security-reviewer agent (fifth pass)
**Branch**: main @ `f385c6d` — "fix: close fourth-pass audit findings (N-1..N-5)"
**Scope**: regression check on every prior finding (v2 / v2_2 / v2_3 / v2_4) + a paranoid re-read of the fourth-pass fix surfaces, plus the broad sweep of every middleware / interceptor / config validator the user requested.

---

## Verdict

**Do NOT tag v2.0.0.** The fourth-pass fix introduced one HIGH and one MEDIUM new finding — both direct regressions of the fix's stated intent ("the loopback gate makes the network risk mechanically zero", "all such [zero] forms are rejected"). One LOW UX finding follows from the same fix surface. None of the 27 prior findings regressed; the f385c6d patch landed correctly for the cases it tested but each of its two patch surfaces (`requireLoopbackHost`, `isAllZeroDottedDecimal`) misses a class of input that bypasses the guarantee. The fix attacked the *parser-discrepancy* class of bug but did not extend the new check across pgxpool's full `ConnConfig` shape; it added an `isAllZeroDottedDecimal` predicate but not the `net.ResolveTCPAddr` approach the prior audit recommended. Both gaps reproduce with five-line tests below.

---

## Regression check

For each prior finding: status + current file:line of the fix.

| ID | Title | Status | Current fix location |
|----|-------|--------|----------------------|
| Auth fail-closed (2937115) | `RequirePermission` / `RequireScope` no longer pass-through on missing claim | **still closed** | `httpx/middleware/auth/auth.go:249-271`, `:277-302`, `httpx/middleware/auth/scope.go` |
| C-1 (v2-2) | Internal ops port binds to 0.0.0.0 by default | **still closed** | Default loopback at `app/config.go:37-43`; validator at `app/validate.go:161-163` (but see N-7 below for hex-zero gap) |
| C-2 (v2-2) | Production-defaults skip TLS check | **still closed** | Validator at `app/validate.go:152-154`; opt-out `WithoutTLS` at `app/builder.go:239-242` |
| C-3 (v2-2) | Cross-tenant key collision via `:` in tenant ID | **still closed** | `core/tenant/tenant.go`; length-prefix scoping at `data/cache/tenant/tenant.go` and `data/idempotency/tenant/tenant.go` |
| H-1 (v2-2) | Idempotency middleware collapses to shared-key on empty userID | **still closed** | `httpx/middleware/idempotency/idempotency.go:243-255` |
| H-2 (v2-2) | gRPC auth lacks RequirePermission/RequireScope/IsTrustedS2S | **still closed** | `grpcx/interceptor/auth.go:212-303`; `verifyClientCertGRPC` at `:424-439` |
| H-3 (v2-2) | Example agentic-service is copy-paste hazard | **still closed** | `examples/agentic-service/internal/app/app.go:1-29` |
| H-4 (v2-2) | Budget middleware fails open without WithMultiTenant | **still closed** | Validator at `app/validate.go:125-127` |
| H-5 (v2-2) | WithProductionDefaults does not require WithJWTAudience | **still closed** | Validator at `app/validate.go:145-147`; opt-out `WithoutJWTAudience` at `app/builder.go:253-256` |
| H-6 (v2-2) | `authz.SubjectFromHeader` reads spoofable header | **still closed** | Deprecation warn at `httpx/authz/authz.go`; safe alternatives present |
| H-7 (v2-2) | MCP default actor extractor reads spoofable X-Actor-Id | **still closed** | `httpx/mcp/mcp.go:321-327` |
| H-8 (v2-2) | JWT module KIT_ENV literal-match drift | **still closed** | Pairing enforced at `app/validate.go:138-147` |
| M-1 (v1) | signedrequest ships only MemoryNonceStore | **still closed** | `httpx/middleware/signedrequest/redis/redis.go` |
| M-1 (v2-2) | same as v1 M-1 | **still closed** | (same) |
| M-2 (v2-2) | `RequirePermission("")` does not panic | **still closed** | Panic at `httpx/middleware/auth/auth.go:250-257`, `:278-283`; gRPC at `grpcx/interceptor/auth.go:228, :247, :270, :289` |
| M-3 (v2-2) | JWT permissions claim malformed → silent empty | **still closed** | `security/jwtutil/jwtutil.go:135-148` |
| M-4 (v2-2) | Tenant middleware silently passes safe-method GETs | **still closed** | `httpx/middleware/tenant/tenant.go` |
| M-5 (v2-2) | gRPC logging accepts unvalidated correlation/request IDs | **still closed** | `grpcx/interceptor/logging.go:107-118` |
| M-6 (v2-2) | Auditlog HTTP middleware uses raw r.RemoteAddr | **still closed** | `httpx/middleware/auditlog/auditlog.go:45-53, :87-92` |
| M-7 (v2-2) | Auditlog HTTP middleware does not run on panics | **still closed** | `httpx/middleware/auditlog/auditlog.go:103-118` |
| M-8 (v2-2) | Budget middleware default scope is unset | **still closed** | `httpx/middleware/budget/budget.go` |
| M-9 (v2-2) | CSRF SkipCheck-via-Bearer bypasses Origin allowlist | **still closed** | `httpx/middleware/csrf/csrf.go:265-279, :367-378` |
| M-A (v2-3) | `validateProductionSafety` only catches IPv4 wildcard | **partially closed (see N-7)** | `app/validate.go:31-44, :161-163` — handles `0.0.0.0` and most leading-zero / short-form IPv4 variants but misses hex-encoded zero forms |
| M-B (v2-3) | `pgx.Config.AllowPlaintext` silently honored regardless of caller | **partially closed (see N-6)** | `infra/sqldb/pgx/pgx.go:97-111, :268-292` — uses `pcfg.ConnConfig.Host` (good) but ignores `pcfg.ConnConfig.Fallbacks[*].Host` so multi-host DSN bypasses |
| N-1 (v2-4) | `isUnspecifiedHost` misses Go-accepted wildcard forms | **partially closed (see N-7)** | `app/validate.go:31-44` — closes `00.00.00.00`, `0`, `0.0`, `0.0.0`, `000.000.000.000`, `0.00.00.00` per `TestBuilder_Validates_RejectsIPv4ZeroForms`; misses hex-encoded zero forms |
| N-2 (v2-4) | pgx loopback gate bypassable via URL `?host=` and duplicate `host=` keys | **partially closed (see N-6)** | `infra/sqldb/pgx/pgx.go:104` — `requireLoopbackHost(pcfg.ConnConfig.Host)` correctly rejects URL-form `?host=` override and duplicate-key last-wins per `TestRequireLoopbackHost_RejectsParsedNonLoopback`; the multi-host fallback case is the new gap |
| N-3 (v2-4) | pgx unconditional TLS check bypassable via duplicate `sslmode=` | **closed** | `infra/sqldb/pgx/pgx.go:108, :310-321` — `requireTLSOnParsedConfig` walks `pcfg.ConnConfig.TLSConfig` + `Fallbacks[*].TLSConfig`. Verified against pgx 5.9.2 (`pgconn/config.go:380-423, :731-919`): `disable` → `cc.TLSConfig=nil`, caught; `prefer` → `cc.TLSConfig` non-nil but `Fallbacks[0].TLSConfig=nil`, caught; `allow` → `cc.TLSConfig=nil`, caught. The `last-wins` discrepancy is fully removed because pgxpool itself is the parser. |
| N-4 (v2-4) | `sqldb.Fields.Validate` accepts `sslmode=prefer`/`allow` | **closed** | `infra/sqldb/config.go:367-374` — `validatePostgresSSLMode` rejects `prefer`/`allow` directly with a clear "admits a plaintext fallback on TLS handshake error" message. Note: the function returns nil for `disable` and relies on the caller's secondary check at `:328-330` — a fragile coupling (see "design observations" below) but currently correct for the only caller. |
| N-5 (v2-4) | amqp `RejectWeakCredential` was passed the URL string | **closed** | `infra/messaging/amqpbackend/config.go:129-130, :141-148` — extracts password via `url.Parse` + `User.Password()`, then runs `RejectWeakCredential` on the actual password. `guest:guest` is now rejected (regression test `TestRabbitMQFields_ValidateRabbitMQ/default guest password rejected` covers it). |

No regressions. Three prior fixes (M-A, M-B, N-1) carry over residual gaps (N-6, N-7) detailed below.

---

## New findings

### HIGH

#### N-6. `pgx.requireLoopbackHost` checks only the primary `ConnConfig.Host` — multi-host DSN with a non-loopback fallback bypasses the loopback gate behind `AllowPlaintextLoopbackForTests`

**File**: `infra/sqldb/pgx/pgx.go:97-106, :268-292`

**What's wrong**: The fourth-pass fix correctly delegated DSN parsing to `pgxpool.ParseConfig` and inspects `pcfg.ConnConfig.Host`, but pgx supports comma-separated multi-host DSNs (libpq high-availability syntax — see `pgconn/config.go:205-211, :380-423`) where each host becomes a separate `*FallbackConfig` entry. The kit's check inspects only `pcfg.ConnConfig.Host` (the first host) and ignores `pcfg.ConnConfig.Fallbacks[*].Host`. A DSN like

```
host=localhost,evil.example.com user=u password=p dbname=db sslmode=disable port=5432
```

resolves through `pgxpool.ParseConfig` to:

```text
primary host="localhost" fallbacks=1 fb0{host="evil.example.com" tls=nil}
```

`requireLoopbackHost("localhost")` returns nil (PASSES). `Connect()` returns a working pool. pgx's connection logic at `pgconn/pgconn.go:188-238` walks `[primary] + Fallbacks` and dials each in order — when localhost is unreachable (DNS failure, postgres not running, port conflict), pgx fails over to `evil.example.com` and ships plaintext credentials.

**Verified end-to-end** with the actual `pgxbackend.Connect()`:

```text
=== RUN   TestRepro_MultiHostFallbackBypass
    repro_test.go:19: primary host="localhost" fallbacks=1
    repro_test.go:21:   fb0: host="evil.example.com" tls=false
    repro_test.go:26: Connect result: <nil>
    repro_test.go:28: BYPASS: AllowPlaintextLoopbackForTests permitted a DSN with non-loopback fallback host
--- FAIL: TestRepro_MultiHostFallbackBypass (0.00s)
```

URL form is also vulnerable:

```
postgres://u:p@127.0.0.1:5432,evil.example.com:5432/db?sslmode=disable
```

→ `primary host="127.0.0.1" fallbacks=1 fb0{host="evil.example.com" tls=nil}` → `requireLoopbackHost("127.0.0.1")` PASSES.

**Why this matters**: the fourth-pass commit message claims "the loopback gate makes the network risk mechanically zero". With this bypass, the network risk is NOT mechanically zero — it depends on whether the operator typed a single host or a comma-separated pair. Multi-host DSNs are documented libpq syntax and any HA-capable test fixture or copy-paste from pgx's own examples will use them. The `AllowPlaintextLoopbackForTests` field name is verbose but not load-bearing once the gate itself fails to inspect every host.

**Attack scenario**: A test fixture sets `Config{DSN: "host=localhost,db.staging.local user=u password=p dbname=db sslmode=disable", AllowPlaintextLoopbackForTests: true}` for resilience testing. Code review accepts the verbose flag because "the DSN starts with localhost". Production deployment leaves the flag in place. When the local pg crashes (or wasn't started in the new k8s namespace), pgx fails over to `db.staging.local` and ships plaintext credentials over the cluster network. No log, no warning.

**Suggested fix**: Walk every host pgxpool will dial:

```go
if cfg.AllowPlaintextLoopbackForTests {
    if err := requireLoopbackHost(pcfg.ConnConfig.Host); err != nil {
        return nil, fmt.Errorf("pgx: AllowPlaintextLoopbackForTests is set but DSN host is not loopback: %w", err)
    }
    for i, fb := range pcfg.ConnConfig.Fallbacks {
        if err := requireLoopbackHost(fb.Host); err != nil {
            return nil, fmt.Errorf("pgx: AllowPlaintextLoopbackForTests is set but DSN fallback[%d] host is not loopback: %w", i, err)
        }
    }
}
```

While there, consider also rejecting `[::1]` (with brackets) as a real loopback — currently `requireLoopbackHost("[::1]")` errors because `net.ParseIP("[::1]")` returns nil; bracket-stripping (as done in `app/validate.go:36`) would close that UX gap (LOW finding N-8 below).

**5-line failing test** (place in `infra/sqldb/pgx/pgx_test.go`):

```go
func TestConnect_RejectsMultiHostFallbackBypass(t *testing.T) {
    for _, dsn := range []string{
        "host=localhost,evil.example.com user=u password=p dbname=db sslmode=disable port=5432",
        "postgres://u:p@127.0.0.1:5432,evil.example.com:5432/db?sslmode=disable",
    } {
        _, err := Connect(context.Background(), Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
        require.Errorf(t, err, "DSN with non-loopback fallback host must NOT pass the loopback gate: %s", dsn)
    }
}
```

---

### MEDIUM

#### N-7. `isAllZeroDottedDecimal` misses hex-encoded zero IPv4 forms (`0x0`, `0x0.0x0.0x0.0x0`, `0X00000000`, `0x00.0x00.0x00.0x00`, …) that `net.Listen` accepts as the IPv4 wildcard

**File**: `app/validate.go:50-69`

**What's wrong**: The fourth-pass fix added `isAllZeroDottedDecimal` to catch leading-zero and short-form decimal variants, which correctly closes the cases enumerated in audit finding N-1 (`00.00.00.00`, `0`, `0.0`, `0.0.0`, `000.000.000.000`, `0.00.00.00`). But the predicate accepts only ASCII `'0'` digits — it rejects every form containing `'x'` or `'X'`. Go's `net.Listen` (via cgo's `getaddrinfo` on darwin/linux) accepts hex-encoded numeric host literals, so all of the following bind to `0.0.0.0` despite passing the validator:

| Internal.Host | `isAllZeroDottedDecimal` says | `net.Listen("tcp", host+":0")` actually | Validator behaviour |
|---|---|---|---|
| `0x0` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0X0` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0x00000000` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0X00000000` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0x0.0x0.0x0.0x0` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0X0.0X0.0X0.0X0` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0x00.0x00.0x00.0x00` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0x0.0` | false | binds 0.0.0.0 | **passes — BYPASS** |
| `0.0X0` | false | binds 0.0.0.0 | **passes — BYPASS** |

Verified by binding each form via `net.Listen("tcp", net.JoinHostPort(host, "0"))` and inspecting `Addr().IP.IsUnspecified()` on the returned listener — every one returns `[::]:NNN` indicating dual-stack wildcard.

**Why this matters**: The user's stated bar in the fifth-pass prompt — "Is there an attack where 5 segments or hex-encoded zero (like '0x0.0x0.0x0.0x0') slips past?" — is exactly the gap. The fourth-pass commit message says "all such forms are rejected" but only decimal-zero forms are. The original audit recommended `net.ResolveTCPAddr`, which mirrors `net.Listen`'s actual semantics; the implemented predicate is a hand-rolled approximation that misses the hex case.

**Attack scenario**: An operator who copy-pastes `INTERNAL_HOST=0x0` from a Hacker News comment about IP-address obfuscation, or who sources the value from a config-as-code tool that hex-encodes loopback/wildcard addresses, bypasses the C-1 / M-A / N-1 check entirely. `/metrics` becomes reachable on every interface. Same blast radius as the original C-1 finding.

**Suggested fix**: Replace `isAllZeroDottedDecimal` with the prior audit's recommended `net.ResolveTCPAddr` — which mirrors `net.Listen`'s actual semantics, in one call, for every form:

```go
func isUnspecifiedHost(host string) bool {
    if host == "" {
        return false
    }
    addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, "0"))
    if err != nil {
        return false
    }
    return addr.IP.IsUnspecified()
}
```

Verified: `net.ResolveTCPAddr("tcp", "0x0:0")` returns `&TCPAddr{IP: 0.0.0.0}` whose `IsUnspecified()` is true. Same for every hex form above. The bracket-stripping for IPv6 literals (`[::]` → `::`) is no longer needed because `net.JoinHostPort` re-applies brackets correctly and `ResolveTCPAddr` handles the parse.

If `ResolveTCPAddr`'s DNS lookup behaviour for non-numeric hostnames is undesirable here (it consults the system resolver for non-numeric inputs), restrict to numeric resolution only by calling `(&net.Resolver{PreferGo: true}).LookupIPAddr` with a context whose deadline forces local-only resolution — but in practice the `Validate()` path runs at startup and is not on a hot path; a single resolver call per validate is acceptable.

**5-line failing test**:

```go
func TestBuilder_Validates_RejectsHexZeroIPv4(t *testing.T) {
    for _, host := range []string{"0x0", "0X0", "0x00000000", "0x0.0x0.0x0.0x0", "0x00.0x00.0x00.0x00", "0x0.0", "0.0X0"} {
        cfg := BaseConfig{Internal: InternalConfig{Host: host, Port: 9090}, TLS: validTLSForTest()}
        err := New("svc", "v1", cfg).WithoutJWTAudience().Validate()
        require.Errorf(t, err, "Internal.Host=%q binds to 0.0.0.0 via net.Listen and must be rejected", host)
    }
}
```

---

### LOW

#### N-8. `pgx.requireLoopbackHost` rejects `[::1]` (bracket-wrapped IPv6 loopback) — false-positive UX bug from libpq key=value form

**File**: `infra/sqldb/pgx/pgx.go:276-292`

**What's wrong**: `requireLoopbackHost` does not strip square brackets before passing to `net.ParseIP`. A DSN in libpq key=value form `host=[::1] user=u dbname=db sslmode=disable` parses successfully through `pgxpool.ParseConfig` (which keeps the brackets in `ConnConfig.Host`), and then `requireLoopbackHost("[::1]")` errors with `DSN host "[::1]" is not a loopback address` because `net.ParseIP("[::1]")` returns nil.

URL-form `postgres://u:p@[::1]:5432/db?sslmode=disable` does NOT hit this — pgxpool's URL parser strips brackets before storing in Host. The bug is libpq-form-only.

This is not a security finding — it FAILS CLOSED (rejects a real loopback). But the same `app/validate.go:36` already strips brackets in `isUnspecifiedHost`; the inconsistency between the two checkers is a small-but-real footgun for operators who use IPv6 loopback in libpq form. Worth fixing because the bracket-stripping is one line.

**Suggested fix**:

```go
func requireLoopbackHost(host string) error {
    if host == "" {
        return fmt.Errorf("DSN does not specify a host")
    }
    stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
    low := strings.ToLower(stripped)
    if low == "localhost" {
        return nil
    }
    ip := net.ParseIP(stripped)
    if ip == nil || !ip.IsLoopback() {
        return fmt.Errorf("DSN host %q is not a loopback address", host)
    }
    return nil
}
```

---

## Items checked, no findings

### Re-audit of the f385c6d fix surface (per the user's explicit prompts)

- **`infra/sqldb/pgx/pgx.go:108, :310-321` `requireTLSOnParsedConfig`** — confirmed against pgx 5.9.2 (`pgconn/config.go:380-423, :731-919`):
  - `sslmode=disable`: `tlsConfigs=[nil]` → `cc.TLSConfig=nil` → caught by line 312.
  - `sslmode=prefer`: `tlsConfigs=[tlsConfig, nil]` → `cc.TLSConfig=tlsConfig` (non-nil), `Fallbacks[0].TLSConfig=nil` → caught by line 315-318.
  - `sslmode=allow`: `tlsConfigs=[nil, tlsConfig]` → `cc.TLSConfig=nil` → caught by line 312.
  - `sslmode=require/verify-ca/verify-full`: `tlsConfigs=[tlsConfig]` → `cc.TLSConfig` non-nil, no nil fallbacks → accepted.
  - Multi-host with `sslmode=require`: each host gets its own non-nil TLSConfig → accepted (TLS uniformly enforced).
  - The `last-wins` parser-discrepancy class (audit finding N-3) is fully closed because pgxpool itself is the parser.
- **`infra/sqldb/pgx/pgx.go:97-106` `AllowPlaintextLoopbackForTests` branch** — branching is exclusive (no fall-through to `requireTLSOnParsedConfig`). The single-host loopback-host check correctly catches the cases pinned in `TestRequireLoopbackHost_RejectsParsedNonLoopback`. **The multi-host case is the gap (see N-6).**
- **`infra/sqldb/pgx/pgx.go:276-292` case-insensitive `localhost`** — `strings.ToLower(host) == "localhost"` returns true for `LocalHost`, `LOCALHOST`, `Localhost`. The user's specific concern about `LocalHost.evilco.com` is NOT a bypass: pgxpool.ParseConfig stores the host string as-is (`"LocalHost.evilco.com"`), `strings.ToLower` produces `"localhost.evilco.com"`, which does NOT equal `"localhost"`. Then `net.ParseIP("LocalHost.evilco.com")` returns nil, so the function errors with "is not a loopback address". Verified via real `pgxpool.ParseConfig` call. Correct.
- **`app/validate.go:50-69` `isAllZeroDottedDecimal`** — the user's specific concerns:
  - 5+ segments (`0.0.0.0.0`): `net.Listen` rejects them as DNS lookups for non-existent hostname (`lookup 0.0.0.0.0: no such host`). Not a bypass — `isAllZeroDottedDecimal` correctly returns false because `len(parts) > 4`, AND `net.Listen` also fails. Result: validator and listener agree.
  - Hex-encoded zero (`0x0.0x0.0x0.0x0`): the predicate returns false because the first non-`'0'` char fails the inner loop. **`net.Listen` accepts and binds to `0.0.0.0` — this is the gap (N-7).**
- **`infra/sqldb/config.go:353-374` `validatePostgresSSLMode`** — only one caller (`Fields.Validate` at `:318-330`). The caller's secondary check at `:328-330` rejects the `disable` case that this function lets through. The coupling is fragile (a future second caller would need to re-apply the upstream check) but currently safe. Documented in the function's own comment block. **No finding** (design observation, not bypass).
- **`infra/messaging/amqpbackend/config.go:123-148` `extractAMQPPassword`** — for `amqp://host:5672/` (no userinfo), `extractAMQPPassword` returns `""`, then `RejectWeakCredential("RABBITMQ_PASSWORD", "")` fires the `len(value) < 12` branch with message `"RABBITMQ_PASSWORD must be at least 12 characters long"`. The error message is misleading when the underlying problem is "URL has no credentials at all" — but the URL would also fail upstream when `LoadRabbitMQFields` populates the empty Password into the URL anyway (the URL would then contain `:@` or no userinfo). **No finding** (UX nit; could optionally improve the message to include "RABBITMQ_URL has no userinfo; provide a password or set RABBITMQ_USER/RABBITMQ_PASSWORD" but this is documentation, not security).
- **Stale references** to removed functions: `grep -rn "extractDSNHost|extractSSLMode|requireLoopbackDSN" --include="*.go"` returns only two hits in `infra/sqldb/pgx/pgx_test.go` lines 56 and 86 — both are in COMMENTS describing the historical N-3 and N-2 findings. No stale code references; `requireTLS` only appears as the new `requireTLSOnParsedConfig`. The doc reference to `Config{AllowPlaintext: true}` at `docs/RELEASE_NOTES_v2.md:80` is stale (the field is now `AllowPlaintextLoopbackForTests`); not a security finding because a copy-paste fails to compile, but pre-existing per v2_SECURITY_REVIEW_4.md.

### Comprehensive sweep

- `httpx/middleware/auth/auth.go:249-302` — fail-closed on missing claim AND missing trusted-S2S marker; `RequirePermission("")` and `PermissionByMethod("","")` panic.
- `httpx/middleware/auth/auth.go:121-128` — `verifyClientCert` requires `len(VerifiedChains) > 0`; cannot be tricked by an unverified peer cert.
- `httpx/middleware/auth/auth.go:174-202` — trusted-S2S marker stamped only on the verified-mTLS branch.
- `httpx/middleware/csrf/csrf.go:265-279, :367-378` — Origin allowlist check precedes `SkipCheck` predicate in both double-submit and session-bound flows. KIT_ENV reads removed; secret presence checked at construction (`csrf.New`); session-bound rejects empty session.
- `httpx/middleware/cors/cors.go` — delegates to `jub0bs/cors`; panics on invalid config (correct for boot-time enforcement).
- `httpx/middleware/secheaders/secheaders.go:172-186` — `shouldSetHSTS` correctly gates on `r.TLS != nil` OR `WithForceHSTS` OR (`WithTrustedProxiesForProto` AND verified-IP AND `X-Forwarded-Proto: https`).
- `httpx/middleware/clientip/clientip.go` — defaults to loopback-only trusted proxies; `ParseTrustedProxiesStrict` for fail-loud parsing; X-Real-IP/X-Forwarded-For only honoured when `r.RemoteAddr` itself is a trusted proxy.
- `httpx/middleware/auditlog/auditlog.go:78-122` — `WithTrustedProxies` plumbed into `clientip.ClientIPWithTrustedProxies`; deferred audit + panic-recording in place; re-raises the panic so the outer recovery middleware still produces 500.
- `httpx/middleware/budget/budget.go` — default scope `"tenant"`; backend errors → 503; tenant required.
- `httpx/middleware/idempotency/idempotency.go:222-224, :243-255` — construction panic on missing extractor unless `WithAllowSharedKeys`; empty userID → 400.
- `httpx/middleware/tenant/tenant.go` — `WithRequiredOnSafeMethods` opt-in; default keeps safe-method bypass.
- `httpx/middleware/timeout/timeout.go` — WebSocket bypass requires explicit opt-in.
- `httpx/middleware/signedrequest/signedrequest.go` — verify ordering: timestamp → signature decode → key resolve → body read → MAC compare → nonce store; `nonceStore == nil` panics at construction.
- `httpx/middleware/signedrequest/redis/redis.go` — `SET NX EX` atomic; nil client panics; ttl<=0 panics; failure → 500 (fail-closed).
- `httpx/middleware/recover/recover.go` — `http.ErrAbortHandler` re-raised; `recordingWriter` flags `wroteHeader`.
- `httpx/middleware/maxbody/maxbody.go` — caps body via `http.MaxBytesReader`; reused across MCP and approval middleware.
- `httpx/middleware/ratelimit/{ratelimit,keyed,tenant}.go` — sharded fixed-window; tenant fail-closed on missing tenant (400) and limiter error (500).
- `httpx/middleware/approval/approval.go` — body capped at 64 KiB; tenant required.
- `httpx/healthhttp/handler.go` — `Cache-Control: no-store` on /metrics, /health, /ready.
- `httpx/sign/sign.go:138-147` — `defaultNonce` panics on `crypto/rand` error.
- `httpx/mcp/mcp.go:321-327, :174-216, :255-279` — default actor extractor anonymous (does NOT read X-Actor-Id); `WithStrictAudit(false)` is explicit opt-out for the audit-precheck gate.
- `httpx/mcp/server.go:202-218, :300-338` — `auditPrecheck` refuses dispatch in strict mode; `mapErrorToRPC` surfaces generic "internal error" to caller.
- `grpcx/server.go:191-198` — recovery interceptors prepended; deadline interceptor placed inside recovery so the deferred cancel runs before unwind.
- `grpcx/interceptor/auth.go:212-303, :305-372, :415-439` — RequirePermission/RequireScope panic on empty args; mTLS path requires `len(VerifiedChains) > 0`; trusted-S2S marker stamped only by mTLS branch.
- `grpcx/interceptor/auth.go:443-449` — x-user-id metadata read after CN allowlist check; fails closed on absent or non-UUID.
- `grpcx/interceptor/recovery.go` — defer-recover before handler in unary and stream.
- `grpcx/interceptor/deadline.go` — only tightens deadlines (preserves caller's stricter deadline).
- `grpcx/interceptor/logging.go:107-118` — `isValidID` rejects non-printable ASCII; regenerates on invalid.
- `grpcx/interceptor/metrics.go` — labels are method + grpc.Code (no cardinality risk).
- `security/jwtutil/jwtutil.go:87-151` — Verify validates issuer + audience when set; subject must be non-empty; permissions/scopes warn-on-malformed (no silent downgrade).
- `security/jwtutil/jwtutil.go:329-342` — `defaultHTTPClient` caps response headers at 64 KiB (defence against pathological JWKS responses); body cap at 1 MB.
- `security/jwtutil/jwtutil.go:344-365` — `KeySet()` returns nil when stale beyond `maxStale` (default 1h); downstream verifiers fail-close on nil.
- `security/csrf/*` — `Issuer` mints + verifies session-bound tokens with HMAC; constant-time compare.
- `security/netutil/tls.go:46-100` — `ServerTLS` requires explicit `WithRequireClientCert` for mTLS; `ClientTLS` returns nil if not enabled.
- `security/netutil/ssrf.go` — outbound dialer drops connections to private IPv4/IPv6 ranges; preserves SNI; defaults to TLS 1.3.
- `core/secret/secret.go` — value-receiver redaction; safe by-value; `LogValue` returns `[REDACTED]`.
- `core/tenant/tenant.go` — forbidden-byte set rejects `:` `/` `\x00` etc.; `NewID` calls `ValidateID`.
- `data/cache/tenant/tenant.go`, `data/idempotency/tenant/tenant.go` — length-prefix scoping; cross-tenant collision impossible.
- `app/builder.go:769-782` — `Run()` calls `Validate()` before any infrastructure spins up; no public `Build()` bypasses validation.
- `app/builder.go:212-256` — opt-outs `WithInternalNonLoopback`, `WithoutTLS`, `WithoutJWTAudience` each set a typed boolean read by the validator; cannot be silently elided.
- `app/validate.go:81-186` — `Validate()` returns on the first validation error; the production-safety subset runs at the end of the chain. All checks fire unconditionally.
- `app/jwt_module.go:55-71` — switch is exhaustive with respect to `cfg.allowAnyIssuer` and `cfg.expectedIssuer`. Cannot fall through silently. The error-log branch fires once per process at provider construction (acceptable signal, not crash).
- `infra/redis/config.go:122-139` — `ValidateRedis` requires REDIS_PASSWORD when not using URL; URL-form bypass acknowledged (kit assumes user-supplied URL is intentional). Pre-existing inconsistency vs. AMQP — not a regression.
- `infra/sqldb/config.go:299-337` — environment parameter no longer consulted; check fires unconditionally; tightened sslmode acceptance to `require/verify-ca/verify-full` only via `validatePostgresSSLMode`.
- `infra/storage/s3backend/config.go:100-126`, `infra/storage/azurebackend/config.go:59-76` — environment parameter no longer consulted; weak-credential check uses actual `c.SecretAccessKey` / account key.
- `infra/storage/sftpbackend/config.go:75-101` — `InsecureSkipHostKeyVerify=true` rejected unconditionally in `Validate()`. Note: `sftpbackend.New(cfg)` does NOT call `cfg.Validate()` — pre-existing structural issue, not a regression.
- `infra/messaging/buffered_publisher.go:117, :162-164` — panic on `stateFile == ""` without `WithEphemeralBuffer`.
- `infra/messaging/amqpbackend/debughttp/guard.go:33` — `IsDevelopment` reads here gate debug endpoints OFF in non-dev. Fail-closed; correct.
- `core/config/validate.go:19`, `app/config.go:81-83` — `IsDevelopment` retained as public API; never read from a security-critical path.
- `examples/agentic-service/internal/app/app.go` — package doc explicitly warns against production use; per-handler comments mark each spoofable surface; demo HMAC secret labelled with mandatory rotation note. Pre-existing pattern, no regression.

### Cross-cutting checks

- DSN parsing discrepancies: confirmed pgx 5.9.2 source in `~/go/pkg/mod/github.com/jackc/pgx/v5@v5.9.2/pgconn/config.go`. The fourth-pass fix's assumption about `Fallbacks[].TLSConfig=nil` is correct for sslmode=prefer/allow (tested in-process via `pgxpool.ParseConfig`). The new gap is the multi-host fallback HOST check, not the TLS check.
- Wildcard binding behaviour of `net.Listen` vs `net.ParseIP` vs `net.ResolveTCPAddr`: confirmed via `net.Listen("tcp", host+":0")` against 20+ host strings; the hex-form gap (N-7) reproduces deterministically on darwin (cgo `getaddrinfo`) and linux (Go resolver — same `strconv.ParseInt` semantics with base-0).
- Go workspace: 50+ submodules; `go test ./...` baseline green for app, infra/sqldb, infra/sqldb/pgx, infra/messaging, httpx, security/jwtutil, grpcx.
- Stale-reference sweep across `*.go` and `*.md`: no stale function calls; one stale doc reference at `docs/RELEASE_NOTES_v2.md:80` (`Config{AllowPlaintext: true}` — pre-existing, won't compile if copied, flagged in v2_SECURITY_REVIEW_4.md).
- No new TODO/FIXME with security implications introduced by f385c6d (only pre-existing TODOs in unrelated TODO-marked future modules: `kekaws/kekgcp/kekvault`, `redis-backed leader election`, `redis ratelimit`).

---

## Bottom line for tagging v2.0.0

Three new findings, all in the f385c6d fix surface itself:

- **N-6 HIGH**: pgx multi-host DSN bypasses the loopback gate behind `AllowPlaintextLoopbackForTests`. The fix only inspects `pcfg.ConnConfig.Host` but pgx walks `[primary] + Fallbacks` at dial time. Fix is one for-loop in `Connect()`.
- **N-7 MEDIUM**: hex-encoded zero IPv4 (`0x0`, `0x0.0x0.0x0.0x0`, `0X00000000`, etc.) bypasses `isUnspecifiedHost` despite `net.Listen` binding to 0.0.0.0. Fix is to swap `isAllZeroDottedDecimal` for `net.ResolveTCPAddr` as the prior audit recommended.
- **N-8 LOW**: `requireLoopbackHost` rejects libpq-form `host=[::1]` (false positive — IPv6 loopback is real). Fix is bracket-stripping (one line, mirroring `app/validate.go:36`).

Each has a 5-line failing test inline. None are exploitable by default — all require an operator to construct a specific config — but each fits the user's stated bar: "absence of an explicit signal silently relaxes a check". The shortest patch path is a single PR touching `infra/sqldb/pgx/pgx.go` (close N-6 + N-8) and `app/validate.go` (close N-7 by replacing `isAllZeroDottedDecimal` with `net.ResolveTCPAddr`). After those land, re-audit; the surface around them is otherwise clean and v2.0.0 should be tag-ready.
