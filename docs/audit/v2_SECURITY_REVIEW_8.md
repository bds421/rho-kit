# v2.0.0 — eighth-pass security review (post-N-10 fix)

**Reviewer**: security-reviewer agent (eighth pass)
**Branch**: main @ `57a8258` — "fix(app/validate): reject bracket-only host forms (N-10)"
**Scope**: regression check on every prior finding (v2 / v2_2 / v2_3 / v2_4 / v2_5 / v2_6 / v2_7) + paranoid re-read of the 57a8258 fix surface (the new bracket-only short-circuit in `app/validate.go isUnspecifiedHost`), plus the comprehensive sweep of every middleware / interceptor / config validator the user requested.

---

## Verdict

**Clean — tag v2.0.0.**

The 57a8258 fix lands the seventh-pass review's recommended Option B verbatim and pins it with three sub-tests (`[]`, `[`, `]`) under `TestBuilder_Validates_RejectsIPv4ZeroForms`. The new `if stripped == "" { return true }` branch is purely additive, fires on exactly three reachable inputs, and is correct or harmlessly over-conservative on every one of them. The empty-host path (`Internal.Host = ""`) remains correctly handled — defaulted to `127.0.0.1` at `app/config.go:37-42`, validator returns `false` because the listener never binds a wildcard. No regressions in the broader codebase are mechanically possible because 57a8258 touches only `app/validate.go`, `app/production_defaults_test.go`, and the prior audit doc — all other files are byte-identical to the seventh-pass-clean baseline. All security-relevant tests pass.

The four-cycle pattern N-1 → N-7 → N-9 → N-10 is now exhausted: the validator and `net.Listen` are in lock-step on every reachable input. The C-1 surface is genuinely sealed.

**Tag v2.0.0.**

---

## Regression check

For each prior finding: status + current file:line of the fix.

