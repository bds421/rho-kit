# v2.0.0 — code-quality review (independent of security audits)

**Reviewer**: code-quality reviewer (post-eighth-pass-clean baseline)
**Branch**: main @ `ffadd33` — "docs: NOTICE file with third-party attribution for ory/x derivations"
**Scope**: code quality, architecture, naming, duplication, dead code, API consistency on the v2.0.0 surface (commits since `c113451`). Security surface is out of scope (eight clean audit passes).

---

## Verdict

**Minor cleanup needed.** No blockers. The audit-fix loop has been thorough and the new packages (`core/randstr`, `core/safecast`, `httpx/urlutil`, `httpx/pagination`, `httpx/middleware/signedrequest/redis`, `httpx/mcp`, `data/{actionlog,approval,budget}`) are genuinely well-engineered: small files, doc-rich, comprehensive tests, no dead code in the runtime path, no leftover stale opt-out names. Naming and rename consistency from the audit fixes is essentially clean. The structural concerns below are cosmetic / ergonomic — none threaten the v2.0.0 tag.

The strongest items to fix before tagging:

- **S-1** (Should-fix): Async audit-log goroutine has no panic recovery — a misbehaving Logger.Append crashes the process.
- **S-2** (Should-fix): `examples/agentic-service/README.md` claims a "refuse to boot if KIT_ENV is not dev" guard that the binary no longer implements.
- **S-3** (Should-fix): `RELEASE_NOTES_v2.md` migration-guide import block has duplicate `budget` import names and won't compile as written.
- **S-4** (Should-fix): No regression test pins the L-1 length-prefix canonicalisation fix in `data/actionlog`.
- **S-5** (Should-fix): `BaseConfig.IsDevelopment()` is exported but unreachable from the kit's runtime path — leftover from the dev-mode-removal sweep.

Everything else is Nit-class: typo in a private field name, three lint warnings, one stuttering type name, one misleading test comment, a few documentation polish items.

---

## Findings

### Should-fix

#### S-1 — Async audit goroutine has no panic recovery
**Severity**: Should-fix
**Location**: `httpx/mcp/actionlog.go:131`
**What**: When `WithAsyncAudit(true)` is set, `recordActionLog` spawns a bare goroutine via `go s.appendActionLog(...)`. If the configured `actionLogger.Append` panics (a buggy custom Logger, a nil-deref in the underlying store, a panic from the secret source), the panic propagates and crashes the entire process. Other async paths in the kit recover from panics (cf. `app/builder.go:1051-1057` for shutdown hooks).
**Suggested fix**: Wrap the goroutine body in a `defer func() { if rec := recover(); rec != nil { s.cfg.logger.Error("mcp: async audit append panicked", "panic", rec) } }()` block.

#### S-2 — Example README claims a guard that no longer exists
**Severity**: Should-fix
**Location**: `examples/agentic-service/README.md:5-9`
**What**: README states the binary "refuses to boot if `KIT_ENV` is anything but `dev` / `development` / `test` / `local`" — but `cmd/agentic-service/main.go` has zero env-var checks (the dev-mode-removal in `c113451` removed them, as documented in `docs/RELEASE_NOTES_v2.md:87-91`). A cautious operator reading the README will trust the safety net that isn't there.
**Suggested fix**: Replace the claim with the truth — package doc + main.go now warn via comments only; the "refuse to boot" guard was removed, and consumers are expected to use `app.Builder` whose always-on validator catches missing TLS/auth.

#### S-3 — Release notes import block won't compile
**Severity**: Should-fix
**Location**: `docs/RELEASE_NOTES_v2.md:174-187`
**What**: Two imports both default to the package name `budget`:
```go
"github.com/bds421/rho-kit/data/budget"
"github.com/bds421/rho-kit/httpx/middleware/budget"
```
Pasting this snippet into a real service produces `duplicate import` from the Go compiler. The kit itself uses `httpxbudget` as the alias (cf. `app/builder.go:21`). The migration guide is the first thing a consumer copies — a non-compiling sample undermines the kit's "refuse to misconfigure" stance.
**Suggested fix**: Alias as `httpxbudget "github.com/bds421/rho-kit/httpx/middleware/budget"` to match the kit's own convention.

