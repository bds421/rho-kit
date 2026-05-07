# v2.0.0 — fourth-pass security review (post-third-pass-fix)

**Reviewer**: security-reviewer agent (fourth pass)
**Branch**: main @ `9bd7877` — "fix: close third-pass audit findings (M-A, M-B + defence-in-depth)"
**Scope**: regression check on all 22 prior findings + auth fail-closed + M-1 (v1) + M-A and M-B (third pass), plus a paranoid hunt for new fail-open shapes introduced by the third-pass fix commit itself.

---

## Verdict

**Do NOT tag v2.0.0.** The third-pass fix introduced one CRITICAL, one HIGH, and three MEDIUM new findings. Most are direct regressions of the third-pass fix's stated intent ("the loopback gate makes the network risk mechanically zero", "production-safe defaults are unconditional"). All five findings are real fail-open shapes that bypass guardrails the user explicitly paid for the kit to enforce. None of the 22 prior findings regressed; the third-pass fix landed correctly in spirit but each of its three patch surfaces (`isUnspecifiedHost`, `requireLoopbackDSN`, defence-in-depth removal) has at least one concrete bypass demonstrable with five lines of Go. Fix the five new findings below and re-audit; the surface around them is otherwise clean.

---

## Regression check

For each prior finding: status + current file:line of the fix.

| ID | Title | Status | Current fix location |
|----|-------|--------|----------------------|
| Auth fail-closed (2937115) | `RequirePermission` / `RequireScope` no longer pass-through on missing claim | **still closed** | `httpx/middleware/auth/auth.go:249-271`, `:277-302`, `httpx/middleware/auth/scope.go:26-44` |
| C-1 (v2-2) | Internal ops port binds to 0.0.0.0 by default, exposes /metrics | **partially regressed (see N-1 below)** | Default loopback at `app/config.go:37-43`; validator at `app/validate.go:120-122` (now uses `isUnspecifiedHost` which still misses several wildcard forms) |
| C-2 (v2-2) | Production-defaults skip TLS check | **still closed** | Validator at `app/validate.go:111-113`; opt-out `WithoutTLS` at `app/builder.go:239-242` |
| C-3 (v2-2) | Cross-tenant key collision via `:` in tenant ID | **still closed** | `core/tenant/tenant.go:65-79`; length-prefix scoping at `data/cache/tenant/tenant.go:71-78` and `data/idempotency/tenant/tenant.go:76-83` |
| H-1 (v2-2) | Idempotency middleware collapses to shared-key on empty userID | **still closed** | `httpx/middleware/idempotency/idempotency.go:243-255` |
| H-2 (v2-2) | gRPC auth lacks RequirePermission/RequireScope/IsTrustedS2S | **still closed** | `grpcx/interceptor/auth.go:212-303` etc.; `verifyClientCertGRPC` at `:424-439` requires `len(VerifiedChains) > 0` |
| H-3 (v2-2) | Example agentic-service is copy-paste hazard | **still closed** | `examples/agentic-service/internal/app/app.go:1-29` |
| H-4 (v2-2) | Budget middleware fails open without WithMultiTenant | **still closed** | Validator at `app/validate.go:84-86` |
| H-5 (v2-2) | WithProductionDefaults does not require WithJWTAudience | **still closed** | Validator at `app/validate.go:104-106`; opt-out `WithoutJWTAudience` at `app/builder.go:253-256` |
| H-6 (v2-2) | `authz.SubjectFromHeader` reads spoofable header | **still closed** | Deprecation warn-on-every-construction at `httpx/authz/authz.go:147-152`; safe alternatives `SubjectFromTrustedHeader` at `:169-176` and `SubjectFromContext` at `:209-213` |
| H-7 (v2-2) | MCP default actor extractor reads spoofable X-Actor-Id | **still closed** | `httpx/mcp/mcp.go:321-327` |
| H-8 (v2-2) | JWT module KIT_ENV literal-match drift | **still closed** | `app/jwt_module.go` reads no env; pairing enforced at `app/validate.go:97-106` |
| M-1 (v1) | signedrequest ships only MemoryNonceStore | **still closed** | `httpx/middleware/signedrequest/redis/redis.go:1-114` |
| M-1 (v2-2) | Same as v1 M-1 | **still closed** | See above |
| M-2 (v2-2) | `RequirePermission("")` / `RequireScope("")` do not panic | **still closed** | Panic at `httpx/middleware/auth/auth.go:250-257`, `:278-283`, `scope.go:27-29` |
| M-3 (v2-2) | JWT permissions claim malformed → silent empty | **still closed** | `security/jwtutil/jwtutil.go:135-138` |
| M-4 (v2-2) | Tenant middleware silently passes safe-method GETs | **still closed** | `httpx/middleware/tenant/tenant.go:64-84, :108-121` |
| M-5 (v2-2) | gRPC logging accepts unvalidated correlation/request IDs | **still closed** | `grpcx/interceptor/logging.go:107-118` |
| M-6 (v2-2) | Auditlog HTTP middleware uses raw r.RemoteAddr | **still closed** | `httpx/middleware/auditlog/auditlog.go:45-53, :87-92` |
| M-7 (v2-2) | Auditlog HTTP middleware does not run on panics | **still closed** | `httpx/middleware/auditlog/auditlog.go:103-118` |
| M-8 (v2-2) | Budget middleware default scope is unset | **still closed** | `httpx/middleware/budget/budget.go:101` |
| M-9 (v2-2) | CSRF SkipCheck-via-Bearer bypasses Origin allowlist | **still closed** | `httpx/middleware/csrf/csrf.go:265-279, :367-378` |
| M-A (v2-3) | `validateProductionSafety` only catches IPv4 wildcard | **partially closed (see N-1)** | `app/validate.go:18-28, :120-122` — fix correct for IPv4 0.0.0.0, IPv6 [::], ::, 0:0:0:0:0:0:0:0; misses Go's other accepted wildcard forms |
| M-B (v2-3) | `pgx.Config.AllowPlaintext` silently honored regardless of caller | **partially closed (see N-2)** | `infra/sqldb/pgx/pgx.go:84-91, :265-282` — rename to `AllowPlaintextLoopbackForTests` is good; loopback gate is correct in spirit but the host-extraction parser disagrees with pgxpool's parser (DSN-discrepancy bypass) |

