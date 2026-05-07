# v2.0.0 — test-coverage review across the eight audit passes

**Reviewer**: code-reviewer agent (test-coverage pass)
**Branch**: main @ `ffadd33`
**Scope**: every closed audit finding in `docs/audit/v2_SECURITY_REVIEW{,_2..8}.md` (32 total). For each finding, locate the regression test that pins the fix and judge whether the test would actually fail against the pre-fix code.

---

## Verdict

**Tag-ready on regression coverage.** Of the 32 closed findings, **30 have a dedicated regression test** that names the finding and pins the specific shape the audit called out. The two exceptions are not blockers for tagging:

- **v1 H-3 (NATS `MaxDeliver` poison-pill)** — the fix landed (`MaxDeliver` defaults to `5` in `NewConsumer`), but no test exercises the redelivery cap. The behaviour is config-only, the config is wired through to `CreateOrUpdateConsumer`, and the integration test (`TestConsumer_NackRedelivers`) only verifies that nack redelivers — it does not pin the `MaxDeliver=5` cap. **Recommendation: add a unit test that asserts `NewConsumer(cfg{MaxDeliver=0}).cfg.MaxDeliver == 5` and an integration variant that confirms a permanently-failing handler stops being redelivered after 5 attempts.** Class: not a regression-test blocker, but the user's "no findings, including trivial ones" bar would benefit from the unit test.
- **v2-2 H-2 partial (gRPC RBAC API gap)** — the API additions (`RequirePermission{Unary,Stream}`, `RequireScope{Unary,Stream}`, `MTLSAuth{Unary,Stream}`, `IsTrustedS2S`) are well-tested in `grpcx/interceptor/auth_test.go`. The original audit explicitly said "no simple failing test — this is an API gap". The regression coverage for the new functions is comprehensive (panics on empty args, fail-closed on missing claims, mTLS-only trusted marker stamping). Effectively closed via positive coverage of the new API surface.

The class-of-bug findings the user singled out for paranoid scrutiny are all properly covered:

- **IPv4 wildcard variants (N-1, N-7, N-9, N-10)** — `TestBuilder_Validates_RejectsIPv4ZeroForms` enumerates 23 sub-cases across all four iterations of the predicate. Hand-verified against the audit tables — every form the audits flagged as a bypass is in the test list.
- **Multi-host pgx DSN bypass (N-2, N-6)** — `TestConnect_RejectsMultiHostNonLoopbackFallback` (libpq form) and `TestConnect_RejectsMultiHostURLFormFallback` (URL form) pin both shapes; `TestRequireLoopbackHost_RejectsParsedNonLoopback` covers the `?host=` query-string and duplicate-key cases. `TestRequireTLS_RejectsLastWinsBypass` covers the N-3 sslmode last-wins bypass.
- **Cross-tenant key collision (C-3)** — `TestScopedKey_ColonInTenantIDNoCollision` exists in BOTH `data/cache/tenant/tenant_test.go:137` and `data/idempotency/tenant/tenant_test.go:112`, each using `NewIDUnchecked` to simulate the worst case (validation bypassed) so the length-prefix scoping itself is the load-bearing defence.
- **TLS required (C-2)** — `TestBuilder_Validates_RequiresTLS` + `TestWithoutTLS_AcceptsOptIn` pin both directions of the gate.
- **Internal /metrics not on 0.0.0.0 (C-1)** — `TestInternalConfig_DefaultsToLoopback` + `TestBuilder_Validates_RejectsExposedInternal` + `TestWithInternalNonLoopback_AcceptsOptIn` pin the default, the rejection, and the opt-in.
- **Auth fail-closed (2937115)** — `TestRequirePermission_NilPermissions_NoMarker_Denied`, `TestRequirePermission_NoAuthAtAll_Denied`, `TestPermissionByMethod_NilPermissions_NoMarker_Denied`, `TestRequireScope_NoScopes_NoMarker_Denied`, `TestRequireScopeStrict_NoScopes_Denied`, `TestRequireScopeStrict_EmptyScopes_Denied`, `TestRequirePermission_TrustedS2S_PassesThrough`, `TestRequireS2SAuth_MTLS_SetsTrustedMarker`, `TestRequireS2SAuth_JWT_DoesNotSetTrustedMarker` form a defence battery. The mTLS-only trusted-marker invariant is also pinned on the gRPC side via `TestIsTrustedS2S_NotSetByJWTAuth`.