#### S-4 — L-1 length-prefix canonicalisation has no regression test
**Severity**: Should-fix
**Location**: `data/actionlog/actionlog_test.go` (gap)
**What**: Commit `f4c84dd` introduced the length-prefix encoding in `canonical.go` to defeat newline-injection collisions ("two distinct entries producing identical signatures"). The canonical doc comment now spells out the threat, but no test in `actionlog_test.go` constructs a colliding pair and asserts the signatures differ. A future refactor that simplifies `canonicalForm` could silently re-introduce the bug.
**Suggested fix**: Add a test like `TestCanonical_NewlineInjectionDoesNotCollide` that constructs two `Entry` values whose Reason / Action fields contain newlines arranged so a plain newline-join would produce the same byte sequence, then asserts `computeSignature(entryA) != computeSignature(entryB)`.

#### S-5 — `BaseConfig.IsDevelopment()` is dead in the kit runtime
**Severity**: Should-fix
**Location**: `app/config.go:81-84`
**What**: The dev-mode-removal commit (`c113451`) eliminated all kit-side calls to `IsDevelopment`, and `RELEASE_NOTES_v2.md:93-95` explicitly says "the kit's core path simply stops calling them." But `BaseConfig.IsDevelopment()` is still exported as a method, with zero callers in the kit (verified via grep). Either downstream consumers genuinely depend on this method (in which case the doc comment should say so and the release note should explicitly preserve it), or it's a leftover from the rename. Currently it's neither documented as a downstream-API method nor removed.

Note: `core/config.IsDevelopment(string)` is a separate function that IS still used by `infra/messaging/amqpbackend/debughttp/guard.go:33`. That one is correctly preserved. The unused method is the receiver-style wrapper on `BaseConfig`.
**Suggested fix**: Either delete the method (cleaner — consumers can call `config.IsDevelopment(c.Environment)` directly) or add a doc-comment note that it's intentionally preserved as a downstream-consumer-facing affordance.

