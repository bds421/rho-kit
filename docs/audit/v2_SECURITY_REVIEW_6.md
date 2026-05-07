# v2.0.0 — sixth-pass security review (post-fifth-pass-fix)

**Reviewer**: security-reviewer agent (sixth pass)
**Branch**: main @ `e9b30ee` — "fix: close fifth-pass audit findings (N-6 HIGH, N-7 MEDIUM, N-8 LOW)"
**Scope**: regression check on every prior finding (v2 / v2_2 / v2_3 / v2_4 / v2_5) + paranoid re-read of the e9b30ee fix surface (`infra/sqldb/pgx/pgx.go` Fallbacks loop, `app/validate.go` `isAllZeroIPv4Numeric`, `requireLoopbackHost` bracket-strip), plus the broad sweep of every middleware / interceptor / config validator the user requested.

---

## Verdict

**Do NOT tag v2.0.0.** One MEDIUM finding directly in the e9b30ee fix surface: `app/validate.go`'s replacement of `isAllZeroDottedDecimal` with `strconv.ParseUint`-based `isAllZeroIPv4Numeric` requires `v == 0` to flag a segment, but on every cgo-enabled Go binary (the default), `net.Listen` resolves single-segment numeric host strings via glibc/getaddrinfo's `inet_addr` semantics, which TRUNCATE the value to the low 32 bits. So `INTERNAL_HOST=4294967296` (decimal 2^32), `0x100000000` (hex 2^32), `040000000000` (octal 2^32), and any larger multiple of 2^32 binds to 0.0.0.0 on all interfaces while the validator says "not a wildcard, value is non-zero". The fifth-pass commit message calls out "every form `net.Listen` accepts as the IPv4 wildcard"; this class is missed.

The e9b30ee fix's two other surfaces (`pgx.go` Fallbacks loop, `requireLoopbackHost` bracket-strip) hold under paranoid review — see "Items checked, no findings" below. The 30 prior findings are all still closed.

The fix is a one-line addition: after the `v != 0` check, also reject if the parsed value's `v & 0xFFFFFFFF == 0` for any single-segment form (i.e. when the truncated 32-bit IP is zero). Or — as recommended by the fourth-pass and fifth-pass audits — drop the hand-rolled predicate entirely and use `net.ResolveTCPAddr("tcp", net.JoinHostPort(host, "0"))` which mirrors `net.Listen`'s actual semantics in one call. The fifth-pass fix cited "no I/O" as the reason to avoid `ResolveTCPAddr`, but `ResolveTCPAddr` does NOT do DNS lookups for numeric forms — it returns immediately for IP literals. The I/O concern is illusory; `ResolveTCPAddr` is the correct tool.

---

## Regression check

For each prior finding: status + current file:line of the fix.