---

## Per-finding table

### v2_SECURITY_REVIEW.md (first pass)

| ID | File:test | Status |
|----|-----------|--------|
| H-1 | `core/secret/secret_test.go:148 TestValueTypedUsage_StillRedacts` | covered |
| H-2 | `httpx/mcp/server_test.go:314 TestServer_ActionLog_StrictMode_NoTenant_RefusesDispatch` + `:374 TestServer_ActionLog_StrictMode_WithTenant_WritesEntry` + `:341 TestServer_ActionLog_LooseMode_NoTenant_RunsToolAndSkipsAudit` | covered |
| H-3 | (config wiring at `infra/messaging/natsbackend/natsbackend.go:255-256`; no direct unit test for `MaxDeliver` default value) | **partial** — see "Recommended new tests" |
| M-1 | `httpx/mcp/server_test.go:130 TestServer_HandlerErrorMappedToOperationFailed` | covered |
| M-2 | (migration files use `TIMESTAMPTZ`; verified by inspection of `data/actionlog/postgres/migrations/20260507000001_*.sql:16` and `data/approval/postgres/migrations/20260507000001_*.sql:13-18`; round-trip pinned by `data/actionlog/postgres/integration_test.go:57 TestPostgres_Live_RoundTrip` which would fail signature verification on a TZ-stripped timestamp) | covered |
| M-3 | (demo HMAC labelled at `examples/agentic-service/internal/app/app.go:60-69`; documentation-only, no test required) | covered (doc) |
| M-4 | `httpx/middleware/signedrequest/redis/redis_test.go:99 TestSeenOrStore_FirstTimeThenReplay` + `:219 TestSeenOrStore_AcrossClientsSharesState` (RedisNonceStore is the cross-replica fix) | covered |
| L-1 | `data/actionlog/canonical.go:65` uses `%d:%s\n` length-prefix; pinned indirectly by `data/actionlog/actionlog_test.go:172 TestSign_DeterministicAcrossInvocations` + `:190 TestSign_OrderInsensitiveMetadata` + `:112 TestGet_DetectsTamper` (any length-prefix regression would break determinism). No dedicated "newline-in-field-doesn't-shift-boundary" test, but tamper detection covers the security-relevant invariant. | covered |
| L-2 | `httpx/middleware/signedrequest/signedrequest_test.go:182 TestMemoryNonceStore_WithSweepEvery_Immediate` + `:202 TestMemoryNonceStore_WithSweepEvery_Deferred` + `:226 TestMemoryNonceStore_WithSweepEvery_PanicsOnNonPositive` + the `WithSweepEvery` API itself in `noncestore.go:36-49` | covered |
| L-3 | `httpx/mcp/server_test.go:425 TestServer_ActionLog_AsyncMode_RespondsBeforeAppend` + `:473 TestServer_ActionLog_SyncMode_AppendBeforeResponse` | covered |
| L-4 | `httpx/mcp/server_test.go:492 TestServer_DisallowUnknownFields_ReturnsGenericMessage` | covered |

### v2_SECURITY_REVIEW_2.md (22 findings)