#### S-6 — Example smoke test does not exercise three of the v2.0.0 primitives it advertises
**Severity**: Should-fix
**Location**: `examples/agentic-service/internal/app/app_test.go`
**What**: Per the user's brief (Item 4), the smoke test should cover the strict-audit gate, the tenant middleware, and the dangerous-action approval flow. Currently it only covers (a) the echo tool round-trip and (b) the `-32602 Invalid params` path. There is no test that:
  - Omits `X-Tenant-Id` and asserts the strict-audit gate returns `-32603` and the echo handler did NOT execute (the H-2 audit fix's behaviour at the example layer).
  - Calls `/admin/dangerous-action` and asserts a `202 Accepted` plus a stored `approval.Request` in `approvalmem`.
  - Calls `/admin/budget` and asserts the JSON response shape.
  The kit's own `httpx/mcp/server_test.go` exercises strict-audit via `TestServer_ActionLog_StrictMode_NoTenant_RefusesDispatch`, so the regression is covered at the unit-test layer — but the example's claim of being a "reference rho-kit v2.0.0 service that demonstrates the full agentic-AI stack" implies its smoke test should exercise the wiring it ships.
**Suggested fix**: Add three tests: `TestStrictAudit_RefusesWhenTenantMissing`, `TestDangerousAction_CreatesApprovalRequest`, `TestBudgetStatus_ReturnsRemaining`.

#### S-7 — Three `ST1005` lint violations in `app/validate.go`
**Severity**: Should-fix
**Location**: `app/validate.go:164, :166, :168` (Postgres sslmode error messages)
**What**: `staticcheck ./...` reports three error strings that begin with capital letters (`"Postgres sslmode ..."`). Every other error in the same file already uses the `sentence-case` convention. The kit's CI presumably runs staticcheck — these would surface in any lint sweep.
**Suggested fix**: `s/Postgres sslmode/postgres sslmode/` on all three lines.

#### S-8 — Field name `jwtAllowAnyIssue` reads as a verb, not a noun
**Severity**: Should-fix (rename; not user-visible)
**Location**: `app/builder.go:113, :391, :409`, `app/validate.go:130`, `app/builder_helpers.go:55`
**What**: The Builder field is named `jwtAllowAnyIssue` (5 occurrences). The matching audience field is `jwtAllowAnyAudience`. The "Issue" form misses the trailing `r` and reads as a verb. This is internal-only (the public methods are `WithoutJWTIssuer` / `jwtIssuer`), but the name diverges from its semantically-paired sibling.
**Suggested fix**: Rename to `jwtAllowAnyIssuer` for consistency with `jwtAllowAnyAudience`. Pure mechanical rename — five sites, no behaviour change.

### Nit

#### N-1 — `redis.RedisNonceStore` stutters
**Location**: `httpx/middleware/signedrequest/redis/redis.go:29`
**What**: Inside the `redis` package, the type `RedisNonceStore` becomes `redis.RedisNonceStore` for callers — Go convention is to drop the package prefix from exported identifiers. The parent package's equivalent is `signedrequest.MemoryNonceStore`, where the prefix isn't repeated since "Memory" disambiguates from "Redis". In the `redis` subpackage there is only one nonce store, so the type can simply be `Store` (or `NonceStore` if the kit prefers more explicit names).
**Suggested fix**: Rename `RedisNonceStore` → `Store` (or `NonceStore`). One file, three occurrences in `redis.go`. Test imports `signedredis.New(...)` so the constructor name is unaffected. Note: this is a public-API rename — only safe pre-tag.

#### N-2 — `WithAsyncAudit(bool)` / `WithStrictAudit(bool)` have no purpose taking a bool
**Location**: `httpx/mcp/mcp.go:257, :278`
**What**: Both options take a `bool` parameter even though the default is `true` for `strictAudit` and `false` for `asyncAudit`. `WithStrictAudit(true)` is a no-op; `WithAsyncAudit(false)` is a no-op. The kit's wider convention for "opt out of always-on default" elsewhere is `WithoutXxx` (cf. `Builder.WithoutTLS`, `WithoutJWTIssuer`). Suggest `WithLooseAudit()` for the strict opt-out and keep `WithAsyncAudit()` as a no-arg toggle. This matches the kit's house style and removes the "did I pass true or false?" guesswork at the call site.
**Suggested fix**: This is a pre-tag-only API change. Could equally be deferred to v2.1 with a deprecation, but cleaner to do now.

#### N-3 — `recordActionLog` defensively converts `""` → `AnonymousActor` after the wrapper already did
**Location**: `httpx/mcp/actionlog.go:100-103`
**What**: `WithActorExtractor` wraps the user-supplied function so empty returns become `AnonymousActor` (cf. `mcp.go:165-171`). Yet `recordActionLog` re-checks `if actor == "" { actor = AnonymousActor }`. Cannot trigger; pure dead branch. Also the default `actorExtractor` in `defaultServerConfig` returns `AnonymousActor` directly. So the second check is unreachable.
**Suggested fix**: Drop the redundant if-check; the wrapping in `WithActorExtractor` is the canonical guard.

#### N-4 — `time.Second` placeholder workaround in example test
**Location**: `examples/agentic-service/internal/app/app_test.go:73-76`
**What**:
```go
// Sanity: avoid unused-import lint when time is only referenced for
// the fixture clock-shift below. Keeps the test file compilable in
// isolation.
var _ = time.Second
```
There is no fixture below. The `time` import is unused in the test file (verified via grep). The workaround and its misleading comment are both dead.
**Suggested fix**: Remove the `time` import and the placeholder line.

#### N-5 — `TestNew_PanicsOnZeroTTL` requires a real Redis to test a constructor invariant
**Location**: `httpx/middleware/signedrequest/redis/redis_test.go:89-97`
**What**: The test calls `newTestClient(t)` (which skips when Redis is unreachable) before triggering the constructor panic. The constructor's TTL validation runs purely on the input, so the test could use a no-op client (any non-nil `goredis.UniversalClient`) to keep the panic-coverage running in environments without Redis. Currently this test is silently skipped in CI environments without Redis.
**Suggested fix**: Use a stub client (e.g. `goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})` — never dialed because the panic fires first) so the test runs unconditionally. Or remove the dependency entirely with a no-op `UniversalClient` mock.

#### N-6 — `app/v2_modules.go` filename embeds a release version
**Location**: `app/v2_modules.go`, `app/v2_modules_test.go`
**What**: The file holds `tenantSpec` / `budgetSpec` / `tenantMiddleware` / `budgetMiddleware` / `actionLogger` / `approvalStore` — the v2.0.0 multi-tenant additions. Naming a file after the release version means future contributors will face a stale reference once v2 isn't current. The kit's other module files use the pattern `<feature>_module.go` (cf. `app/jwt_module.go`, `app/database_module.go`). Suggest splitting into `tenant_module.go` and `budget_module.go` to match.
**Suggested fix**: Split + rename. Mechanical, but touches two files.

#### N-7 — Floating block comment between `package` and `import` in `link_header.go`
**Location**: `httpx/pagination/link_header.go:1-7`
**What**: The third-party-attribution comment was added in the NOTICE work, sandwiched between the package clause and the imports:
```go
package pagination

// API surface (WriteLinkHeader, ParseOffset) is modeled on the offset
// pagination helpers from github.com/ory/x/pagination (Apache-2.0).
// ...

import (
```
Syntactically valid but stylistically odd — Go comments above the import block usually attach to the package clause (and become package doc) or are moved into `doc.go`. The package already has a `doc.go`, so the attribution would read more naturally there.
**Suggested fix**: Move the attribution comment into `pagination/doc.go` alongside the package overview, or wrap it as a doc-attached comment on a synthetic constant.

#### N-8 — `debughttp.Guard` doc comment misrepresents `IsDevelopment` semantics
**Location**: `infra/messaging/amqpbackend/debughttp/guard.go:20-21`
**What**: Comment says `IsDevelopment` treats "anything outside development, dev, test, local" as non-dev. But `core/config.IsDevelopment` only returns true for the literal string `"development"` (cf. `core/config/validate.go:20`). The doc-comment overlists.
**Suggested fix**: Either tighten the doc to `(uses [config.IsDevelopment] semantics — anything but "development" is treated as non-dev and rejected)` or expand `IsDevelopment` to actually accept the listed synonyms (the latter is a behaviour change and would need its own consideration).

#### N-9 — `SubjectFromHeader` deprecation now logs on every construction
**Location**: `httpx/authz/authz.go:140-156`
**What**: The audit-fix removed the `sync.Once` gate so the warning fires on every call (per the rationale in the new doc-comment). Realistic usage is a one-off `init()` so this is fine, but the warning is now inside the constructor's hot path — if a service unwisely calls `SubjectFromHeader("X-User")` per-request, the log stream will fill with deprecation warnings. The previous `sync.Once` solved the "log forever spam" problem; the new "once per construction" solves the "operator only sees the warning once and forgets" problem. Both have failure modes. Worth documenting at the package-doc level or considering a rate-limited compromise.
**Suggested fix**: Optional — accept the trade-off, but the package doc could mention the change of behaviour so consumers don't suddenly see new warnings on upgrade.

#### N-10 — RFC 5988 Link header uses `%q` for `rel` (Go-quote vs HTTP-quote divergence)
**Location**: `httpx/pagination/link_header.go:78`
**What**: `fmt.Sprintf(\`<%s>; rel=%q\`, cp.String(), rel)` uses Go's `%q` verb, which produces a Go-syntax double-quoted string with Go's escape rules (`\xNN`, `\uNNNN`, etc.). RFC 5988 quoted-strings use HTTP/1.1 (RFC 7230) escaping (only `\\` and `\"` need escaping). For ASCII rel-types ("first", "prev", "next", "last") the two encodings agree. If a custom rel-type contained a multi-byte UTF-8 char or a control byte, `%q` would emit `\uNNNN` which RFC 5988 parsers won't decode. Edge case — kit only ships the four ASCII rels — but worth a comment.
**Suggested fix**: Either pin a comment explaining the choice ("`%q` is safe here because all kit-emitted rels are ASCII alphanumerics"), or switch to a hand-rolled escaper that follows RFC 7230.

#### N-11 — `Option` type in `signedrequest/redis` vs `MemoryOption` in `signedrequest`
**Location**: `httpx/middleware/signedrequest/redis/redis.go:37` vs `httpx/middleware/signedrequest/noncestore.go:26`
**What**: The parent package types its store-options as `MemoryOption` (because there will eventually be a `RedisOption` sibling — but there isn't, since `redis` is its own subpackage). The subpackage uses the bare `Option` name. Both are defensible by themselves; the inconsistency is that the parent assumes future option types but the kit chose to use a subpackage instead. Either rename `MemoryOption` → `Option` in `signedrequest` (since there's no other type collision now) or keep both as-is. Currently the surface looks like `signedrequest.NewMemoryNonceStore(ttl, signedrequest.WithSweepEvery(...))` and `signedredis.New(c, ttl, signedredis.WithKeyPrefix(...))` — the latter is one less keystroke per call.
**Suggested fix**: Optional — collapse `MemoryOption` to `Option` in the parent now that the future-Redis-Option assumption was wrong. Pre-tag-only; defer to v2.1 if not done now.

---

## Items checked, no findings

- **Stale opt-out names** (`WithProductionDefaults`, `WithJWTAllowAnyIssuer`, `WithProductionAllowPlaintext`, `WithProductionInternalExposed`): zero leftover occurrences in any `*.go` file outside audit-doc references. Renames in commit history are clean.
- **`AllowPlaintext` without the `LoopbackForTests` suffix**: every occurrence in `infra/sqldb/pgx/` carries the full `AllowPlaintextLoopbackForTests` name. No bare `AllowPlaintext` field, method, or option anywhere in the kit.
- **`extractDSNHost` / `extractSSLMode` / `requireTLS` (standalone) / `requireLoopbackDSN` / `isAllZeroDottedDecimal` / `isAllZeroIPv4Numeric`**: all confined to test-comment narration explaining what the previous implementation did. Zero appearances in production code paths.
- **`KIT_ENV` reads in the kit runtime**: no `os.Getenv("KIT_ENV")` anywhere in the kit. The five matches in source code are all comments documenting the absence of the escape hatch.
- **Naming of recently-added `Without*` opt-outs**: `WithoutTLS`, `WithoutJWTIssuer`, `WithoutJWTAudience`, `WithInternalNonLoopback` — all four use the `Without*` / declarative pattern consistently. Doc comments are unified ("there is no KIT_ENV escape hatch").
- **Builder validation ordering**: `Validate()` returns on first error and ends with a call to `validateProductionSafety()`. The split is sensible — the production-safety subset is the always-on tightening that was previously gated; keeping it in its own method makes the dev-mode-removal narrative readable.
- **`isUnspecifiedHost` post-N-10 implementation**: bracket-only short-circuit + `ResolveTCPAddr` delegation matches the eighth-pass audit's empirical case enumeration. Logic is purely additive and idempotent on every input class.
- **`core/randstr`**: rejection sampling via `crypto/rand.Int` is correct (no modulo bias). Pre-defined charsets are sane. Tests cover invalid args, distribution sanity, multi-byte runes. Doc-comment is clear about when to use `MustString` vs `RuneSequence`. No findings.
- **`core/safecast`**: clamping helpers are correct. Property tests via `testing/quick`. Cases cover both 32-bit and 64-bit platform overflow paths. No findings.
- **`httpx/urlutil`**: no-mutation contract is enforced via tests, including encoded-segment opacity, trailing-slash preservation, query/fragment preservation, and the `Userinfo` deep-copy. No findings.
- **`httpx/pagination` (offset)**: bounds-clamping covers negative offset, invalid limit, max-limit truncation. Doc-comment justifies the design choice. Tests cover boundary conditions including `lastPageOffset` math. No findings.
- **`httpx/middleware/signedrequest/redis`**: SET NX EX is the right primitive for the deduplication contract. Failure mode (return error → middleware translates to 500) preserves the no-fail-open invariant. Concurrency test asserts exactly-one-winner under 64-way contention. No findings (the test-skipping nit above is N-5).
- **`httpx/mcp` strict/async-audit surface**: option overlap is clean — `WithStrictAudit` controls dispatch refusal, `WithAsyncAudit` controls latency vs durability, `WithActorExtractor`/`WithActorFromContext`/`WithActorFromHeader` form a clear preference hierarchy. Doc comments explicitly call out the trust requirement for each. No findings (the bool-arg nit above is N-2).
- **`data/budget`**: interface + optional `Refunder` capability are well-separated. Doc-comment justifies fixed-window vs sliding. Tests cover sentinel distinctness, capability dispatch, and fallback. No findings.
- **`data/actionlog`**: signed canonical form is correctly length-prefixed (the L-1 fix). `SignatureKeyID` is correctly excluded from the signed payload (preserving the "same content signed by a different key" tamper detection). Tests cover rotation, tamper detection, list-fail-closed, deterministic signing. The L-1 regression test gap is S-4.
- **`data/approval`**: state machine is clean. `IsTerminal` is well-named. Doc-comment justifies the Decide/MarkExecuted split. No findings.
- **Builder field count & SRP**: 20+ fields documented as a deliberate composition-root trade-off (cf. `builder.go:54-79`). The comment is honest about the trade-off and points the reader at the alternative path. Acceptable.
- **`app.Builder.WithSLO`**: thin wrapper around `WithModule(newSLOModule(...))`. Sensible default for the common case. Module surface is clean.
- **`app.Builder.WithEventBusPool`**: non-positive-size panic is the correct failure mode for a startup config error.
- **Mutually-exclusive driver guards**: `WithMySQL` / `WithPostgres` / `WithPgx` panic on duplicate-driver registration. Validate also catches `WithPgx + WithPostgres` at boot.
- **Affected-test green status**: `npx nx affected -t test --base=HEAD~10` ran 34 projects, all green (29 cached, 5 fresh). No test failure.
- **`go vet ./...` on every new package**: clean.
- **`staticcheck ./...` on every new package**: only the three S-7 `ST1005` warnings in `app/validate.go`. Everything else clean.
- **NOTICE file & ory/x attribution**: `NOTICE` correctly enumerates the four `httpx/pagination` files derived from `github.com/ory/x` (Apache-2.0). No license-compliance gap.

---

## Bottom line

The v2.0.0 surface is in genuinely good shape. The eight-pass security audit loop closed every bypass class, and the parallel quality work has been disciplined: small files, comprehensive tests on the new packages, doc-rich constructors, no leftover stale opt-out names. The findings above are mostly cosmetic; the only ones with real teeth are S-1 (panic-recovery in async audit), S-2 (README ↔ binary drift in the example), and S-3 (broken sample import in the migration guide). Fix those three before tagging; defer the rest as low-risk polish.

Codex would most likely flag S-1, S-3, S-5, S-7, and S-8 first — those are the textbook code-quality issues. S-2 and S-4 require reading the audit history and the example simultaneously, which Codex does well too. The Nit-class items are unlikely to draw Codex's attention unless prompted with a "rename suggestions" or "API consistency" sub-question.