| ID | Title | Status | Current fix location |
|----|-------|--------|----------------------|
| Auth fail-closed (2937115) | `RequirePermission` / `RequireScope` no longer pass-through on missing claim | **still closed** | `httpx/middleware/auth/auth.go:249-271`, `:277-302`, `httpx/middleware/auth/scope.go` |
| C-1 (v2-2) | Internal ops port binds to 0.0.0.0 by default | **still closed** | Default loopback at `app/config.go:37-43`; validator at `app/validate.go:38-61, :153-155` |
| C-2 (v2-2) | Production-defaults skip TLS check | **still closed** | Validator at `app/validate.go:144-146`; opt-out `WithoutTLS` at `app/builder.go` |
| C-3 (v2-2) | Cross-tenant key collision via `:` in tenant ID | **still closed** | `core/tenant/tenant.go`; length-prefix scoping at `data/cache/tenant/tenant.go` and `data/idempotency/tenant/tenant.go` |
| H-1 (v2-2) | Idempotency middleware collapses to shared-key on empty userID | **still closed** | `httpx/middleware/idempotency/idempotency.go:243-255` |
| H-2 (v2-2) | gRPC auth lacks RequirePermission/RequireScope/IsTrustedS2S | **still closed** | `grpcx/interceptor/auth.go:212-303`; `verifyClientCertGRPC` at `:424-439` |
| H-3 (v2-2) | Example agentic-service is copy-paste hazard | **still closed** | `examples/agentic-service/internal/app/app.go:1-29` |
| H-4 (v2-2) | Budget middleware fails open without WithMultiTenant | **still closed** | Validator at `app/validate.go:117-119` |
| H-5 (v2-2) | WithProductionDefaults does not require WithJWTAudience | **still closed** | Validator at `app/validate.go:137-139`; opt-out `WithoutJWTAudience` |
| H-6 (v2-2) | `authz.SubjectFromHeader` reads spoofable header | **still closed** | Deprecation warn at `httpx/authz/authz.go`; safe alternatives present |
| H-7 (v2-2) | MCP default actor extractor reads spoofable X-Actor-Id | **still closed** | `httpx/mcp/mcp.go:321-327` |
| H-8 (v2-2) | JWT module KIT_ENV literal-match drift | **still closed** | Pairing enforced at `app/validate.go:130-139` |
| M-1 (v1) | signedrequest ships only MemoryNonceStore | **still closed** | `httpx/middleware/signedrequest/redis/redis.go` |
| M-1 (v2-2) | same as v1 M-1 | **still closed** | (same) |
| M-2 (v2-2) | `RequirePermission("")` does not panic | **still closed** | Panic at `httpx/middleware/auth/auth.go:256, :279, :282`; gRPC at `grpcx/interceptor/auth.go:228-229` |
| M-3 (v2-2) | JWT permissions claim malformed → silent empty | **still closed** | `security/jwtutil/jwtutil.go:135-148` |
| M-4 (v2-2) | Tenant middleware silently passes safe-method GETs | **still closed** | `httpx/middleware/tenant/tenant.go` |
| M-5 (v2-2) | gRPC logging accepts unvalidated correlation/request IDs | **still closed** | `grpcx/interceptor/logging.go:107-118` |
| M-6 (v2-2) | Auditlog HTTP middleware uses raw r.RemoteAddr | **still closed** | `httpx/middleware/auditlog/auditlog.go:45-53, :87-92` |
| M-7 (v2-2) | Auditlog HTTP middleware does not run on panics | **still closed** | `httpx/middleware/auditlog/auditlog.go:103-118` |
| M-8 (v2-2) | Budget middleware default scope is unset | **still closed** | `httpx/middleware/budget/budget.go` |
| M-9 (v2-2) | CSRF SkipCheck-via-Bearer bypasses Origin allowlist | **still closed** | `httpx/middleware/csrf/csrf.go:265-279, :367-378` |
| M-A (v2-3) | `validateProductionSafety` only catches IPv4 wildcard | **still closed** | `app/validate.go:38-61, :153-155` — handles canonical/leading-zero/hex/octal/overflow/IPv6/IPv4-mapped + bracket-only via post-strip-empty short-circuit. |
| M-B (v2-3) | `pgx.Config.AllowPlaintext` silently honored regardless of caller | **still closed** | `infra/sqldb/pgx/pgx.go:97-116` — Fallbacks loop |
| N-1 (v2-4) | `isUnspecifiedHost` misses Go-accepted wildcard forms | **still closed** | `app/validate.go:38-61` — handles every form `ResolveTCPAddr` resolves + post-strip-empty form. |
| N-2 (v2-4) | pgx loopback gate bypassable via URL `?host=` and duplicate `host=` keys | **still closed** | `infra/sqldb/pgx/pgx.go:108-116` |
| N-3 (v2-4) | pgx unconditional TLS check bypassable via duplicate `sslmode=` | **still closed** | `infra/sqldb/pgx/pgx.go:118-121, :325-336` |
| N-4 (v2-4) | `sqldb.Fields.Validate` accepts `sslmode=prefer`/`allow` | **still closed** | `infra/sqldb/config.go:320, :353-374` |
| N-5 (v2-4) | amqp `RejectWeakCredential` was passed the URL string | **still closed** | `infra/messaging/amqpbackend/config.go:129-148` |
| N-6 (v2-5) | pgx multi-host fallback bypass | **still closed** | `infra/sqldb/pgx/pgx.go:108-116`; tests at `pgx_test.go:85-93, :97-105` |
| N-7 (v2-5) | hex-encoded zero IPv4 forms bypass `isAllZeroDottedDecimal` | **still closed** | `app/validate.go:38-61` — `ResolveTCPAddr` delegation. |
| N-8 (v2-5) | `requireLoopbackHost` rejects bracket-wrapped IPv6 loopback | **still closed** | `infra/sqldb/pgx/pgx.go:296-302` |
| N-9 (v2-6) | single-segment IPv4 numeric overflow (`4294967296`, `0x100000000`) | **still closed** | `app/validate.go:38-61`; tests at `app/production_defaults_test.go:198-208` |
| N-10 (v2-7) | `INTERNAL_HOST="[]"` bypasses wildcard validator | **closed** | `app/validate.go:46-55` — post-strip-empty short-circuit; tests at `app/production_defaults_test.go:209-214` cover `[]`, `[`, `]` |

No regressions. Every prior finding still closes.

---

## New findings

**None.**

The 57a8258 fix is a 10-line additive change (8 lines comment + 2 lines logic) at `app/validate.go:46-55`. No existing code path is modified. Every reachable input that triggers the new branch is correct or harmlessly over-conservative. The empty-host path (`""`) is unaffected because the pre-existing line 39-41 short-circuit takes that path before the strip runs.

---

## Items checked, no findings

### Re-audit of the 57a8258 fix surface (per the user's three explicit prompts)

#### Q1: bracket-only post-strip-empty branch coverage

