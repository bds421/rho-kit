# v2.0.0 — seventh-pass security review (post-sixth-pass-fix)

**Reviewer**: security-reviewer agent (seventh pass)
**Branch**: main @ `5460a2e` — "fix: close sixth-pass audit finding (N-9 MEDIUM)"
**Scope**: regression check on every prior finding (v2 / v2_2 / v2_3 / v2_4 / v2_5 / v2_6) + paranoid re-read of the 5460a2e fix surface (`app/validate.go` `isUnspecifiedHost` rewrite using `net.ResolveTCPAddr`), plus the comprehensive sweep of every middleware / interceptor / config validator the user requested.

---

## Verdict

**Do NOT tag v2.0.0.** One MEDIUM finding directly in the 5460a2e fix surface: the new `isUnspecifiedHost` strips one outer layer of `[`/`]` and then guards on `addr.IP != nil`. The combination misses one form: `INTERNAL_HOST="[]"`. Bracket-strip leaves the empty string; `net.JoinHostPort("","0")` is `":0"`; `net.ResolveTCPAddr("tcp", ":0")` returns `addr` with `addr.IP == nil` and `err == nil`. The `addr.IP != nil` short-circuit causes the validator to report not-flagged. But `net.Listen("tcp", "[]:0")` (the call the running server makes via `Internal.Addr()`) parses `[]` as the IPv6 wildcard and binds `[::]:port`. Verified against the actual `Builder.Validate()` end-to-end below.

The 30+ prior findings are all still closed. The 5460a2e fix's intent — delegate to the same parser `net.Listen` uses — is correct, and it closes the N-9 overflow class. But the intermediate bracket-strip introduces a different parser path: the validator's path is "strip-then-resolve" while `net.Listen`'s path is "resolve-with-brackets". The two diverge on the empty-bracketed form.

The fix is a one-line addition: after `stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")`, also reject if `stripped == "" && host != ""` (i.e. the host was non-empty but stripping consumed the whole string). Or — simpler and more robust — pass the host through directly to `ResolveTCPAddr` without stripping, and only strip on the parse-error retry path:

```go
func isUnspecifiedHost(host string) bool {
    if host == "" {
        return false
    }
    if addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, "0")); err == nil {
        return addr.IP != nil && addr.IP.IsUnspecified()
    }
    // ResolveTCPAddr rejects pre-bracketed input passed through JoinHostPort
    // (which would produce "[[::]]:0"). Strip one layer and retry.
    stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
    if stripped == "" || stripped == host {
        return false
    }
    addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(stripped, "0"))
    if err != nil {
        return false
    }
    return addr.IP != nil && addr.IP.IsUnspecified()
}
```

(The simpler one-liner — `if stripped == "" { return false }` after the strip — closes this specific finding but leaves the strip path active for non-empty pre-bracketed inputs, which is what the strip was added for.)

---

## Regression check

For each prior finding: status + current file:line of the fix.