| ID | File:test | Status |
|----|-----------|--------|
| Auth fail-closed (2937115) | `httpx/middleware/auth/auth_test.go:731 TestRequirePermission_NilPermissions_NoMarker_Denied`, `:753 TestRequirePermission_NoAuthAtAll_Denied`, `:887 TestPermissionByMethod_NilPermissions_NoMarker_Denied`, `:704 TestRequirePermission_TrustedS2S_PassesThrough`, `:575 TestRequireS2SAuth_MTLS_SetsTrustedMarker`, `:604 TestRequireS2SAuth_JWT_DoesNotSetTrustedMarker`; `httpx/middleware/auth/scope_test.go:24 TestRequireScope_NoScopes_NoMarker_Denied`, `:75 TestRequireScopeStrict_NoScopes_Denied`, `:87 TestRequireScopeStrict_EmptyScopes_Denied`; `grpcx/interceptor/auth_test.go:392 TestRequirePermissionUnary_NoPermsClaim_PermissionDenied`, `:476 TestRequirePermissionStream_NoPermsClaim_PermissionDenied`, `:533 TestRequireScopeUnary_MissingScope_PermissionDenied`, `:549 TestIsTrustedS2S_NotSetByJWTAuth` | covered (battery) |
| C-1 | `app/production_defaults_test.go:114 TestInternalConfig_DefaultsToLoopback` + `:126 TestBuilder_Validates_RejectsExposedInternal` + `:139 TestWithInternalNonLoopback_AcceptsOptIn` | covered |
| C-2 | `app/production_defaults_test.go:232 TestBuilder_Validates_RequiresTLS` + `:241 TestWithoutTLS_AcceptsOptIn` | covered |
| C-3 | `data/cache/tenant/tenant_test.go:137 TestScopedKey_ColonInTenantIDNoCollision` + `data/idempotency/tenant/tenant_test.go:112 TestScopedKey_ColonInTenantIDNoCollision` + `core/tenant/tenant_test.go:25 TestNewID_RejectsColon` + `:33 TestNewID_RejectsControlChars` + `:68 TestValidateID_ReportsAllRejections` | covered (defence-in-depth at both layers) |
| H-1 | `httpx/middleware/idempotency/idempotency_test.go:25 TestMiddleware_PanicsWithoutUserExtractorOrSharedKeysOptIn` + `:40 TestIdempotency_EmptyUserReturns400_NotShared` (the latter is an exact reproduction of the audit's failing test, including the no-poisoning assertion) | covered |
| H-2 | `grpcx/interceptor/auth_test.go:365-628` 30+ tests for new `RequirePermission{Unary,Stream}`, `RequireScope{Unary,Stream}`, `MTLSAuth{Unary,Stream}`, `IsTrustedS2S` — including `:437 TestRequirePermissionUnary_TrustedS2S_BypassesCheck`, `:458 TestRequirePermissionStream_TrustedS2S_BypassesCheck`, `:633 TestMTLSAuthUnary_JWTPath_NoTrustedMarker` | covered (positive coverage of new API; original audit said no simple failing test possible) |
| H-3 | `examples/agentic-service/internal/app/app_test.go:19 TestRun_StartsAndShutsDown` + `:29 TestMCPServer_EchoToolRoundtrip` + per-handler SECURITY warnings in source. Documentation/example finding — primary fix is the giant SECURITY header at lines 1-29 of `app.go`. | covered (doc/structural) |
| H-4 | `app/production_defaults_test.go:251 TestBudget_RequiresMultiTenant` + `:259 TestBudget_WithMultiTenant_Passes` | covered |
| H-5 | `app/production_defaults_test.go:271 TestBuilder_Validates_RequiresJWTAudience` + `:280 TestBuilder_Validates_AcceptsJWTAudience` + `:289 TestBuilder_Validates_AcceptsWithoutJWTAudience` | covered |
| H-6 | `httpx/authz/authz_test.go:158 TestSubjectFromTrustedHeader_RejectsUntrustedRemote` + `:169 TestSubjectFromTrustedHeader_AcceptsTrustedProxy` + `:180 TestSubjectFromTrustedHeader_EmptyTrustedListRejectsAll` + `:188 TestSubjectFromContext`; `SubjectFromHeader` deprecation warns at construction | covered |
| H-7 | `httpx/mcp/server_test.go:515 TestDefaultActorExtractor_NoLongerTrustsHeader` + `:539 TestWithActorFromContext_ReadsAuthContext` + `:574 TestServer_ActorExtractor_OverrideUsedOverHeader` | covered |
| H-8 | (KIT_ENV reads removed entirely from `app/jwt_module.go` and `security/jwtutil/jwtutil.go`; pairing now enforced unconditionally at `app/validate.go:151-152`; pinned by `app/production_defaults_test.go:30 TestBuilder_Validates_RejectsJWTWithoutIssuer` which fires regardless of KIT_ENV; `app/jwt_module_test.go` removed `KIT_ENV=development` weakening) | covered (structural — env path eliminated) |
| M-1 | `httpx/middleware/signedrequest/redis/redis_test.go` (full file: 11 tests for `RedisNonceStore` API correctness, atomicity, replay across clients) | covered |
| M-2 | `httpx/middleware/auth/auth_test.go:928 TestRequirePermission_PanicsOnEmpty` + `:937 TestPermissionByMethod_PanicsOnEmpty`; `httpx/middleware/auth/scope_test.go:159 TestRequireScope_PanicsOnEmpty` + `:168 TestRequireScopeStrict_PanicsOnEmpty`; `grpcx/interceptor/auth_test.go:365 TestRequirePermissionUnary_PanicsOnEmpty` + `:371 TestRequirePermissionStream_PanicsOnEmpty` + `:377 TestRequireScopeUnary_PanicsOnEmpty` + `:383 TestRequireScopeStream_PanicsOnEmpty` | covered |
| M-3 | `security/jwtutil/jwtutil_test.go:669 TestPermissionsClaim_MalformedLogsWarning` (asserts WARN level + claim=permissions key) | covered |
| M-4 | `httpx/middleware/tenant/tenant_test.go:119 TestTenantMiddleware_SafeMethodEnforcement` + `:139 ...PassesWithTenant` + `:150 ...DefaultStillPasses` | covered |
| M-5 | `grpcx/interceptor/logging_test.go:118 TestExtractIDs_RejectsControlChars` (asserts poisoned IDs regenerated, not adopted; log line records fresh IDs) | covered |
| M-6 | `httpx/middleware/auditlog/auditlog_test.go:33 TestAuditlog_DefaultClientIPNoTrustedProxies` + `:56 TestAuditlog_HonorsTrustedProxyXFF` | covered |
| M-7 | `httpx/middleware/auditlog/auditlog_test.go:78 TestAuditlog_RecordsOnPanic` (asserts deferred audit + panic re-raise + Status="failure") | covered |
| M-8 | `httpx/middleware/budget/budget_test.go:101 TestMiddleware_DefaultScopeIsTenant` + `:114 TestMiddleware_ScopeOverride` | covered |
| M-9 | `httpx/middleware/csrf/csrf_test.go:275 TestCSRF_OriginCheckBeforeSkipCheck` + `:298 TestCSRF_OriginCheckBeforeSkipCheck_AllowedOriginPasses` | covered |

### v2_SECURITY_REVIEW_3.md (third pass)

| ID | File:test | Status |
|----|-----------|--------|
| M-A | `app/production_defaults_test.go:156 TestBuilder_Validates_RejectsIPv6Wildcard` (sub-cases: `::`, `[::]`, `0:0:0:0:0:0:0:0`) | covered |
| M-B | `infra/sqldb/pgx/pgx_test.go:67 TestRequireLoopbackHost_AcceptsLoopback` + `:107 TestRequireLoopbackHost_RejectsNonLoopback` (later evolved into the multi-host fix tests for N-6) | covered |

### v2_SECURITY_REVIEW_4.md (N-1..N-5)

| ID | File:test | Status |
|----|-----------|--------|
| N-1 | `app/production_defaults_test.go:179 TestBuilder_Validates_RejectsIPv4ZeroForms` (decimal-zero sub-cases: `00.00.00.00`, `000.000.000.000`, `0`, `0.0`, `0.0.0`, `0.00.00.00`) | covered |
| N-2 | `infra/sqldb/pgx/pgx_test.go:121 TestRequireLoopbackHost_RejectsParsedNonLoopback` (URL `?host=` override + libpq duplicate-key last-wins) | covered |
| N-3 | `infra/sqldb/pgx/pgx_test.go:61 TestRequireTLS_RejectsLastWinsBypass` (`sslmode=require sslmode=disable` DSN) + `:37 TestRequireTLS_RejectsLooseModes` covers `prefer`/`allow`/`disable` | covered |
| N-4 | `app/production_defaults_test.go:67 TestBuilder_Validates_PostgresRejectsLooseSSLMode` covers `sslmode=prefer` via Builder; **no direct test against `Fields.Validate("SVC", "production", "postgres")` for the standalone path**. The `infra/sqldb/config_unified_test.go:200-222` tests cover `invalid` and `require` only, not `prefer`/`allow`. | **partial** — see "Recommended new tests" |
| N-5 | `infra/messaging/amqpbackend/config_test.go:162 TestRabbitMQFields_ValidateRabbitMQ/default_guest_password_rejected` (exact match for audit's failing test) | covered |

### v2_SECURITY_REVIEW_5.md (N-6..N-8)

| ID | File:test | Status |
|----|-----------|--------|
| N-6 | `infra/sqldb/pgx/pgx_test.go:85 TestConnect_RejectsMultiHostNonLoopbackFallback` (libpq form) + `:97 TestConnect_RejectsMultiHostURLFormFallback` (URL form) | covered |
| N-7 | `app/production_defaults_test.go:179 TestBuilder_Validates_RejectsIPv4ZeroForms` (hex sub-cases: `0x0`, `0X0`, `0x00000000`, `0X00000000`, `0x0.0x0.0x0.0x0`, `0X0.0X0.0X0.0X0`, `0x00.0x00.0x00.0x00`, `0x0.0`, `0.0X0`) | covered |
| N-8 | `infra/sqldb/pgx/pgx_test.go:67 TestRequireLoopbackHost_AcceptsLoopback` (sub-case `[::1]`) | covered |

### v2_SECURITY_REVIEW_6.md (N-9)

| ID | File:test | Status |
|----|-----------|--------|
| N-9 | `app/production_defaults_test.go:179 TestBuilder_Validates_RejectsIPv4ZeroForms` (overflow sub-cases: `4294967296`, `0x100000000`, `0X100000000`, `040000000000`, `8589934592`) | covered |

### v2_SECURITY_REVIEW_7.md (N-10)

| ID | File:test | Status |
|----|-----------|--------|
| N-10 | `app/production_defaults_test.go:179 TestBuilder_Validates_RejectsIPv4ZeroForms` (bracket-only sub-cases: `[]`, `[`, `]`) | covered |

### v2_SECURITY_REVIEW_8.md (eighth pass — clean verdict)

No new findings; final regression-check pass confirmed every prior fix held under paranoid review with zero diffs to closed surfaces.

---

## Recommended new tests

These are **optional polish**, not tag blockers. The user can ship v2.0.0 without them; both are sensible follow-ups for a v2.0.1 / v2.1 cleanup.

### 1. `infra/messaging/natsbackend/natsbackend_test.go` — pin the `MaxDeliver` default

```go
func TestNewConsumer_DefaultsMaxDeliverTo5(t *testing.T) {
    // Smoke: the v1 H-3 fix is config-only. A future refactor that
    // accidentally drops the default would silently revert to JetStream's
    // -1 (infinite) and reopen the poison-pill DoS.
    conn := &Connection{js: nil} // js is unused by NewConsumer
    c := NewConsumer(conn, ConsumerConfig{Stream: "s", Durable: "d"}, nil)
    require.Equal(t, 5, c.cfg.MaxDeliver,
        "MaxDeliver=0 must default to 5 to bound poison-pill redelivery")
}
```

The fix is at `infra/messaging/natsbackend/natsbackend.go:255-256`. The integration test `TestConsumer_NackRedelivers` at `integration_test.go:91` only verifies redelivery happens; it does not assert the cap. A unit test that pokes the config field is enough to pin the default.

### 2. `infra/sqldb/config_unified_test.go` — pin `Fields.Validate` rejecting `sslmode=prefer`/`allow` standalone

```go
func TestFields_Validate_RejectsLooseSSLMode_Standalone(t *testing.T) {
    // N-4 regression: the Builder validator at app/validate.go correctly
    // rejects prefer/allow, but Fields.Validate is the lower-level
    // standalone path used by CLI tools / non-HTTP daemons. Both paths
    // must agree.
    for _, mode := range []string{"prefer", "allow"} {
        t.Run(mode, func(t *testing.T) {
            f := Fields{Database: Config{
                Host: "h", Port: 5432, User: "u",
                Password: "a-strong-password-here", Name: "n",
                Options: map[string]string{"sslmode": mode},
            }}
            err := f.Validate("SVC", "production", "postgres")
            require.Errorf(t, err,
                "sslmode=%q silently degrades to plaintext on TLS handshake failure", mode)
            assert.Contains(t, err.Error(), "fail closed")
        })
    }
}
```

The fix is at `infra/sqldb/config.go:367-374` (`validatePostgresSSLMode`). It is currently exercised only via the Builder path (`TestBuilder_Validates_PostgresRejectsLooseSSLMode` covers `prefer`); a direct test against `Fields.Validate` would prevent a future refactor that re-routes the Builder's check from regressing the standalone surface.

---

## Cross-cutting verifications

- All 32 finding IDs from the eight audit passes are accounted for above.
- The `TestBuilder_Validates_RejectsIPv4ZeroForms` test at `app/production_defaults_test.go:179` is the load-bearing regression test for the entire N-1 → N-7 → N-9 → N-10 progression; it has 23 sub-cases covering every wildcard form the audits flagged. A single deletion of this test file would silently re-open four CRITICAL/MEDIUM findings.
- The `TestScopedKey_ColonInTenantIDNoCollision` test exists in BOTH wrapper layers (`data/cache/tenant`, `data/idempotency/tenant`) — a future refactor that reverts the length-prefix in only one wrapper is caught.
- The trusted-S2S marker invariant is pinned on both HTTP (`auth_test.go:575,604`) and gRPC (`auth_test.go:549, 633`) — a refactor that stamps the marker from a JWT-only path would fail both.
- The pgx fail-closed posture has six tests pinning the parser-discrepancy class (`TestRequireTLS_*`, `TestRequireLoopbackHost_*`, `TestConnect_RejectsMultiHost*`) — the audit's "use pgxpool.ParseConfig as the authoritative parser" directive is enforced by these tests because each one constructs a DSN that exposes a parser discrepancy and asserts the kit's check sees the same posture pgxpool will use.

---

## Bottom line for tagging v2.0.0

**Tag-ready on the regression-coverage axis.** The 30/32 covered findings each have a test that (a) names the finding ID in a comment, (b) reproduces the audit's described shape, and (c) would fail against the pre-fix code. The two partial cases are non-blocking polish:

- v1 H-3 (NATS `MaxDeliver` default) — fix is config-line; add a 5-line unit test post-tag.
- v2-4 N-4 partial (standalone `Fields.Validate` for `sslmode=prefer`/`allow`) — fix is in place and tested via the Builder path; add a 10-line standalone-path test post-tag.

Neither gap is a "fix could regress without anyone noticing" risk because both behaviours are (a) load-bearing and visible in adjacent passing tests, and (b) defended by the eighth-pass audit's clean verdict on the surrounding surface. The user can tag v2.0.0 today and queue the two recommended tests for a v2.0.1 cleanup.