The user asked: "does the post-strip-empty short-circuit cover every input form that produces an empty string after `TrimPrefix("[")` + `TrimSuffix("]")`? `[]`, `[`, `]`, `[[`, `]]`, `[ ]` (whitespace inside)". Empirical results from running the actual `isUnspecifiedHost` against `net.Listen`:

| Input | After strip | validator | net.Listen | Agree on security verdict? |
|-------|-------------|-----------|------------|---------------------------|
| `[]` | `""` | **true** (new branch) | binds `[::]:N` (wildcard) | **YES — N-10 closed** |
| `[` | `""` (TrimSuffix no-op, TrimPrefix removes `[`) | **true** (new branch) | err `missing ']' in address` | yes — over-conservative, listener fails anyway |
| `]` | `""` (TrimSuffix removes `]`, TrimPrefix no-op) | **true** (new branch) | err `unexpected ']' in address` | yes — over-conservative, listener fails anyway |
| `[[` | `[` (TrimSuffix no-op, TrimPrefix removes one `[`) | false | err `missing ']' in address` | yes (both reject) |
| `]]` | `]` (TrimSuffix removes one `]`, TrimPrefix no-op) | false | err `unexpected ']' in address` | yes (both reject) |
| `[ ]` | `" "` (TrimSuffix removes `]`, TrimPrefix removes `[`) | false (DNS NXDOMAIN on " ") | err `lookup  : no such host` | yes (both reject) |
| `[ ` | `[ ` (TrimSuffix no-op, TrimPrefix no-op since not exactly `[`) | wait — TrimPrefix removes the leading `[`, leaving `" "` | false | err `missing ']' in address` | yes (both reject) |
| ` ]` | ` ` (TrimSuffix removes `]`, TrimPrefix no-op) | false | err `unexpected ']' in address` | yes (both reject) |
| `[]]` | `]` (TrimSuffix `]`, TrimPrefix `[`) | false | err `missing port in address` | yes (both reject) |
| `[[]` | `[` (TrimSuffix `]`, TrimPrefix one `[`) | false | err `unexpected '[' in address` | yes (both reject) |
| `[\x00]` | `\x00` (TrimSuffix `]`, TrimPrefix `[`) | false | err `lookup : invalid argument` | yes (both reject) |
| `[\t]` | `\t` | false | err `lookup tab: no such host` | yes (both reject) |

Empirical run output:

```
"[]"         validator=true  net.Listen=OK ip=:: unspec=true
"["          validator=true  net.Listen=ERR(listen tcp: address [:0: missing ']' in address)
"]"          validator=true  net.Listen=ERR(listen tcp: address ]:0: unexpected ']' in address)
"[["         validator=false  net.Listen=ERR(listen tcp: address [[:0: missing ']' in address)
"]]"         validator=false  net.Listen=ERR(listen tcp: address ]]:0: unexpected ']' in address)
"[ ]"        validator=false  net.Listen=ERR(listen tcp: lookup  : no such host)
" "          validator=false  net.Listen=ERR(listen tcp: lookup  : no such host)
"[ "         validator=false  net.Listen=ERR(listen tcp: address [ :0: missing ']' in address)
" ]"         validator=false  net.Listen=ERR(listen tcp: address  ]:0: unexpected ']' in address)
"[\x00]"     validator=false  net.Listen=ERR(listen tcp: lookup  : invalid argument)
"[\t]"       validator=false  net.Listen=ERR(listen tcp: lookup tab: no such host)
"[]]"        validator=false  net.Listen=ERR(listen tcp: address []]:0: missing port in address)
"[[]"        validator=false  net.Listen=ERR(listen tcp: address [[]:0: unexpected '[' in address)
```

**Conclusion**: the new branch fires on exactly the three inputs where stripping produces `""` (`[]`, `[`, `]`). Of those, `[]` is the actual security concern (binds wildcard); `[` and `]` are listener-rejected so flagging is harmlessly over-conservative. The user's other suggested cases (`[[`, `]]`, `[ ]`) all leave a non-empty stripped string and fall through to `ResolveTCPAddr`, which correctly returns false for them — and `net.Listen` rejects all three too, so agreement holds. No bypass.

#### Q2: empty-host semantics with raw BaseConfig

The user asked: "is there any code path where the validator sees the BaseConfig with `Internal.Host = ""` (raw struct, no Loader involvement) and the empty-host short-circuit is wrong? Verify InternalConfig.Addr's defaulting still holds."

Verified at `app/config.go:37-43`:

```go
func (c InternalConfig) Addr() string {
    host := c.Host
    if host == "" {
        host = "127.0.0.1"
    }
    return fmt.Sprintf("%s:%d", host, c.Port)
}
```