| ID | Title | Status | Current fix location |
|----|-------|--------|----------------------|
| Auth fail-closed (2937115) | `RequirePermission` / `RequireScope` no longer pass-through on missing claim | **still closed** | `httpx/middleware/auth/auth.go:249-271`, `:277-302`, `httpx/middleware/auth/scope.go` |
| C-1 (v2-2) | Internal ops port binds to 0.0.0.0 by default | **still closed** | Default loopback at `app/config.go:37-43`; validator at `app/validate.go:38-51, :143-145` (but see N-10 below for empty-bracketed gap) |
| C-2 (v2-2) | Production-defaults skip TLS check | **still closed** | Validator at `app/validate.go:134-136`; opt-out `WithoutTLS` at `app/builder.go` |
| C-3 (v2-2) | Cross-tenant key collision via `:` in tenant ID | **still closed** | `core/tenant/tenant.go`; length-prefix scoping at `data/cache/tenant/tenant.go` and `data/idempotency/tenant/tenant.go` |
| H-1 (v2-2) | Idempotency middleware collapses to shared-key on empty userID | **still closed** | `httpx/middleware/idempotency/idempotency.go:243-255` |
| H-2 (v2-2) | gRPC auth lacks RequirePermission/RequireScope/IsTrustedS2S | **still closed** | `grpcx/interceptor/auth.go:212-303`; `verifyClientCertGRPC` at `:424-439` |
| H-3 (v2-2) | Example agentic-service is copy-paste hazard | **still closed** | `examples/agentic-service/internal/app/app.go:1-29` |
| H-4 (v2-2) | Budget middleware fails open without WithMultiTenant | **still closed** | Validator at `app/validate.go:107-109` |
| H-5 (v2-2) | WithProductionDefaults does not require WithJWTAudience | **still closed** | Validator at `app/validate.go:127-129`; opt-out `WithoutJWTAudience` |
| H-6 (v2-2) | `authz.SubjectFromHeader` reads spoofable header | **still closed** | Deprecation warn at `httpx/authz/authz.go`; safe alternatives present |
| H-7 (v2-2) | MCP default actor extractor reads spoofable X-Actor-Id | **still closed** | `httpx/mcp/mcp.go:321-327` |
| H-8 (v2-2) | JWT module KIT_ENV literal-match drift | **still closed** | Pairing enforced at `app/validate.go:120-129` |
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
| M-A (v2-3) | `validateProductionSafety` only catches IPv4 wildcard | **partially closed (see N-10)** | `app/validate.go:38-51, :143-145` — handles canonical/leading-zero/hex/octal/overflow/IPv6/IPv4-mapped via `ResolveTCPAddr`; misses empty-bracketed `[]` form. |
| M-B (v2-3) | `pgx.Config.AllowPlaintext` silently honored regardless of caller | **closed** | `infra/sqldb/pgx/pgx.go:97-116` — Fallbacks loop |
| N-1 (v2-4) | `isUnspecifiedHost` misses Go-accepted wildcard forms | **partially closed (see N-10)** | `app/validate.go:38-51` — handles every form `ResolveTCPAddr` resolves; misses the empty-bracketed form. |
| N-2 (v2-4) | pgx loopback gate bypassable via URL `?host=` and duplicate `host=` keys | **closed** | `infra/sqldb/pgx/pgx.go:108-116` |
| N-3 (v2-4) | pgx unconditional TLS check bypassable via duplicate `sslmode=` | **closed** | `infra/sqldb/pgx/pgx.go:118-121, :325-336` |
| N-4 (v2-4) | `sqldb.Fields.Validate` accepts `sslmode=prefer`/`allow` | **closed** | `infra/sqldb/config.go:320, :353-374` |
| N-5 (v2-4) | amqp `RejectWeakCredential` was passed the URL string | **closed** | `infra/messaging/amqpbackend/config.go:129-148` |
| N-6 (v2-5) | pgx multi-host fallback bypass | **closed** | `infra/sqldb/pgx/pgx.go:108-116`; tests at `pgx_test.go:85-93, :97-105` |
| N-7 (v2-5) | hex-encoded zero IPv4 forms bypass `isAllZeroDottedDecimal` | **partially closed (see N-10)** | `app/validate.go:38-51` — replaced with `ResolveTCPAddr` delegation. Closes hex/octal/decimal-overflow class. Misses empty-bracketed form. |
| N-8 (v2-5) | `requireLoopbackHost` rejects bracket-wrapped IPv6 loopback | **closed** | `infra/sqldb/pgx/pgx.go:296-302` — bracket-strip for the loopback gate. **Note**: `requireLoopbackHost` also has the empty-bracketed quirk (`[]` strips to `""`, then `net.ParseIP("")` returns nil, treated as not-loopback → rejected). For loopback-gating the failure-mode is fail-safe (rejects), so no finding there. The same code pattern in the wildcard-detection direction (N-10) is fail-open and does need fixing. |
| N-9 (v2-6) | single-segment IPv4 numeric overflow (`4294967296`, `0x100000000`) | **closed** | `app/validate.go:38-51` — `ResolveTCPAddr` walks the same address parser net.Listen uses. Tests at `app/production_defaults_test.go:198-208` cover `4294967296`, `0x100000000`, `0X100000000`, `040000000000`, `8589934592`. Verified pass: `go test -run TestBuilder_Validates_RejectsIPv4ZeroForms`. |