---

## New findings

### CRITICAL

#### N-3. `pgx.requireTLS` / `extractSSLMode` DSN-discrepancy lets a libpq key=value DSN bypass the unconditional sslmode check (works WITHOUT `AllowPlaintextLoopbackForTests`)

**File**: `infra/sqldb/pgx/pgx.go:301-336`

**What's wrong**: `extractSSLMode` walks the DSN's tokens and returns the FIRST `sslmode=` it finds. `pgxpool.ParseConfig` (the real consumer) honors the LAST occurrence in libpq key=value form (mirrors libpq's "last setting wins" rule). A DSN like

```
sslmode=require sslmode=disable host=server.example.com user=u password=p dbname=db
```

passes the validator (sees `require`) but pgxpool actually opens the connection with `sslmode=disable`. **No `AllowPlaintextLoopbackForTests` flag needed** — this is a pure validator-vs-parser discrepancy on the unconditional TLS check itself.

Verified:

```text
$ go run ... // direct pgxbackend.Connect call
BYPASS: Connect succeeded with sslmode=disable on the wire (validator saw sslmode=require)
```

**Attack scenario**: A misconfigured config-loader concatenates two `sslmode=` settings (one from a default, one from an env override) into the same DSN string instead of replacing the first. The validator sees the safe-looking first occurrence; pgxpool transmits credentials and queries in plaintext to the production DB host. The kit's package-doc claim "production-safe defaults are unconditional" (`pgx.go:14-18`) and the README's "TLS is unconditional" promise are silently violated. No log, no warning, no audit signal.

The same pattern applies to URL-form DSNs in *some* configurations: `?sslmode=require&sslmode=disable` — Go's `url.Values.Get` returns the first, which here happens to align with pgxpool's first-wins for URL form, so URL form is NOT vulnerable to this exact DSN. But the key=value form (which is the form used by `infra/sqldb/gormpostgres`'s rendered DSN, the form testcontainers' helpers may produce when the operator concatenates them, and the form library users tend to type by hand) IS vulnerable.

**Suggested fix**: Replace `extractSSLMode`'s first-wins token walk with a parse that mirrors libpq's last-wins rule. Concretely:

```go
func extractSSLMode(dsn string) string {
    // Mirror libpq's "last setting wins" rule that pgxpool.ParseConfig follows.
    var mode string
    if i := strings.Index(dsn, "?"); i >= 0 {
        for _, kv := range strings.Split(dsn[i+1:], "&") {
            if eq := strings.Index(kv, "="); eq > 0 && strings.EqualFold(kv[:eq], "sslmode") {
                mode = kv[eq+1:]
            }
        }
        return mode
    }
    for _, tok := range strings.Fields(dsn) {
        if eq := strings.Index(tok, "="); eq > 0 && strings.EqualFold(tok[:eq], "sslmode") {
            mode = tok[eq+1:]
        }
    }
    return mode
}
```

Even better: ditch the hand-rolled extractor entirely. `pgxpool.ParseConfig(cfg.DSN)` already runs at line 98 — call it FIRST and inspect `pcfg.ConnConfig.TLSConfig` (nil ⇔ plaintext) and `pcfg.ConnConfig.Host`. That guarantees the validator and the actual connection see the same settings.

**5-line failing test** (place in `infra/sqldb/pgx/pgx_test.go`):

```go
func TestConnect_RejectsDuplicateSSLModeBypass(t *testing.T) {
    dsn := "sslmode=require sslmode=disable host=remote.example.com user=u password=p dbname=db"
    _, err := Connect(context.Background(), Config{DSN: dsn})
    require.Error(t, err, "duplicate sslmode keys must not let plaintext slip past requireTLS")
    require.Contains(t, err.Error(), "sslmode")
}
```

---

### HIGH

#### N-2. `pgx.requireLoopbackDSN` host-extraction disagrees with pgxpool: query-string `?host=` and duplicate `host=` keys bypass the loopback gate behind `AllowPlaintextLoopbackForTests`

**File**: `infra/sqldb/pgx/pgx.go:258-299`

**What's wrong**: `extractDSNHost` reads the URL form via `u.Hostname()` and ignores the query string; for libpq key=value form it returns the first `host=` token. `pgxpool.ParseConfig` honors the query-string `?host=` over the URL host AND honors the LAST `host=` key in libpq form. Two trivial bypasses:

1. **URL form with query-string override**:
   ```
   postgres://u:p@localhost:5432/db?host=10.0.0.5&sslmode=disable
   ```
   Validator extracts `localhost` → loopback OK. pgxpool connects to `10.0.0.5`. Plaintext credentials sent over the network.

2. **Key=value form with duplicate host keys**:
   ```
   host=localhost host=10.0.0.5 user=u password=p dbname=db sslmode=disable
   ```
   Validator extracts the first `host=localhost` → loopback OK. pgxpool takes the last `host=10.0.0.5`. Same outcome.

Verified end-to-end (both DSNs result in successful `pgxbackend.Connect()` returns with `AllowPlaintextLoopbackForTests: true`):

```text
BYPASS: dsn="postgres://u:p@localhost:5432/db?host=10.0.0.5&sslmode=disable"
  Connect() returned no error (plaintext credentials would be sent to non-loopback host)
BYPASS: dsn="host=localhost host=10.0.0.5 user=u password=p dbname=db sslmode=disable"
  Connect() returned no error (plaintext credentials would be sent to non-loopback host)
```

The third-pass commit said "the loopback gate makes the network risk mechanically zero even if a config struct gets copy-pasted into production". With these bypasses, the network risk is NOT mechanically zero — it depends on which DSN parser an attacker reaches first.

**Attack scenario**: A test fixture sets `Config{DSN: localDSN, AllowPlaintextLoopbackForTests: true}`. A consolidating refactor of the config loader adds a query-string passthrough (`?host=$DB_OVERRIDE_HOST&...`) that production also uses. An operator setting `DB_OVERRIDE_HOST=10.0.0.5` in a production env gets a plaintext pgx pool that connects to the production DB IP. No log, no panic, no audit signal. The "verbose name + loopback gate" defence-in-depth posture both fail closed only if both parsers agree on what "the host" is — they don't.

**Suggested fix**: Don't reimplement the parser. Use pgxpool itself:

```go
func requireLoopbackDSN(dsn string) error {
    pcfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return fmt.Errorf("parse DSN: %w", err)
    }
    host := pcfg.ConnConfig.Host
    low := strings.ToLower(host)
    if low == "localhost" {
        return nil
    }
    ip := net.ParseIP(host)
    if ip == nil || !ip.IsLoopback() {
        return fmt.Errorf("DSN host %q is not a loopback address", host)
    }
    return nil
}
```

Do the same for the sslmode extractor in N-3 — `pcfg.ConnConfig.TLSConfig != nil` is the question the kit actually wants to ask, and it's free once `ParseConfig` ran.

Bonus: this also fixes the *unix-socket* case (`host=/var/run/postgresql`) which the current implementation rejects but is mechanically zero-risk by virtue of being a local unix socket — pgxpool's parsed `Host` for that case is the socket path, which `net.ParseIP` rejects too, but the operator can be permitted by special-casing `strings.HasPrefix(host, "/")`.

**5-line failing test**:

```go
func TestConnect_RejectsHostExtractionBypass(t *testing.T) {
    cases := []string{
        "postgres://u:p@localhost:5432/db?host=10.0.0.5&sslmode=disable",
        "host=localhost host=10.0.0.5 user=u password=p dbname=db sslmode=disable",
    }
    for _, dsn := range cases {
        _, err := Connect(context.Background(), Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
        require.Error(t, err, "DSN that pgxpool resolves to a non-loopback host must NOT pass the loopback gate: %s", dsn)
    }
}
```

---

### MEDIUM

#### N-1. `isUnspecifiedHost` misses Go-accepted wildcard forms `00.00.00.00`, `0`, `0.0`, `0.0.0`, `000.000.000.000`

**File**: `app/validate.go:18-28, :120-122`

**What's wrong**: The fix swaps the literal-string comparison for `net.ParseIP(host).IsUnspecified()`. That correctly catches `0.0.0.0`, `[::]`, `::`, `0:0:0:0:0:0:0:0`, and `::ffff:0.0.0.0`. But Go's `net.Listen` accepts a wider set of "unspecified" forms than `net.ParseIP` recognises. Verified by binding each:

| Internal.Host | `net.ParseIP` returns | `isUnspecifiedHost` says | `net.Listen("tcp", host+":0")` actually | Validator behaviour |
|---|---|---|---|---|
| `0.0.0.0` | unspecified | true | binds 0.0.0.0 | rejects ✅ |
| `::` | unspecified | true | errors (`Addr()` produces `:::0`) | rejects ✅ (rejection moot — kernel rejects too) |
| `[::]` | unspecified after bracket-strip | true | binds [::] | rejects ✅ |
| `00.00.00.00` | nil | **false** | **binds 0.0.0.0** | **passes — BYPASS** |
| `000.000.000.000` | nil | **false** | **binds 0.0.0.0** | **passes — BYPASS** |
| `0` | nil | **false** | **binds 0.0.0.0** | **passes — BYPASS** |
| `0.0` | nil | **false** | **binds 0.0.0.0** | **passes — BYPASS** |
| `0.0.0` | nil | **false** | **binds 0.0.0.0** | **passes — BYPASS** |
| `0.00.00.00` | nil | **false** | **binds 0.0.0.0** | **passes — BYPASS** |

The Go runtime's resolver applies `net.LookupHost` semantics that accept these as the IPv4 unspecified address, but `net.ParseIP` is stricter (it rejects leading zeros to avoid the CVE-2021-29923 ambiguity class). The two do not agree, and the validator picked the strict one — leaking the audit's intent.

**Attack scenario**: An operator who copy-pastes an `INTERNAL_HOST=00.00.00.00` from a Stack Overflow answer (or who autocompletes the value from a different env-var template that uses leading-zero padding for visual alignment with port numbers) bypasses the C-1 / M-A check entirely. `/metrics` becomes reachable on every interface. Same blast radius as the original C-1 finding.

**Suggested fix**: Don't ask `net.ParseIP` for the answer; ask `net.ResolveTCPAddr`, which mirrors `net.Listen`'s actual semantics:

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

Verified: `net.ResolveTCPAddr("tcp", "00.00.00.00:0")` returns `&TCPAddr{IP: 0.0.0.0}`, whose `IsUnspecified()` is true. Similarly for `0`, `0.0`, `0.0.0`, `000.000.000.000`.

Alternative (cheaper but more verbose): keep `net.ParseIP` and additionally walk the dotted-quad form, calling `net.ParseIP` after `strconv.Atoi` per octet — but the `ResolveTCPAddr` approach is the one-liner that mirrors the actual binding behaviour.

**5-line failing test**:

```go
func TestBuilder_Validates_RejectsLeadingZeroWildcard(t *testing.T) {
    for _, host := range []string{"00.00.00.00", "0", "0.0", "0.0.0", "000.000.000.000"} {
        cfg := BaseConfig{Internal: InternalConfig{Host: host, Port: 9090}, TLS: validTLSForTest()}
        err := New("svc", "v1", cfg).WithoutJWTAudience().Validate()
        require.Errorf(t, err, "Internal.Host=%q binds to all interfaces (verified via net.Listen) and must be rejected", host)
    }
}
```

---

#### N-4. `sqldb.Fields.Validate` accepts `sslmode=prefer` and `sslmode=allow` even though the third-pass fix said "credential-strength / sslmode / host-key-verify checks fire unconditionally"

**File**: `infra/sqldb/config.go:299-337`

**What's wrong**: The third-pass commit dropped the `IsDevelopment(environment)` gate around the sslmode check, but the check itself only rejects `""` and `"disable"`. The standalone `validatePostgresSSLMode` helper (line 353-363) returns nil for all of `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`, and the secondary check at line 327-330 only catches the empty-or-disable case. So `sslmode=prefer` and `sslmode=allow` — both of which silently degrade to plaintext on a TLS handshake error (the exact failure mode the kit advertises it protects against) — pass `Fields.Validate` cleanly.

Verified:

```text
sslmode="prefer"     | err=<nil>
sslmode="allow"      | err=<nil>
sslmode="disable"    | err=SVC_DB_SSL_MODE must be set to require/verify-ca/verify-full (got "disable")
sslmode="require"    | err=<nil>
sslmode=""           | err=SVC_DB_SSL_MODE must be set to require/verify-ca/verify-full (got "")
sslmode="verify-ca"  | err=<nil>
sslmode="verify-full" | err=<nil>
```

The Builder-path validator (`app/validate.go:127-136`) correctly rejects all three of `disable`, `allow`, `prefer`. The standalone `Fields.Validate` does NOT. This means a downstream consumer who calls `cfg.ValidateBase()` followed by `f.Validate("SVC", env, "postgres")` (the documented standalone pattern shown in `examples/app/main.go:38-43`) gets a different security guarantee than someone going through `app.Builder`.

The third-pass commit's claim "the kit's 'no development mode' policy" implies the unconditional check matches the Builder's check. It doesn't.

**Attack scenario**: A service that doesn't use `app.Builder` (a CLI tool or a non-HTTP daemon that uses `sqldb.LoadFields` + `f.Validate` directly) ships with `DB_SSL_MODE=prefer` from the operator's first-run debugging session. Validation passes; the connection silently degrades to plaintext when a TLS handshake error occurs (e.g., self-signed cert in a staging environment); credentials and queries leak.

**Suggested fix**: Tighten `Fields.Validate` to mirror the Builder's check:

```go
if driver == "postgres" {
    sslMode := f.Database.Option("sslmode", "")
    normalized := strings.ToLower(sslMode)
    switch normalized {
    case "require", "verify-ca", "verify-full":
        // ok
    case "":
        return fmt.Errorf("%s_DB_SSL_MODE must be set to require/verify-ca/verify-full", envPrefix)
    case "disable", "allow", "prefer":
        return fmt.Errorf("%s_DB_SSL_MODE=%q does not fail closed on TLS handshake error; use require/verify-ca/verify-full", envPrefix, sslMode)
    default:
        return fmt.Errorf("%s_DB_SSL_MODE=%q is unrecognized", envPrefix, sslMode)
    }
}
```

This collapses `validatePostgresSSLMode` and the `disable`-only secondary check into one place that fails closed on every loose mode.

**5-line failing test**:

```go
func TestFields_Validate_RejectsLooseSSLMode(t *testing.T) {
    for _, mode := range []string{"prefer", "allow"} {
        f := Fields{Database: Config{
            Host: "h", Port: 5432, User: "u", Password: "a-strong-password-here", Name: "n",
            Options: map[string]string{"sslmode": mode},
        }}
        require.Errorf(t, f.Validate("SVC", "production", "postgres"),
            "sslmode=%q silently degrades to plaintext and must be rejected", mode)
    }
}
```

---

#### N-5. `amqpbackend.ValidateRabbitMQ` calls `RejectWeakCredential` with the URL string instead of the password — `guest:guest` and other weak default credentials pass the strength check

**File**: `infra/messaging/amqpbackend/config.go:122`

**What's wrong**: The third-pass commit made the weak-credential check unconditional ("credential-strength checks fire unconditionally"). But the call has been wrong since v1.0.0 and the third-pass made the broken check fire unconditionally instead of fixing it:

```go
if err := config.RejectWeakCredential("RABBITMQ_PASSWORD", resolved); err != nil {
    return err
}
```

`resolved` is the AMQP URL (e.g. `amqp://guest:guest@host:5672/`), not the password. `RejectWeakCredential(name, value string)` tests `len(value) < 12` and `strings.Contains(strings.ToLower(value), "changeme")`. The URL is always > 12 chars and rarely contains "changeme", so:

| URL | Should reject? | Actually rejects? |
|-----|---|---|
| `amqp://guest:guest@host:5672/` | yes (default credentials) | **no** (URL > 12 chars, no "changeme") |
| `amqp://prod:short@h:5672/` | yes (5-char password) | **no** (URL > 12 chars) |
| `amqp://prod:strong-pw-that-is-long-enough@h:5672/` | no | no ✅ |
| `amqp://prod:changeme@h:5672/` | yes | yes (URL contains "changeme") |
| `amqp://changeme:changeme@h:5672/` | yes | yes |

The check is decoupled from the password's actual strength. The third-pass-fix intent ("credential-strength checks fire unconditionally") is not satisfied because the broken check is now fired unconditionally — a strictly worse outcome than before, where at least the development-environment branch acknowledged the relaxed bar.

**Attack scenario**: A service using the kit's RabbitMQ defaults loads `RABBITMQ_USER=guest RABBITMQ_PASSWORD=guest` (the kit's literal defaults at line 105). `ValidateRabbitMQ` sees the resolved URL `amqp://guest:guest@host:5672/`, length 30, no "changeme" substring → returns nil. The service ships to production with the well-known default RabbitMQ credentials. An attacker on the broker network logs in as `guest`, manages exchanges/queues, intercepts traffic.