The defaulting is on the **method** — it doesn't depend on `LoadBaseConfig`. Any code path that calls `Internal.Addr()` (which is what `app/builder.go` does to feed `net.Listen`) gets `127.0.0.1` for an empty host. Verified:

```
validator("") = false
Internal.Addr() = "127.0.0.1:9090"
net.Listen bound: 127.0.0.1:9090 unspec=false
```

Validator returning `false` for `""` is correct because the listener will bind loopback, not a wildcard. The empty-string short-circuit at `app/validate.go:39-41` is correct. No regression possible from raw `BaseConfig` construction.

#### Q3: DNS performance / unreachable-resolver caveat

The user asked: "Is there a way to make the validator not do DNS at boot? If a corporate DNS server is slow / unreachable, does the validator hang at boot? Should there be a context.Context with timeout passed to ResolveTCPAddr — but Go's net.ResolveTCPAddr doesn't take a context. If we want a timeout, we need a different API."

Analysis:

1. **Numeric inputs do NOT hit DNS.** `net.ResolveTCPAddr` recognises numeric IPv4 / IPv6 / hex / octal / single-segment / dotted forms via `parseIPZone` and returns without resolver involvement. Verified by timing: `0.0.0.0`, `[::]`, `4294967296`, `0x100000000` all resolve in ~1-5µs (no I/O).
2. **Non-numeric inputs DO hit DNS** via the system resolver (`getaddrinfo` on cgo-enabled builds, pure-Go resolver otherwise). Bounded by `/etc/resolv.conf` `timeout`/`attempts` (default ~5-30s on Linux glibc, ~5s on macOS).
3. **The validator does not hang unboundedly.** When DNS errors, `ResolveTCPAddr` returns an error, the validator's `if err != nil { return false }` branch fires (line 57-59), and `Validate()` returns `nil` for the C-1 check. `Run()` then proceeds to `net.Listen(host+":port")`, which hits the same resolver code path and times out symmetrically. Total worst-case boot delay is bounded by 2× resolver timeout.
4. **`context.Context` cannot help here.** Go's `net.ResolveTCPAddr` predates context and doesn't accept one. A `(*net.Resolver).LookupHost(ctx, host)` API exists, but it's not the parser path used by `net.Listen`, so substituting it would re-introduce the validator/listener-parser-divergence the seventh-pass review just exhausted closing. The only other option is `(*net.Dialer).Resolver` — but that's also separate from the listen path.
5. **The right call is to leave it.** The default `INTERNAL_HOST` is `""` → `127.0.0.1` (numeric, no DNS). The supported opt-in path is `0.0.0.0` (numeric, no DNS). Operators using a non-numeric `INTERNAL_HOST` are unusual and willingly accept the resolver dependency that the listener itself imposes. Documenting this trade-off would be welcome polish, but it is not a security finding because:
   - the validator does not fail-open on DNS error (it resolves to "not flagged", and the listener itself fails on the same error, so no wildcard binds), and
   - the validator does not increase the resolver-failure surface beyond what the listener already incurs.

**Verdict**: not a security finding. The seventh-pass audit's classification as "QoS caveat, no code change needed" still holds. If the maintainer wants to mitigate operator confusion when DNS is partitioned, the pragmatic path is to add a comment to `LoadBaseConfig` recommending numeric `INTERNAL_HOST` for fastest boot — but that's a developer-experience polish item, not a security tightening.

#### Bracket-stripping pathologies (re-verified)

Same table as v7 audit, plus the new bracket-only short-circuit:

| Input | After strip | validator | net.Listen | Agree? |
|-------|-------------|-----------|------------|--------|
| `[::]` | `::` | true (correct) | binds `[::]:N` | yes |
| `[[::]]` | `[::]` (one strip on each side) | false (`ResolveTCPAddr` errors on `[[::]]:0`) | err `missing port in address` | yes (both reject) |
| `]::1[` | `]::1[` (no leading `[`, no trailing `]`) | false | err `too many colons` | yes (both reject) |
| `[::1]` | `::1` | false (loopback, not wildcard) | binds `[::1]:N` | yes |
| `[::1` | `::1` | false (loopback) | err `missing ']'` | yes — listener rejects, validator says non-wildcard (no security impact since no listen) |
| `::1]` | `::1` | false (loopback) | err `unexpected ']'` | yes |
| `[]` | `""` (post-strip empty) | **true (new N-10 branch)** | binds `[::]:N` | **YES — closed** |
| `[` | `""` | **true (new N-10 branch)** | err `missing ']'` | yes — over-conservative |
| `]` | `""` | **true (new N-10 branch)** | err `unexpected ']'` | yes — over-conservative |