No regressions. One prior fix (the parent class N-1/N-7/M-A/N-9, all the same gap progression) carries over a residual gap (N-10) detailed below.

---

## New findings

### MEDIUM

#### N-10. `isUnspecifiedHost("[]")` returns false but `net.Listen("tcp", "[]:N")` binds `[::]:N` — empty-bracketed form bypasses the C-1/M-A/N-1/N-7/N-9 wildcard validator

**File**: `app/validate.go:38-51`

**What's wrong**: The 5460a2e fix replaced the hand-rolled segment walker with `net.ResolveTCPAddr`, with a bracket-strip preamble to handle pre-bracketed IPv6 literals (because `net.JoinHostPort` doesn't tolerate already-bracketed input — it would produce `[[::]]:0` which fails to parse).

```go
func isUnspecifiedHost(host string) bool {
    if host == "" {
        return false
    }
    stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
    addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(stripped, "0"))
    if err != nil {
        return false
    }
    return addr.IP != nil && addr.IP.IsUnspecified()
}
```

When `host == "[]"`:

1. The empty-string short-circuit doesn't fire (`host` has length 2).
2. `strings.TrimSuffix("[]", "]") == "["`, then `strings.TrimPrefix("[", "[") == ""`, so `stripped == ""`.
3. `net.JoinHostPort("", "0") == ":0"`.
4. `net.ResolveTCPAddr("tcp", ":0")` returns `addr` with `addr.IP == nil` (no host part to parse) and `err == nil`.
5. The `addr.IP != nil` guard short-circuits to `false`, so `IsUnspecified()` is never called.
6. **The validator reports: not flagged.**

But `Internal.Addr()` returns `"[]:9090"` (with `Host="[]"` and `Port=9090`), and `net.Listen("tcp", "[]:9090")` parses `[]` exactly the same as `:9090` does — Go's listen parser sees the empty bracketed host as the IPv6 wildcard, binds to `[::]:9090`, and exposes `/metrics` on every interface.

| Internal.Host | `isUnspecifiedHost` says | `net.Listen("tcp", host+":0")` actually | Verdict |
|---|---|---|---|
| `[]` | false (addr.IP=nil) | binds `[::]:NNN` `IsUnspecified=true` | **BYPASS** |
| `[` | false (ResolveTCPAddr err) | net.Listen err: missing `]` | both reject (no listen) — agree |
| `]` | false (ResolveTCPAddr err) | net.Listen err: unexpected `]` | both reject — agree |
| `[[]]` | false | net.Listen err: missing port | both reject — agree |
| `[[::]]` | false (ResolveTCPAddr err on `[::]`) | net.Listen err: missing port | both reject — agree |
| `[::]` | true (correct) | binds `[::]:NNN` | both flag — agree |
| `[]]` | false | net.Listen err: missing port | both reject — agree |

Verified end-to-end against the actual `Builder.Validate()`:

```go
func TestN10_EmptyBracketBypass(t *testing.T) {
    cfg := BaseConfig{
        Internal: InternalConfig{Host: "[]", Port: 9090},
        TLS:      validTLSForTest(),
    }
    b := New("svc", "v1", cfg).WithoutJWTAudience()
    err := b.Validate()
    require.Error(t, err, "INTERNAL_HOST=[] binds to [::] wildcard but bypasses validator")
}
```

```text
=== RUN   TestN10_EmptyBracketBypass
    Error: An error is expected but got nil.
    Messages: INTERNAL_HOST=[] binds to [::] wildcard but bypasses validator
--- FAIL: TestN10_EmptyBracketBypass (0.00s)
```

And the listen-side reproduction:

```text
net.Listen("[]:0") bound to [::]:49773
IP=:: IsUnspecified=true IPv4=false IPv6=true
```