| ID | Title | Status | Current fix location |
|----|-------|--------|----------------------|
| Auth fail-closed (2937115) | `RequirePermission` / `RequireScope` no longer pass-through on missing claim | **still closed** | `httpx/middleware/auth/auth.go:249-271`, `:277-302`, `httpx/middleware/auth/scope.go` |
| C-1 (v2-2) | Internal ops port binds to 0.0.0.0 by default | **still closed** | Default loopback at `app/config.go:37-43`; validator at `app/validate.go:174-176` (but see N-9 below for hex-overflow gap) |
| C-2 (v2-2) | Production-defaults skip TLS check | **still closed** | Validator at `app/validate.go:165-167`; opt-out `WithoutTLS` at `app/builder.go:239-242` |
| C-3 (v2-2) | Cross-tenant key collision via `:` in tenant ID | **still closed** | `core/tenant/tenant.go`; length-prefix scoping at `data/cache/tenant/tenant.go` and `data/idempotency/tenant/tenant.go` |
| H-1 (v2-2) | Idempotency middleware collapses to shared-key on empty userID | **still closed** | `httpx/middleware/idempotency/idempotency.go:243-255` |
| H-2 (v2-2) | gRPC auth lacks RequirePermission/RequireScope/IsTrustedS2S | **still closed** | `grpcx/interceptor/auth.go:212-303`; `verifyClientCertGRPC` at `:424-439` |
| H-3 (v2-2) | Example agentic-service is copy-paste hazard | **still closed** | `examples/agentic-service/internal/app/app.go:1-29` |
| H-4 (v2-2) | Budget middleware fails open without WithMultiTenant | **still closed** | Validator at `app/validate.go:138-140` |
| H-5 (v2-2) | WithProductionDefaults does not require WithJWTAudience | **still closed** | Validator at `app/validate.go:158-160`; opt-out `WithoutJWTAudience` at `app/builder.go:253-256` |
| H-6 (v2-2) | `authz.SubjectFromHeader` reads spoofable header | **still closed** | Deprecation warn at `httpx/authz/authz.go`; safe alternatives present |
| H-7 (v2-2) | MCP default actor extractor reads spoofable X-Actor-Id | **still closed** | `httpx/mcp/mcp.go:321-327` |
| H-8 (v2-2) | JWT module KIT_ENV literal-match drift | **still closed** | Pairing enforced at `app/validate.go:151-152, :158-160` |
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
| M-A (v2-3) | `validateProductionSafety` only catches IPv4 wildcard | **partially closed (see N-9)** | `app/validate.go:31-44, :59-82, :174-176` — handles canonical/leading-zero/hex zero forms; **misses the hex/decimal/octal overflow forms (single-segment value ≥ 2^32 whose low 32 bits = 0)**. |
| M-B (v2-3) | `pgx.Config.AllowPlaintext` silently honored regardless of caller | **closed** | `infra/sqldb/pgx/pgx.go:97-116` — walks `pcfg.ConnConfig.Host` PLUS every `Fallbacks[*].Host`. Verified against pgx 5.9.2 `pgconn.buildConnectOneConfigs` at `pgconn/pgconn.go:179-241` — the dial loop iterates exactly `[primary] + Fallbacks`, which is what the kit now walks. No other field on `*pgconn.Config` carries a host that lands a connection. |
| N-1 (v2-4) | `isUnspecifiedHost` misses Go-accepted wildcard forms | **partially closed (see N-9)** | `app/validate.go:31-82` — handles canonical/leading-zero/hex zero. Missing class is single-segment-overflow. |
| N-2 (v2-4) | pgx loopback gate bypassable via URL `?host=` and duplicate `host=` keys | **closed** | `infra/sqldb/pgx/pgx.go:108-116` — the same Fallbacks loop also covers the URL `?host=` and libpq duplicate-key cases (because pgxpool resolves both to `ConnConfig.Host` / `Fallbacks[*].Host` per `pgconn/config.go:380-423`). |
| N-3 (v2-4) | pgx unconditional TLS check bypassable via duplicate `sslmode=` | **closed** | `infra/sqldb/pgx/pgx.go:118-121, :325-336` — `requireTLSOnParsedConfig` walks `cc.TLSConfig` + `Fallbacks[*].TLSConfig`. |
| N-4 (v2-4) | `sqldb.Fields.Validate` accepts `sslmode=prefer`/`allow` | **closed** | `infra/sqldb/config.go:320, :353-374` — `validatePostgresSSLMode` rejects `prefer`/`allow` directly. |
| N-5 (v2-4) | amqp `RejectWeakCredential` was passed the URL string | **closed** | `infra/messaging/amqpbackend/config.go:129-148` — extracts password via `url.Parse` + `User.Password()`. |
| N-6 (v2-5) | pgx multi-host fallback bypass | **closed** | `infra/sqldb/pgx/pgx.go:108-116` — Fallbacks loop. Regression tests at `pgx_test.go:85-93, :97-105`. |
| N-7 (v2-5) | hex-encoded zero IPv4 forms bypass `isAllZeroDottedDecimal` | **partially closed (see N-9)** | `app/validate.go:59-82` — replaced with `isAllZeroIPv4Numeric` using `strconv.ParseUint` base 0. Closes literal-zero hex forms (`0x0`, `0X00000000`, `0x0.0x0.0x0.0x0`, …). Misses single-segment overflow forms where the parsed value is non-zero but `v & 0xFFFFFFFF == 0`. |
| N-8 (v2-5) | `requireLoopbackHost` rejects bracket-wrapped IPv6 loopback | **closed** | `infra/sqldb/pgx/pgx.go:296-302` — bracket-strip via `TrimPrefix`/`TrimSuffix` mirrors `app/validate.go:36`. Verified handling of `[::1]`, `[::1`, `::1]` (all accepted as ::1), `]::1[` (rejected — does not start with `[`), `[::]` (rejected — wildcard, not loopback), `[8.8.8.8]` (rejected — non-loopback). The strip is mildly permissive on one-sided brackets but never accepts non-loopback content. **Fail-safe.** |