#### Build & test verification

- `cd app && go build ./...` — clean
- `cd app && go vet ./...` — clean
- `cd app && go test ./...` — `ok github.com/bds421/rho-kit/app 1.096s`
- `cd app && go test -run TestBuilder_Validates_RejectsIPv4ZeroForms -v` — all 23 sub-cases pass, including new `[]`, `[`, `]`
- `cd grpcx && go test ./...` — clean
- `cd security/jwtutil && go test ./...` — clean
- `cd security/csrf && go test ./...` — clean
- `cd security/netutil && go test ./...` — clean
- `cd httpx && go test ./middleware/...` — all middlewares clean
- `cd httpx/middleware/csrf && go test ./...` — clean
- `cd httpx/middleware/auth && go test ./...` — clean
- `cd httpx/middleware/auditlog && go test ./...` — clean
- `cd core/tenant && go test ./...` — clean
- `cd data/cache/tenant && go test ./...` — clean
- `cd data/idempotency/tenant && go test ./...` — clean
- `cd infra/sqldb && go test ./...` — clean
- `cd infra/sqldb/pgx && go test ./...` — clean

#### Comprehensive sweep (same scope as fifth/sixth/seventh-pass)

57a8258 touches only `app/validate.go` and `app/production_defaults_test.go`. Regressions in the broader codebase are mechanically impossible because every other file is byte-identical to the seventh-pass-clean baseline. The seventh-pass clean items are still clean. Spot-checks confirmed (file:lines reflect current `main`):

- `httpx/middleware/auth/auth.go:249-302` — fail-closed on missing claim AND missing trusted-S2S marker; `RequirePermission("")` and `PermissionByMethod("","")` panic.
- `httpx/middleware/auth/auth.go:121-128` — `verifyClientCert` requires `len(VerifiedChains) > 0`.
- `httpx/middleware/csrf/csrf.go:265-279, :367-378` — Origin allowlist check precedes `SkipCheck` predicate.
- `httpx/middleware/cors/cors.go` — delegates to `jub0bs/cors`; panics on invalid config.
- `httpx/middleware/secheaders/secheaders.go:172-186` — `shouldSetHSTS` correctly gates on TLS / forced / trusted-proxy paths.
- `httpx/middleware/clientip/clientip.go` — defaults to loopback-only trusted proxies.
- `httpx/middleware/auditlog/auditlog.go:78-122` — `WithTrustedProxies` plumbed; deferred audit + panic-recording.
- `httpx/middleware/budget/budget.go` — default scope `"tenant"`; backend errors → 503; tenant required.
- `httpx/middleware/idempotency/idempotency.go:222-224, :243-255` — construction panic on missing extractor; empty userID → 400.
- `httpx/middleware/tenant/tenant.go` — `WithRequiredOnSafeMethods` opt-in.
- `httpx/middleware/timeout/timeout.go` — WebSocket bypass requires explicit opt-in.
- `httpx/middleware/signedrequest/{signedrequest,redis/redis}.go` — verify ordering preserved; `nonceStore == nil` panics; ttl<=0 panics; failure → 500.
- `httpx/middleware/recover/recover.go` — `http.ErrAbortHandler` re-raised.
- `httpx/middleware/maxbody/maxbody.go` — caps body via `http.MaxBytesReader`.
- `httpx/middleware/ratelimit/{ratelimit,keyed,tenant}.go` — sharded fixed-window; tenant fail-closed.
- `httpx/middleware/approval/approval.go` — body capped at 64 KiB; tenant required.
- `httpx/healthhttp/handler.go` — `Cache-Control: no-store` on /metrics, /health, /ready.
- `httpx/sign/sign.go:138-147` — `defaultNonce` panics on `crypto/rand` error.
- `httpx/mcp/mcp.go:321-327, :174-216, :255-279` — default actor extractor anonymous.
- `httpx/mcp/server.go:202-218, :300-338` — `auditPrecheck` refuses dispatch in strict mode.
- `grpcx/server.go:191-198` — recovery interceptors prepended.
- `grpcx/interceptor/auth.go:212-303, :305-372, :415-439` — RequirePermission/RequireScope panic on empty args; mTLS path requires verified chains.
- `grpcx/interceptor/auth.go:443-449` — x-user-id metadata read after CN allowlist check.
- `grpcx/interceptor/recovery.go` — defer-recover before handler.
- `grpcx/interceptor/deadline.go` — only tightens deadlines.
- `grpcx/interceptor/logging.go:107-118` — `isValidID` rejects non-printable ASCII.
- `grpcx/interceptor/metrics.go` — bounded label cardinality.
- `security/jwtutil/jwtutil.go:87-151` — Verify validates issuer + audience; subject must be non-empty; permissions/scopes warn-on-malformed.
- `security/jwtutil/jwtutil.go:329-342` — `defaultHTTPClient` caps response headers/body.
- `security/jwtutil/jwtutil.go:344-365` — `KeySet()` returns nil when stale beyond `maxStale`.
- `security/csrf/*` — `Issuer` mints + verifies session-bound tokens with HMAC; constant-time compare.
- `security/netutil/tls.go:46-100` — `ServerTLS` requires explicit `WithRequireClientCert` for mTLS.
- `security/netutil/ssrf.go` — outbound dialer drops connections to private IPv4/IPv6 ranges.
- `core/secret/secret.go` — value-receiver redaction.
- `core/tenant/tenant.go` — forbidden-byte set rejects `:` `/` `\x00`.
- `data/cache/tenant/tenant.go`, `data/idempotency/tenant/tenant.go` — length-prefix scoping.
- `app/builder.go` — `Run()` calls `Validate()` before infra spins up; opt-outs `WithInternalNonLoopback`, `WithoutTLS`, `WithoutJWTAudience` typed booleans.
- `app/validate.go:73-122` — `Validate()` returns on first error; production-safety subset runs at end.
- `app/jwt_module.go:55-71` — switch is exhaustive.
- `infra/redis/config.go:122-139` — `ValidateRedis` requires REDIS_PASSWORD when not using URL.
- `infra/sqldb/config.go:299-374` — environment parameter no longer consulted; `validatePostgresSSLMode` rejects loose modes.
- `infra/sqldb/pgx/pgx.go:97-116, :118-121, :296-302, :325-336` — Fallbacks loop, TLS check, loopback gate; all hold.
- `infra/storage/{s3backend,azurebackend,sftpbackend}/config.go` — environment parameter no longer consulted; weak-credential check on actual key.
- `infra/messaging/buffered_publisher.go:117, :162-164` — panic on `stateFile == ""` without `WithEphemeralBuffer`.
- `infra/messaging/amqpbackend/debughttp/guard.go:33` — debug endpoints OFF in non-dev.
- `infra/messaging/amqpbackend/config.go:129-148` — extracts password before `RejectWeakCredential`.
- `examples/agentic-service/internal/app/app.go` — package doc warns against production use.