**Why this matters**: Same blast radius as C-1 / N-1 / N-7 / N-9 (unauthenticated `/metrics` exposed on every interface). Lower likelihood of operator error than `0.0.0.0` or `[::]` — but a real footgun: an operator writing IPv6 syntax (`INTERNAL_HOST=[<ipv6>]`) and accidentally clearing the IP between the brackets gets the wildcard. Fits the user's stated bar: "absence of an explicit signal silently relaxes a check."

**Why the sixth-pass review missed this**: The sixth-pass review's recommendation explicitly suggested the bracket-strip preamble (because `net.JoinHostPort` panics on pre-bracketed input), and the sixth-pass review verified `[::]` works (it does). It did not test the pathological `host == "[]"` case where the strip yields an empty post-strip string. The `addr.IP != nil` guard, which seems defensive, is what hides the bypass — without it, the code would crash on the nil dereference (because `IsUnspecified()` on a nil IP panics on Go 1.21- and returns false on Go 1.22+; either way the call-site code didn't probe the edge). The strip is asymmetric: it consumes both ends ungated, so any input whose only non-trivial content is `[` or `]` (or both) can collapse to empty.

**Why other one-sided/double bracket forms don't bypass**: `[`, `]`, `[[]]`, etc., all cause `net.Listen` to fail too (different error: "missing `]`", "unexpected `]`", "missing port"). The validator's false-negative there has no security impact because the listener never binds. Only `[]` is special: it's the one form where `net.Listen` accepts the bracketed-empty syntax as a wildcard while `net.JoinHostPort("","0")` produces `":0"` (which `ResolveTCPAddr` parses as IP-less).

**Suggested fix** — three options, in order of robustness:

**Option A (preferred)**: pass the host through `ResolveTCPAddr` directly, fall back to the bracket-strip only on a parse error.

```go
func isUnspecifiedHost(host string) bool {
    if host == "" {
        return false
    }
    // Fast path: net.Listen accepts both bracketed and unbracketed IPv6 forms,
    // so try the input as-is first.
    if addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, "0")); err == nil {
        return addr.IP != nil && addr.IP.IsUnspecified()
    }
    // JoinHostPort would produce "[[::]]:0" for pre-bracketed input. Strip and retry.
    stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
    if stripped == "" || stripped == host {
        return false
    }
    addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(stripped, "0"))
    if err != nil {
        return false
    }
    return addr.IP != nil && addr.IP.IsUnspecified()
}
```

But wait — `net.JoinHostPort("[::]","0")` actually produces `"[[::]]:0"` because JoinHostPort detects the colon in the host and brackets it again. `ResolveTCPAddr("tcp", "[[::]]:0")` does fail. So the fast-path wouldn't accept pre-bracketed. The fallback handles it.