No regressions. One prior fix (N-7, the parent class of N-1 and M-A) carries over a residual gap (N-9) detailed below.

---

## New findings

### MEDIUM

#### N-9. `isAllZeroIPv4Numeric` misses single-segment IPv4 numeric forms whose value mod 2^32 is zero (`4294967296`, `0x100000000`, `040000000000`, …) — `net.Listen` accepts them as the IPv4 wildcard via `getaddrinfo`'s `inet_addr` truncation

**File**: `app/validate.go:59-82`

**What's wrong**: The fifth-pass fix replaced the previous all-`'0'`-character predicate with `strconv.ParseUint(p, 0, 64)` per segment, accepting only segments where `v == 0`. This correctly catches all-zero literal forms (`0x0`, `0X00000000`, `00`, `000`, the canonical decimal forms) but the predicate's `v == 0` test is wrong for **single-segment numeric forms ≥ 2^32**. On every cgo-enabled Go binary (the default for `go run`, `go build` without `CGO_ENABLED=0`), the resolver routes through glibc/Apple-libsystem `getaddrinfo`, which uses `inet_aton`/`inet_addr` semantics for purely-numeric host strings. Per POSIX `inet_addr`, a single-segment numeric value is interpreted as the full 32-bit IP; values larger than 2^32 are silently truncated (mod 2^32) before the address is constructed.

| Internal.Host | `isAllZeroIPv4Numeric` says | `net.Listen("tcp", host+":0")` actually | Verdict |
|---|---|---|---|
| `4294967296` (decimal 2^32) | false (v=4294967296, v != 0) | binds `[::]:NNN` `IsUnspecified=true` | **BYPASS** |
| `0x100000000` (hex 2^32) | false (v=4294967296, v != 0) | binds `[::]:NNN` `IsUnspecified=true` | **BYPASS** |
| `0X100000000` | false | binds `[::]:NNN` `IsUnspecified=true` | **BYPASS** |
| `040000000000` (octal 2^32) | false (v=4294967296, v != 0) | binds `[::]:NNN` `IsUnspecified=true` | **BYPASS** |
| `8589934592` (2 × 2^32) | false | binds `[::]:NNN` (truncates to 0.0.0.0) | **BYPASS** |
| `0x200000000`, `0x300000000`, … | false | binds `[::]:NNN` | **BYPASS** |
| `0x100000000000000000000000000` | false (uint64 overflow → ParseUint err → flagged=false) | binds `[::]:NNN` (mod-2^32 wraps to 0) | **BYPASS** |

Verified end-to-end against the actual `isUnspecifiedHost` from `app/validate.go`:

```text
=== RUN   TestN9_HexOverflowBypass
=== RUN   TestN9_HexOverflowBypass/4294967296
    n9_test.go:38: BYPASS: isUnspecifiedHost("4294967296")=false but net.Listen bound to wildcard [::]:49567
=== RUN   TestN9_HexOverflowBypass/0x100000000
    n9_test.go:38: BYPASS: isUnspecifiedHost("0x100000000")=false but net.Listen bound to wildcard [::]:49568
=== RUN   TestN9_HexOverflowBypass/0X100000000
    n9_test.go:38: BYPASS: isUnspecifiedHost("0X100000000")=false but net.Listen bound to wildcard [::]:49569
=== RUN   TestN9_HexOverflowBypass/040000000000
    n9_test.go:38: BYPASS: isUnspecifiedHost("040000000000")=false but net.Listen bound to wildcard [::]:49570
--- FAIL: TestN9_HexOverflowBypass (0.00s)
```