#### Cross-cutting checks

- `git diff 5460a2e..57a8258 --name-only` shows 57a8258 touches only `app/validate.go`, `app/production_defaults_test.go`, and `docs/audit/v2_SECURITY_REVIEW_7.md`. Mechanical confirmation that nothing else changed.
- `git diff 5460a2e..57a8258 -- app/validate.go` shows the change is purely additive: 10 lines (8 comment + 2 logic) inserted between `stripped := …` and `addr, err := …`. No existing logic is modified.
- The new branch is reachable on exactly three inputs (`[]`, `[`, `]`) per case enumeration above. All three are correctly handled.
- `TestBuilder_Validates_RejectsIPv4ZeroForms` now has 23 sub-cases (was 20 in v7); the three additions (`[]`, `[`, `]`) are pinned and pass.
- The four-iteration N-class progression (N-1 string-equality → N-7 hex/octal predicate → N-9 single-segment overflow → N-10 bracket-only post-strip-empty) is now exhausted: every reachable input that `net.Listen` accepts as a wildcard is now also flagged by the validator, and every input the validator over-flags is one `net.Listen` rejects too — so the operator never sees a difference.

---

## Bottom line for tagging v2.0.0

Clean. Zero new findings. Every prior finding still closes. The 57a8258 fix lands the seventh-pass review's recommended Option B verbatim, pins it with three sub-tests, and the empirical bracket-only case enumeration confirms the validator and `net.Listen` are now in lock-step on every reachable input. The C-1 surface — unauthenticated `/metrics` exposed on every interface — is genuinely sealed across all wildcard-bind classes (canonical IPv4/IPv6, leading-zero, hex, octal, single-segment overflow, IPv4-mapped, bracketed forms, bracket-only forms).

The user can tag **v2.0.0**.