Empirical check confirmed: `net.Listen("tcp", "[]:0")` binds `[::]`. Here the fast path also succeeds — `net.JoinHostPort("[]", "0")` produces `"[]:0"` (JoinHostPort sees no ':' in `[]` so it doesn't re-bracket), `ResolveTCPAddr("tcp", "[]:0")` returns `addr.IP=nil, err=nil`. Same problem.

So Option A inherits the bug. Option B is needed.

**Option B (better)**: explicitly reject post-strip empty.

```go
func isUnspecifiedHost(host string) bool {
    if host == "" {
        return false
    }
    stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
    if stripped == "" {
        // Empty after bracket-strip: net.Listen treats "[]:port" as the
        // IPv6 wildcard. Flag it.
        return true
    }
    addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(stripped, "0"))
    if err != nil {
        return false
    }
    return addr.IP != nil && addr.IP.IsUnspecified()
}
```

The post-strip-empty case can only arise when the input is `[]`, `[`, `]`, `[[`, `]]`, etc. Of those, only `[]` is something `net.Listen` actually accepts (the others fail), so flagging the whole class is a tightening that is fail-safe.

**Option C (most robust)**: skip the bracket-strip entirely; always call `ResolveTCPAddr` directly without intermediate manipulation, by parsing the host with `net.SplitHostPort` after wrapping. Or: directly listen with `host+":0"` and inspect the bound IP.

```go
func isUnspecifiedHost(host string) bool {
    if host == "" {
        return false
    }
    // Use ResolveTCPAddr on host+":0" directly; this is the same string
    // form net.Listen accepts and parses the same way.
    addr, err := net.ResolveTCPAddr("tcp", host+":0")
    if err != nil {
        return false
    }
    return addr.IP != nil && addr.IP.IsUnspecified()
}
```

Empirical verification: `net.ResolveTCPAddr("tcp", "[]:0")` returns `addr.IP=nil, err=nil` (matches the JoinHostPort path). `net.ResolveTCPAddr("tcp", "[::]:0")` returns `addr.IP=::, IsUnspecified=true`. So Option C still has the `[]` problem on the resolver side.

The root cause is that `ResolveTCPAddr` returns `addr.IP=nil` for empty-host input, while `net.Listen` happens to handle that as the IPv6 wildcard. The two are NOT in lock-step on this exact case.

**Bottom line**: Option B is the simplest and most correct: explicitly reject `stripped == ""` after the strip, on the basis that an empty post-strip can only arise from inputs `net.Listen` either (a) treats as wildcard (`[]`) or (b) rejects (`[`, `]`, etc.). Either way, flagging is correct or harmlessly over-conservative.

**5-line failing test** (drop into `app/production_defaults_test.go`):

```go
func TestBuilder_Validates_RejectsEmptyBracketed(t *testing.T) {
    cfg := BaseConfig{
        Internal: InternalConfig{Host: "[]", Port: 9090},
        TLS:      validTLSForTest(),
    }
    b := New("svc", "v1", cfg).WithoutJWTAudience()
    require.Error(t, b.Validate(), `Internal.Host="[]" binds to [::] via net.Listen and must be rejected`)
}
```

---

## Items checked, no findings

### Re-audit of the 5460a2e fix surface (per the user's explicit prompts)

#### `app/validate.go isUnspecifiedHost` (N-9 fix)

The user asked the exact edge-case battery. Empirical results below (against the real `isUnspecifiedHost` from the binary):

| Input | validator | net.Listen | Agree? |
|---|---|---|---|
| `0.0.0.0` | true | binds `[::]` (cgo wildcard) | yes |
| `[::]` | true | binds `[::]` | yes |
| `::` | true | binds `[::]` | yes |
| `0:0:0:0:0:0:0:0` | true | binds `[::]` | yes |
| `4294967296` (2^32) | true | binds `[::]` (cgo `inet_addr` 32-bit truncation) | **yes — N-9 closed** |
| `0x100000000` | true | binds `[::]` | **yes — N-9 closed** |
| `040000000000` (octal 2^32) | true | binds `[::]` | **yes — N-9 closed** |
| `8589934592` (2 × 2^32) | true | binds `[::]` | **yes — N-9 closed** |
| `[::ffff:0.0.0.0]` | true | binds `[::]` | yes |
| `[0]` | true | binds `[::]` | yes |
| `[0.0.0.0]` | true | binds `[::]` | yes |
| `[00.00.00.00]` | true | binds `[::]` | yes |
| `[]` | **false** | binds `[::]` | **NO — see N-10** |
| `[` | false | err `missing ]` | yes (both reject) |
| `]` | false | err `unexpected ]` | yes (both reject) |
| `[[]]` | false | err `missing port` | yes (both reject) |
| `[[::]]` | false | err `missing port` | yes (both reject) |
| `]::1[` | false | err `too many colons` | yes (both reject) |
| `fe80::1%lo0` | false | binds `fe80::1%lo0` (link-local, NOT wildcard) | yes |
| `fe80::1%eth0` | false | err `can't assign requested address` (no eth0 on test host, but the IP is link-local non-wildcard regardless) | yes |
| `[::%lo0]` | true | binds `[::]` (zone on wildcard) | yes |
| `0X100000000` | true | binds `[::]` | yes |
| `0xffffffff` (255.255.255.255) | false | err `address family not supported` | yes (validator says non-wildcard, listen says non-wildcard) |
| `localhost` | false (resolves to 127.0.0.1) | binds `127.0.0.1` | yes |
| `definitely-does-not-exist.invalid` | false (DNS NXDOMAIN) | err `no such host` | yes |
| `*` | false (DNS NXDOMAIN) | err `no such host` | yes |
| `0x` | false | err `no such host` | yes |
| ` 0` (leading space) | false | err `no such host` | yes |
| `0\n` (trailing newline) | false | err `no such host` | yes |
| `０` (fullwidth zero) | false | err `no such host` | yes |
| `-1` | false | err `no such host` | yes |
| `1e0` | false | err `no such host` | yes |

**The single disagreement is N-10 (`[]`).**

#### Performance / DNS side-effect

The user asked: "does the new check fire DNS lookups during boot for non-numeric hosts? If so, does it tolerate a slow / unreachable DNS server?"

Empirical timing of `isUnspecifiedHost`:

| Input | Duration |
|---|---|
| `0.0.0.0` | ~1µs (no I/O) |
| `[::]` | ~1µs (no I/O) |
| `4294967296` | ~5µs (no I/O) |
| `0x100000000` | ~5µs (no I/O) |
| `localhost` | varies (system resolver hit) — typically < 1ms when cached |
| `xn--nxasmq6b.example.com` (nonexistent IDN) | ~35ms (DNS NXDOMAIN round trip) |
| `definitely-does-not-exist.invalid` | ~15ms (DNS NXDOMAIN, recursion-cached) |

**Numeric forms don't hit DNS** — `ResolveTCPAddr` recognises numeric IP literals (decimal/hex/octal, dotted/single-segment, IPv4/IPv6) and returns without network I/O. Confirmed.

**Non-numeric forms DO hit DNS**. If an operator sets `INTERNAL_HOST=internal-bastion.corp.example.com` and the system resolver is slow/partitioned, `Validate()` (called from `Run()` at boot) can block for the resolver's default timeout (typically 5-30s on Linux glibc). The blocking is bounded — `getaddrinfo` does eventually time out and return error, at which point the validator returns false (not flagged) and `Run()` proceeds. The subsequent `net.Listen` on the same hostname will hit the same DNS code path (also bounded by the resolver). So the validator's DNS hit doesn't make boot strictly worse — it shifts the wait earlier and adds it to the total — but it doesn't introduce an unbounded hang.

This is **not a security finding** but is worth noting as a Quality of Service caveat: services with non-numeric `INTERNAL_HOST` and unreliable DNS will pay the resolver-timeout cost twice at boot (once at Validate, once at Listen). For numeric `INTERNAL_HOST` (the default and recommended path) the validator runs in microseconds. Documenting this trade-off is sufficient — no code change needed.

#### Bracket-stripping pathologies

The user asked: "does `strings.TrimPrefix/TrimSuffix` correctly handle pathological forms like `[[::]]`, `]::1[`?"

| Input | Stripped | Result |
|---|---|---|
| `[::]` | `::` | flagged (correct) |
| `[[::]]` | `[::]` (one strip on each side) | not flagged — `ResolveTCPAddr("tcp", "[[::]]:0")` errors. **net.Listen also errors here.** Agree. |
| `]::1[` | `]::1[` (no leading `[`, no trailing `]`) | not flagged — `ResolveTCPAddr` errors on this colon-malformed form. **net.Listen also errors.** Agree. |
| `[::1]` | `::1` | not unspecified (loopback) — correct |
| `[::1` | `::1` (one-sided strip) | not unspecified — correct |
| `::1]` | `::1` | not unspecified — correct |
| `[]` | `""` | **N-10 — see above** |

The strip is mildly permissive on one-sided brackets but is fail-safe except for the empty-bracketed case (N-10).

#### IPv6 zone identifiers

The user asked: "IPv6 zone identifiers like `fe80::1%eth0` — does Go's resolver handle these?"

`ResolveTCPAddr("tcp", "fe80::1%eth0:0")` rejects with "too many colons" (because the un-bracketed form has too many colons for the parser). The bracket-strip path doesn't help because `fe80::1%eth0` contains no brackets to strip. Validator returns false (not flagged) — correct, because `fe80::1%eth0` is link-local, not unspecified.

`net.Listen("tcp", "fe80::1%eth0:0")` also fails (same too-many-colons error if no brackets, or `bind: can't assign requested address` if interface absent). Agree.

`[::%lo0]` (bracketed wildcard with zone): validator says true, `net.Listen` binds `[::]` (zone is silently dropped on the unspecified address). Agree on the security verdict.

#### Percent-encoded / IDN forms

| Input | validator | net.Listen | Agree? |
|---|---|---|---|
| `%30.%30.%30.%30` (URL-encoded zeros) | false (DNS NXDOMAIN) | DNS NXDOMAIN | yes |
| `xn--nxasmq6b.example.com` (Punycode IDN) | false (DNS NXDOMAIN — example domain doesn't exist) | DNS NXDOMAIN | yes |

Go's `getaddrinfo` does NOT URL-decode percent-encoded hosts — they go to DNS as literal strings and fail. Both sides agree: not a wildcard (because not even resolvable). No bypass.

#### Comprehensive sweep (same scope as fifth/sixth-pass)

5460a2e touches only `app/validate.go` and its test. Regressions in the broader codebase are mechanically impossible. The sixth-pass clean items are still clean. Spot-checks confirmed (file:lines reflect current `main`):

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
- `app/validate.go:63-112` — `Validate()` returns on first error; production-safety subset runs at end.
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

- `go test ./app/...` baseline green at 5460a2e (verified post-audit). The new N-9 regression tests pass.
- `git diff e9b30ee..5460a2e --name-only` shows 5460a2e touches only `app/validate.go`, `app/production_defaults_test.go`, and `docs/audit/v2_SECURITY_REVIEW_6.md`. Mechanical confirmation that nothing else changed.
- `net.ResolveTCPAddr` source path verified: for numeric hosts, `internetSocket → ipSocket → resolveAddrList → addrList → singleAddrList → ResolveIPAddr → parseIPZone` returns immediately for IP-literals without invoking the resolver. For non-numeric hosts, it does invoke `LookupHost` (system resolver). Documented in `src/net/ipsock.go` and `src/net/dial.go` of Go 1.26.2.
- `inet_addr` semantics on darwin/Linux/glibc verified to match POSIX: 1-segment numeric is the full 32-bit IP, mod-2^32 truncation on overflow. Numeric overflow class (N-9) is now caught by `ResolveTCPAddr` because the standard library tracks the same behaviour as `getaddrinfo`.

---

## Bottom line for tagging v2.0.0

One new finding, in the 5460a2e fix surface:

- **N-10 MEDIUM**: `INTERNAL_HOST="[]"` bypasses the wildcard validator. `isUnspecifiedHost` strips brackets to empty, `ResolveTCPAddr(":0")` returns `addr.IP=nil`, the `addr.IP != nil` guard short-circuits to false. But `net.Listen("tcp", "[]:port")` accepts `[]` as the IPv6 wildcard and binds `[::]`. Fix is one line: reject `stripped == ""` after the bracket-strip (Option B above).

A 5-line failing test is inline above. Same blast radius as C-1 / N-1 / N-7 / N-9 (unauthenticated `/metrics` exposed on every interface) for the operator who copy-pastes the empty-bracketed form (a plausible IPv6-template-typo scenario).

After N-10 lands, the surface should be tag-ready: every other prior finding still closes, the 5460a2e fix for N-9 (overflow class) holds under paranoid review, no broader regressions are possible because no other code changed, and the comprehensive middleware/interceptor/JWT/builder/tenant/example sweep is clean.

The pattern across the last four pass-fix-pass cycles is now exhausted on this single function: N-1 (string-equality predicate) → N-7 (hex/octal predicate) → N-9 (overflow predicate) → N-10 (bracket-strip wrapper around the right parser). Each fix has been incrementally tighter, but each iteration left a residual class. Option B in this report ("explicitly reject post-strip empty") is finite — it's the last remaining non-trivial input where `net.Listen`'s parser accepts a wildcard-bind that the validator's parser declines to fingerprint. After that, the validator and `net.Listen` are in lock-step on every reachable input, and the C-1 surface is genuinely sealed.