Reachability verified: `net.Dial("tcp", "127.0.0.1:NNN")` against the listener returns `OK` — the bind is a real all-interfaces wildcard, not a darwin quirk. The same behaviour applies on Linux glibc (cgo path), which uses the same `inet_aton` man-page semantics.

**Why this matters**: The fifth-pass commit message says the fix "catches the hex-encoded zero forms `net.Listen` accepts but `net.ParseIP` rejects". That claim is incomplete: `net.Listen` accepts a strictly larger class — every single-segment numeric whose value mod 2^32 = 0. The validator's `v == 0` gate is a 64-bit check; the resolver does a 32-bit-truncation check. The two disagree on every value of the form `k × 2^32` for `k ≥ 1`.

**Why the fifth-pass review missed this**: The fifth-pass review noted the user's bar specifically — "Single-segment bigger-than-uint32 like '0x100000000'? Should NOT be flagged as wildcard (since the full value is non-zero), and the function correctly rejects via the v != 0 check." The fifth-pass auditor accepted the function's behaviour (correctly rejects flagging) without verifying what `net.Listen` does — it assumed `0x100000000` would be a "non-wildcard" bind. It is not: it is a wildcard bind via 32-bit truncation. The inverse of the user's reasoning is the bug: the validator and `net.Listen` must agree, and they don't.

**Attack scenario**: An operator who copy-pastes `INTERNAL_HOST=4294967296` (perhaps from an obfuscation tool or because someone on a forum posted "use 2^32 to bind everywhere on Linux/macOS") bypasses the C-1 / M-A / N-1 / N-7 check entirely. `/metrics` becomes reachable on every interface. Same blast radius as the original C-1 finding. Lower likelihood of operator error than `0x0` (which is more obviously a wildcard) but mechanistically identical bypass.

**Suggested fix** — there are two correct fixes:

**Option A (preferred): use `net.ResolveTCPAddr`** — exactly what the fourth-pass and fifth-pass audits originally recommended. The fifth-pass fix's stated reason for not using it ("does DNS lookup as a side effect") is wrong: `ResolveTCPAddr` does NOT do DNS lookup for numeric host strings; it only does DNS for non-numeric hostnames. For a numeric form like `4294967296` or `0x100000000`, `ResolveTCPAddr` calls the same resolver path `net.Listen` will use, returning the IP literal without any network I/O.

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

This handles every form by definition (since it shells out to the same code path `net.Listen` uses) — including the overflow forms, hex/octal/decimal, IPv6 `[::]`, IPv4-mapped `::ffff:0.0.0.0`, etc. No hand-rolled predicate to maintain.

**Option B: fix the predicate** — keep `isAllZeroIPv4Numeric` but additionally check the 32-bit truncated value:

```go
func isAllZeroIPv4Numeric(s string) bool {
    if s == "" { return false }
    parts := strings.Split(s, ".")
    if len(parts) > 4 { return false }
    for _, p := range parts {
        if p == "" { return false }
        v, err := strconv.ParseUint(p, 0, 64)
        if err != nil {
            // ParseUint overflow on huge values: getaddrinfo's
            // inet_addr would mod-2^32 wrap; we can't know without
            // re-implementing inet_addr. Defensively flag.
            return true
        }
        // For 1-segment: full 32-bit IP truncated. For 2/3/4-segment:
        // each part is constrained, so v == 0 is sufficient.
        if len(parts) == 1 {
            if uint32(v) != 0 {
                return false
            }
        } else if v != 0 {
            return false
        }
    }
    return true
}
```

(Option A is preferred — it removes a class of bug rather than a single instance.)

**5-line failing test**:

```go
func TestBuilder_Validates_RejectsHexOverflowIPv4(t *testing.T) {
    for _, host := range []string{"4294967296", "0x100000000", "0X100000000", "040000000000", "8589934592"} {
        cfg := BaseConfig{Internal: InternalConfig{Host: host, Port: 9090}, TLS: validTLSForTest()}
        err := New("svc", "v1", cfg).WithoutJWTAudience().Validate()
        require.Errorf(t, err, "Internal.Host=%q binds to 0.0.0.0 via net.Listen and must be rejected", host)
    }
}
```