**Suggested fix**: Pass the password, not the URL.

```go
// If the operator passed RABBITMQ_URL directly, parse it back to extract the password.
password := f.RabbitMQ.Password
if f.RabbitMQ.URL != "" {
    if u, err := url.Parse(f.RabbitMQ.URL); err == nil && u.User != nil {
        if pw, ok := u.User.Password(); ok {
            password = pw
        }
    }
}
if err := config.RejectWeakCredential("RABBITMQ_PASSWORD", password); err != nil {
    return err
}
```

Bonus: also reject the literal-default `"guest"` (the kit defaults `RABBITMQ_PASSWORD` to `"guest"` at `LoadRabbitMQFields` line 105 — the strength check should at minimum refuse the kit's own default value).

**5-line failing test**:

```go
func TestValidateRabbitMQ_RejectsDefaultCredentials(t *testing.T) {
    f := RabbitMQFields{RabbitMQ: RabbitMQConfig{Host: "rabbit.example.com", Port: 5672, User: "guest", Password: "guest", VHost: "/"}}
    err := f.ValidateRabbitMQ("production")
    require.Error(t, err, "default guest:guest must be rejected as a weak credential")
}
```

---

## Items checked, no findings

- `app/validate.go:18-28` `isUnspecifiedHost` — handles bracket-stripping correctly (`[::]` → `::` → IsUnspecified); empty-string passthrough is safe because `InternalConfig.Addr` resolves "" to `127.0.0.1` at `app/config.go:37-43`. (See N-1 for the leading-zero gap.)
- `infra/sqldb/pgx/pgx.go:84-95` — `Connect` correctly applies `requireLoopbackDSN` when `AllowPlaintextLoopbackForTests` is set, applies `requireTLS` otherwise. Branching is exclusive (no fall-through). The rename to `AllowPlaintextLoopbackForTests` is verbose enough to be loud in code review.
- `infra/sqldb/pgx/integration_test.go:36, :44, :70` — every test fixture migrated from `AllowPlaintext` to `AllowPlaintextLoopbackForTests`; no stale references.
- `httpx/authz/authz.go:147-152` `SubjectFromHeader` — emits warn on every construction. Per-request hot-path constructions log per-request which is the user's stated intent ("misuse can no longer hide in log volume"). High construction rates are an anti-pattern; the log volume itself is operational pressure to fix the misuse.
- `app/jwt_module.go:67-73` — Error log on the unreachable default branch fires once per process at provider construction time, doesn't crash, doesn't paniic — operator gets a loud signal but the request path still works (jwt verification proceeds with WithAllowAnyIssuer).
- `app/jwt_module.go:55-71` — switch is exhaustive with respect to `cfg.allowAnyIssuer` and `cfg.expectedIssuer`. Cannot fall through silently.
- `app/builder.go:769-782` — `Run()` calls `Validate()` before any infrastructure spins up; no public `Build()` bypasses validation. (Same as third-pass.)
- `app/builder.go:212-256` — opt-outs `WithInternalNonLoopback`, `WithoutTLS`, `WithoutJWTAudience` each set a typed boolean read by the validator; cannot be silently elided.
- `infra/redis/config.go:122-139` — `ValidateRedis` requires REDIS_PASSWORD when not using URL; URL form bypass is acknowledged via the `f.Redis.URL == ""` check (the kit assumes a user-supplied URL is intentional). Acceptable; not a regression.
- `infra/sqldb/config.go:296-337` — environment parameter no longer consulted; check fires unconditionally. (See N-4 for the loose-mode gap.)
- `infra/storage/s3backend/config.go:100-126` — environment parameter no longer consulted; weak-credential check fires unconditionally and uses the actual `c.SecretAccessKey`. Correct.
- `infra/storage/azurebackend/config.go:59-76` — same correct pattern as S3.
- `infra/storage/sftpbackend/config.go:75-101` — `InsecureSkipHostKeyVerify=true` rejected unconditionally in `Validate()`; weak-credential check uses actual `c.Password`. Note: `sftpbackend.New(cfg)` does NOT call `cfg.Validate()` — direct constructor callers can bypass — but this is a pre-existing pattern, not a third-pass regression.
- `httpx/middleware/csrf/csrf.go:265-279, :367-378` — Origin check before SkipCheck-via-Bearer; KIT_ENV reads gone.
- `httpx/middleware/auth/auth.go:249-302` — fail-closed on missing claim AND missing trusted-S2S marker.
- `httpx/middleware/auth/auth.go:199-201` — trusted-S2S marker stamped only on the verified-mTLS branch.
- `httpx/middleware/auth/scope.go:26-44` — same fail-closed shape as `auth.go`.
- `httpx/middleware/tenant/tenant.go:64-84, :108-121` — `WithRequiredOnSafeMethods` opt-in remains; default keeps safe-method bypass.
- `httpx/middleware/budget/budget.go:101` — default scope `"tenant"`; backend errors → 503.
- `httpx/middleware/idempotency/idempotency.go:222-224, :243-255` — construction panic on missing extractor unless `WithAllowSharedKeys`; empty userID returns 400.
- `httpx/middleware/auditlog/auditlog.go:45-53, :87-92, :103-118` — trusted-proxies + deferred-audit + panic-recording all in place.
- `httpx/middleware/timeout/timeout.go:19-26, :94-97` — WebSocket bypass requires explicit opt-in.
- `httpx/middleware/signedrequest/signedrequest.go` — verify ordering: timestamp → signature decode → key resolve → body read → MAC compare → nonce store. `nonceStore == nil` panics at construction.
- `httpx/middleware/signedrequest/redis/redis.go:1-114` — `SET NX EX` atomic; nil client panics; ttl<=0 panics; failure → 500.
- `httpx/sign/sign.go:138-147` — `defaultNonce` panics on `crypto/rand` error.
- `httpx/healthhttp/handler.go:79, :87-92` — `Cache-Control: no-store` on /metrics, /health, /ready.
- `httpx/middleware/recover/recover.go` — `http.ErrAbortHandler` re-raised; `recordingWriter` flags `wroteHeader`.
- `httpx/middleware/cors/cors.go` — delegates to `jub0bs/cors`; panics on invalid config.
- `httpx/middleware/secheaders/secheaders.go:172-186` — `shouldSetHSTS` correctly gates on `r.TLS != nil` OR `WithForceHSTS` OR (`WithTrustedProxiesForProto` AND verified-IP AND `X-Forwarded-Proto: https`).
- `httpx/middleware/ratelimit/{ratelimit,keyed,tenant}.go` — sharded fixed-window; tenant fail-closed on missing tenant (400) and on limiter error (500).
- `httpx/middleware/approval/approval.go` — body capped at 64 KiB; tenant required (400 on absent).
- `httpx/middleware/clientip/clientip.go` — defaults to loopback-only trusted proxies; `ParseTrustedProxiesStrict` for fail-loud parsing.
- `httpx/mcp/mcp.go:321-327, :174-216, :255-279` — default actor extractor anonymous; `WithStrictAudit(false)` is explicit opt-out for the audit-precheck gate.
- `httpx/mcp/server.go:202-218, :300-338` — `auditPrecheck` refuses dispatch in strict mode; `mapErrorToRPC` surfaces generic "internal error" to caller.
- `grpcx/interceptor/auth.go:212-303, :305-372, :415-439` — RequirePermission/RequireScope panic on empty args; mTLS path requires verified chains; trusted-S2S marker stamped only by mTLS branch.
- `grpcx/interceptor/recovery.go` — defer-recover before handler in unary and stream.
- `grpcx/interceptor/deadline.go` — only tightens deadlines.
- `grpcx/interceptor/logging.go:107-118` — `isValidID` rejects non-printable ASCII; regenerates on invalid.
- `grpcx/interceptor/metrics.go` — labels are method + grpc.Code (no cardinality risk).
- `core/secret/secret.go:158-187` — value-receiver redaction; safe by-value.
- `core/tenant/tenant.go:65-79` — forbidden bytes rejected; `NewID` calls `ValidateID`.
- `data/cache/tenant/tenant.go:71-78`, `data/idempotency/tenant/tenant.go:76-83` — length-prefix scoping.
- `infra/messaging/buffered_publisher.go:117, :162-164` — panic on `stateFile == ""` without `WithEphemeralBuffer`; KIT_ENV reads gone.
- `infra/messaging/amqpbackend/debughttp/guard.go:33` — `IsDevelopment` reads here gate debug endpoints OFF in non-dev. Fail-closed; correct.
- `core/config/validate.go:19`, `app/config.go:81-83` — `IsDevelopment` retained as public API; never read from a security-critical path. The Builder validator (`app/validate.go`) is the canonical entry.
- Stale-reference sweep: `grep -rn "WithProductionDefaults\|WithProductionAllowPlaintext\|WithProductionInternalExposed\|WithJWTAllowAnyIssuer\|WithJWTAllowAnyAudience" --include="*.go"` → zero hits. The release-notes doc (`docs/RELEASE_NOTES_v2.md:50-120`) is the only place these names appear, intentionally documenting old → new.
- `grep -rn "AllowPlaintext " --include="*.go"` → only the renamed `AllowPlaintextLoopbackForTests`. No stale `AllowPlaintext` field references in code.
- **One stale doc reference** to the OLD `AllowPlaintext` field at `docs/RELEASE_NOTES_v2.md:80` ("`Config{AllowPlaintext: true}`"). Not a security finding (won't compile if a user copy-pastes — they get a build error pointing them at the new name); flagged for completeness.

---

## What was checked vs. v2_SECURITY_REVIEW_3.md scope

Re-read end-to-end (per the audit prompt):
- HTTP middleware: auth, scope, tenant, budget, idempotency, csrf, cors, secheaders, auditlog, timeout, ratelimit, signedrequest, approval, recover, mcp, clientip.
- gRPC interceptors: auth, recovery, deadline, logging, metrics.
- Auth/JWT/TLS: jwtutil, netutil/tls, httpx/authz.
- App / Builder: config, builder, validate, jwt_module, builder_helpers.
- MCP: mcp.go, server.go, actionlog.go.
- Tenant scoping: core/tenant, data/cache/tenant, data/idempotency/tenant.
- Infra: sqldb (config + pgx + deprecated), redis, messaging (amqpbackend + buffered_publisher), storage (s3 + azure + sftp), debughttp.
- Examples: examples/agentic-service.

Cross-cutting checks performed:
- DSN parsing discrepancies between `pgxbackend.requireLoopbackDSN` / `requireTLS` and `pgxpool.ParseConfig` (tested via direct `pgxbackend.Connect` calls; both N-2 and N-3 reproduced).
- Wildcard binding behaviour of `net.Listen` vs `net.ParseIP` (N-1 reproduced via real `app.Builder.Validate()` against five wildcard variants).
- Documentation drift on storage backends (`s3backend/config.go:99` still says "in non-development environments" but check is now unconditional — minor doc, not finding).
- Validator vs constructor coverage: `sftpbackend.New(cfg)` does not call `cfg.Validate()` so `InsecureSkipHostKeyVerify=true` is honored at the constructor — pre-existing structural issue, not a regression.

---

## Bottom line for tagging v2.0.0

Five new findings:
- **N-3 CRITICAL**: pgx's unconditional TLS check is bypassable with a duplicate-`sslmode=` DSN — no opt-out flag needed. Real plaintext credentials over the wire.
- **N-2 HIGH**: pgx's loopback gate behind `AllowPlaintextLoopbackForTests` is bypassable via query-string `?host=` or duplicate `host=` keys. Negates the third-pass M-B fix's "mechanically zero risk" claim.
- **N-1 MEDIUM**: `isUnspecifiedHost` misses six Go-accepted wildcard forms. Negates the third-pass M-A fix for IPv4 leading-zero variants.
- **N-4 MEDIUM**: `sqldb.Fields.Validate` accepts `sslmode=prefer`/`sslmode=allow` despite the third-pass commit's "unconditional check" claim.
- **N-5 MEDIUM**: `amqpbackend.ValidateRabbitMQ` weak-credential check is wired to the URL string, not the password — the well-known `guest:guest` default credentials pass.

Each has a 5-line failing test inline. None are exploitable by default — all require an operator to construct a specific config — but each fits the user's stated bar: "absence of an explicit signal silently relaxes a check". The shortest patch path is to delete the hand-rolled DSN extractors in `pgx.go` and call `pgxpool.ParseConfig` directly (closes both N-2 and N-3 in one diff), switch `isUnspecifiedHost` to `net.ResolveTCPAddr` (closes N-1), tighten `Fields.Validate`'s sslmode switch to mirror the Builder's (closes N-4), and pass the password rather than the URL to `RejectWeakCredential` in `ValidateRabbitMQ` (closes N-5). After those four diffs land, re-audit; the surface around them is otherwise clean.