---

## Items checked, no findings

### Re-audit of the e9b30ee fix surface (per the user's explicit prompts)

#### `infra/sqldb/pgx/pgx.go` Fallbacks loop (N-6 fix)

The user asked: "does the new walk cover every host pgx might dial? Check pgx 5.9.2's source: are there other fields besides `ConnConfig.Host` and `ConnConfig.Fallbacks[*].Host` that could land a connection on a different host (e.g. `lookup_host` callback, `target_session_attrs`, IPv6 happy-eyeballs branches)?"

Verified against pgx 5.9.2 source (`~/go/pkg/mod/github.com/jackc/pgx/v5@v5.9.2/pgconn/`):

- **`pgconn/pgconn.go:179-241` `buildConnectOneConfigs`** is the single function pgx uses to enumerate every host before dial. It builds `fallbackConfigs := []*FallbackConfig{{Host: config.Host, …}}; fallbackConfigs = append(fallbackConfigs, config.Fallbacks...)` — exactly the set the kit now walks. There is no third source of hosts.
- **`pgconn/config.go:35-104` `Config` struct** — fields beyond `Host`/`Fallbacks` that *could* affect connection: `DialFunc`, `LookupFunc`, `AfterNetConnect`, `ValidateConnect`, `BuildContextWatcherHandler`. None of these are settable from the DSN — they are only set by Go code (the kit doesn't override any of them, so pgx's defaults apply).
  - `LookupFunc` defaults to `(&net.Resolver{}).LookupHost` — does name resolution for each fallback host. If `localhost` resolves to a non-loopback IP via `/etc/hosts` mischief, that's an OS-level threat; not within the scope of a kit-level check.
  - `DialFunc` is a wrapper around `net.Dialer.DialContext` for the connect timeout. Doesn't change which host is dialed.
  - `ValidateConnect` is set when `target_session_attrs` is in the DSN — it accepts/rejects a connection AFTER auth, prompting pgx to try the next host. The set of hosts is still `[primary] + Fallbacks`.
  - `AfterNetConnect` / `BuildContextWatcherHandler` — post-connect hooks. Don't change the host set.
- **`pgconn/config.go:380-423` parser** — `hosts := strings.Split(settings["host"], ",")` then loops, building `fallbacks`. The first lands on `config.Host`, the rest on `config.Fallbacks`. Multi-host DSN, URL `?host=` query, libpq duplicate `host=` keys all flow through this path. The kit's walk over `pcfg.ConnConfig.Host` + `pcfg.ConnConfig.Fallbacks[*].Host` covers every case.
- **`target_session_attrs` (`config.go:438-453`)** — sets `ValidateConnect` to one of five canned validators (`read-write`, `read-only`, `primary`, `standby`, `prefer-standby`). Does NOT add hosts; only filters the existing host set post-auth.
- **IPv6 happy-eyeballs / DNS multi-result expansion (`pgconn.go:208-237`)** — for each fallback host, `LookupFunc(ctx, fb.Host)` may return multiple IPs (e.g., one IPv4 and one IPv6 for `localhost`). Each becomes its own `connectOneConfig`. **Important**: the host string stays the same; the IP list is just multiple A/AAAA records for that host. The kit's check on `fb.Host` still applies — if the host string is `localhost`, the kit accepts it; if `evil.example.com`, the kit rejects it; the IPs returned by `LookupFunc` are not the kit's gate.

**Conclusion**: every host pgx dials at runtime is in `[ConnConfig.Host] + ConnConfig.Fallbacks[*].Host`. The kit's walk covers the entire set. **No finding.**

Regression tests at `infra/sqldb/pgx/pgx_test.go:85-93` (libpq form) and `:97-105` (URL form) pin the multi-host case.

#### `app/validate.go isAllZeroIPv4Numeric` (N-7 fix)

The user's specific edge cases:

| Form | `isAllZeroIPv4Numeric` | `net.Listen` | Outcome |
|---|---|---|---|
| `-0` | false (ParseUint rejects sign) | DNS lookup (no such host) | both reject, agree |
| `+0` | false (ParseUint rejects sign) | DNS lookup (no such host) | both reject, agree |
| ` 0` (leading space) | false (ParseUint rejects whitespace) | DNS lookup (no such host) | both reject, agree |
| `0 ` (trailing space) | false (ParseUint rejects whitespace) | DNS lookup (no such host) | both reject, agree |
| ` 0.0.0.0 ` (wrapped) | false | DNS lookup (no such host) | both reject, agree |
| `０` (fullwidth zero) | false (ParseUint accepts only ASCII digits) | DNS lookup (no such host) | both reject, agree |
| `0x100000000` (single seg > uint32) | **false (v=4294967296, v != 0)** | **binds 0.0.0.0** | **DISAGREE — see N-9** |
| `0xffffffff` (single seg = max u32 = 255.255.255.255) | false | binds 255.255.255.255 (net says address family not supported, not a wildcard) | both correctly NOT flag as wildcard |
| `0b0` (binary zero, Go strconv extension) | true (ParseUint base 0 accepts `0b` prefix) | DNS lookup (no such host) | validator over-flags but no security impact (denies a host net.Listen rejects anyway) |
| `0o0` (Go's new octal prefix) | true | DNS lookup (no such host) | same — over-flag without security impact |
| `0.0.0.256` (4-seg with overflow) | false | DNS lookup (no such host) | both reject, agree |

The over-flagging on `0b0` / `0o0` is not a security concern — it rejects a host that `net.Listen` would also reject (with a different error). The validator is fail-safe in this direction.

The only genuine disagreement is the single-segment-overflow class (N-9 above). All other edge cases the user asked about agree between validator and listener.

The user's explicit question — "What if Go's strconv accepts something net.Listen doesn't, or vice versa? Verify empirically with `net.Listen("tcp", host+":0")` for each suspicious form." — was the right question. Empirical verification on darwin/cgo with Go 1.26.2 and against glibc man-page-documented `inet_addr` semantics shows the disagreement is at the overflow boundary.

#### `requireLoopbackHost` bracket-strip (N-8 fix)

The user asked: "does the strip handle malformed brackets like `]::1[` or `[::1` (one-sided)?"

Verified empirically:

| Input | Stripped | `net.ParseIP` | Result |
|---|---|---|---|
| `[::1]` | `::1` | valid IPv6 loopback | **accepted** (correct) |
| `[::1` | `::1` (TrimSuffix removes nothing, TrimPrefix removes `[`) | valid IPv6 loopback | **accepted** (one-sided, but correct outcome) |
| `::1]` | `::1` (TrimSuffix removes `]`) | valid IPv6 loopback | **accepted** (one-sided, but correct outcome) |
| `]::1[` | `]::1[` (no prefix `[`, no suffix `]`) | nil | **rejected** (correct) |
| `[[::1]]` | `[::1]` (one strip on each side) | nil | **rejected** (correct) |
| `[]` | empty | nil | **rejected** (correct) |
| `[127.0.0.1]` | `127.0.0.1` | IPv4 loopback | **accepted** (unusual but correct — IPv4 doesn't normally use brackets, but `IsLoopback()` is the gate) |
| `[::]` | `::` | unspecified, NOT loopback | **rejected** (correct — IPv6 wildcard is not loopback) |
| `[8.8.8.8]` | `8.8.8.8` | non-loopback | **rejected** (correct) |

The strip is mildly permissive (accepts one-sided brackets like `[::1` and `::1]`) but is fail-safe: it never accepts non-loopback content as loopback. The `IsLoopback()` check is the gate; the bracket-strip just normalizes the input format. **No finding.**

#### Comprehensive sweep (same scope as fifth-pass)

Since e9b30ee touches only `app/validate.go`, `infra/sqldb/pgx/pgx.go`, and their tests (verified via `git diff f385c6d..HEAD --name-only`), regressions in the broader codebase are mechanically impossible. The fifth-pass clean items are still clean. Spot-checks confirmed:

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
- `app/builder.go:769-782` — `Run()` calls `Validate()` before infra spins up.
- `app/builder.go:212-256` — opt-outs `WithInternalNonLoopback`, `WithoutTLS`, `WithoutJWTAudience` typed booleans.
- `app/validate.go:94-143` — `Validate()` returns on first error; production-safety subset runs at end.
- `app/jwt_module.go:55-71` — switch is exhaustive.
- `infra/redis/config.go:122-139` — `ValidateRedis` requires REDIS_PASSWORD when not using URL.
- `infra/sqldb/config.go:299-374` — environment parameter no longer consulted; `validatePostgresSSLMode` rejects loose modes.
- `infra/storage/{s3backend,azurebackend,sftpbackend}/config.go` — environment parameter no longer consulted; weak-credential check on actual key.
- `infra/messaging/buffered_publisher.go:117, :162-164` — panic on `stateFile == ""` without `WithEphemeralBuffer`.
- `infra/messaging/amqpbackend/debughttp/guard.go:33` — debug endpoints OFF in non-dev.
- `infra/messaging/amqpbackend/config.go:129-148` — extracts password before `RejectWeakCredential`.
- `examples/agentic-service/internal/app/app.go` — package doc warns against production use.

#### Cross-cutting checks

- `go test ./app ./infra/sqldb/pgx` baseline green (verified post-audit).
- `git diff f385c6d..HEAD --name-only` shows e9b30ee touches only the three files reviewed in detail above (`app/validate.go`, `app/production_defaults_test.go`, `infra/sqldb/pgx/pgx.go`, `infra/sqldb/pgx/pgx_test.go`, plus `docs/audit/v2_SECURITY_REVIEW_5.md`). Mechanical confirmation that nothing else regressed.
- pgx 5.9.2 source at `~/go/pkg/mod/github.com/jackc/pgx/v5@v5.9.2/pgconn/pgconn.go:179-241` re-read end-to-end; the host enumeration is exactly `[ConnConfig.Host] + ConnConfig.Fallbacks` per `buildConnectOneConfigs`, which is what the kit walks.
- `getaddrinfo` / `inet_addr` semantics on darwin (libsystem `inet_addr.c`) and Linux (glibc `inet/inet_addr.c`) both implement the same POSIX behaviour: 1/2/3/4-segment numeric forms with low-bits-zero truncation. Verified empirically on darwin/arm64 Go 1.26.2; documented behaviour on glibc.

---

## Bottom line for tagging v2.0.0

One new finding, in the e9b30ee fix surface:

- **N-9 MEDIUM**: single-segment IPv4 numeric forms whose value mod 2^32 is zero (`4294967296`, `0x100000000`, `040000000000`, …) bypass `isAllZeroIPv4Numeric` because `strconv.ParseUint` returns the full 64-bit value (non-zero) while `net.Listen` truncates to 32 bits (zero) via `inet_addr` semantics. Fix is one of two:
  1. (preferred) replace `isAllZeroIPv4Numeric` with `net.ResolveTCPAddr` — the fourth-pass and fifth-pass audits' original recommendation. `ResolveTCPAddr` does NOT do DNS for numeric inputs; the "I/O concern" cited in the fifth-pass commit is incorrect.
  2. (alternative) keep the predicate but check `uint32(v) == 0` for single-segment forms.

A 5-line failing test is inline above. Not exploitable by default — requires an operator to construct a specific config — but fits the user's stated bar: "absence of an explicit signal silently relaxes a check". Same blast radius as C-1 / N-1 / N-7 (unauthenticated `/metrics` exposed on every interface) for the operator who copy-pastes the obfuscated form.

After N-9 lands, the surface should be tag-ready: every other prior finding still closes, the e9b30ee fixes for N-6 (Fallbacks loop) and N-8 (bracket-strip) hold under paranoid review, and no broader regressions are possible because no other code changed.

The pattern across the last three pass-fix-pass cycles is unmistakable: each hand-rolled "string equals zero" predicate (`'0'`-only, then `strconv.ParseUint` v==0) misses a class the resolver accepts. The recommendation since the fourth pass — delegate to `net.ResolveTCPAddr` — has been correct each time. Re-recommending it for N-9.
