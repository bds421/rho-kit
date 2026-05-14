# rho-kit v2.0.0 fresh clean-slate review - 2026-05-14

Scope: current checkout at `3735503 fix(v2): wave 58 - propagate redact.Error kit convention to app/*`, including the current dirty tree.

Constraint followed: this review did not read or reuse prior `docs/audit/*` review artifacts, previous finding reports, or memory. Evidence below comes from current source, tests, release docs, live command output, and dirty-tree diffs.

Mode: review only. No production fixes were implemented by this review pass; this file is the review artifact.

## Verdict

Not tag-ready while the logging findings below remain unresolved. The mechanical release gates I ran are green, but the current logging surface has two source-confirmed problems: one hides the only fatal startup diagnostic behind `redact.Error`, and one contradicts the redaction contract by logging gRPC impersonation user and client identity values directly.

## Commands Run

- `git status --short`: dirty tree contains `AGENTS.md`, release benchmark docs, and `security/netutil` TLS diagnostic changes.
- `git log --oneline -12`: HEAD is `3735503`.
- `git diff --stat`: 8 dirty files before this report was added.
- `RELEASE_MODE=all make release-plan`: passed; 73 modules, 6 dependency levels.
- `make check-dependency-allowlist`: passed; 59 direct external dependencies approved.
- `make check-dependency-boundaries`: passed; 393 direct module edges checked.
- `make check-operational-readiness`: passed; 73 modules covered.
- `make test`: passed.
- `make build`: passed.
- `make vulncheck`: passed; no vulnerabilities found.
- `make check-publishable`: passed.
- `EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable`: passed.
- `make check-dashboards`: passed; Prometheus alert/rule files checked.
- `make lint`: passed.
- `git diff --check`: passed before this report was added.
- `GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .`: passed with `null`.

Not run in this pass: `make test-race`, `make test-integration`, `make test-cover`, `make bench`, `make bench-baseline`, full release rehearsal.

## Confirmed Findings

### F-001 - `app.Main` redacts the fatal startup error into an unusable type stamp

Severity: High / release blocker

Evidence:
- `app/serviceboot.go:33-35` logs any fatal `runFn` error as `logger.Error("application error", redact.Error(err))` and exits.
- `core/redact/redact.go:57-70` unwraps to the deepest error and renders only `"<redacted error: %T>"`.
- For the common `fmt.Errorf("load config: %w", errors.New("missing DB_HOST"))` shape, operators see only a generic type such as `*errors.errorString`, not the subsystem, field, dependency, or startup phase.
- This path is the top-level service entrypoint; after `os.Exit(1)`, the log line is usually the only diagnostic.

Failure scenario:
A service fails to start because a required env var, migration, listener bind, JWKS URL, DB URL, or provider config is wrong. The process exits and logs only a redacted type stamp. On-call cannot distinguish config typo, permission failure, DNS failure, bind conflict, or dependency outage from the startup log.

Suggested fix:
Do not blanket-redact the top-level startup error. Use a safe startup/ops formatter that preserves kit typed errors, safe reason codes, and configuration field names while still redacting URLs, paths, tokens, SQL, object keys, and user payloads. At minimum, `app.Main` should log `slog.Any("error", err)` only for operator-controlled startup paths, or log both a safe `reason`/`phase` and a redacted error.

Tests to add:
- An `app.Main`-equivalent test using a fake exit/log sink proving a startup error such as `load config: DB_HOST is required` remains actionable.
- A negative test proving secret-bearing strings in the same error path are not logged.

### F-002 - gRPC S2S impersonation logs raw user IDs and client identities

Severity: High

Evidence:
- `grpcx/interceptor/auth.go:612-626` logs `slog.String("user_id", userID)` and `slog.String("client_identity", identity)` on both rejected and accepted gRPC S2S impersonation paths.
- `grpcx/interceptor/auth.go:635-641` logs the same raw values when the impersonation guard panics.
- `docs/RELEASE_NOTES_v2.md:499-500` claims gRPC mTLS impersonation logs avoid copying user IDs and client identities.
- `AGENTS.md:247` explicitly forbids embedding user IDs in Redis/Prometheus labels, and the wider logging/redaction section in the release notes claims runtime identifiers are redacted. The HTTP S2S path does redact the analogous fields with `redact.String` in `httpx/middleware/auth/auth.go:382-396`.

Failure scenario:
A user UUID from `X-User-Id` and an internal service identity from a cert SAN/CN are copied into centralized logs on every gRPC S2S impersonation. That violates the documented redaction posture and creates a persistent identity/audit-data leak. The rejection path is especially sensitive because it logs attacker-controlled or misconfigured attempted impersonations.

Suggested fix:
Mirror HTTP auth: replace the raw `slog.String("user_id", userID)` and `slog.String("client_identity", identity)` attributes with `redact.String(...)` in accepted, rejected, and panic paths. Add tests that assert the raw UUID and SAN/CN do not appear in logs.

Tests to add:
- gRPC interceptor test for accepted mTLS impersonation logging.
- gRPC interceptor test for rejected guard logging.
- gRPC interceptor test for guard panic logging.

### F-003 - Release notes overstate what app lifecycle redaction does

Severity: Low / documentation correctness

Evidence:
- `docs/RELEASE_NOTES_v2.md:492-494` says app lifecycle, tracing, broker close, internal-server shutdown, and top-level application error logs now redact module names and runtime errors.
- `app/module.go:282-286` intentionally logs module names with `slog.String("module", m.Name())`.
- `app/module_test.go:188-196` explicitly asserts the module name stays visible and only the raw error string is redacted.

Failure scenario:
Consumers and future agents reading the release notes believe module names are redacted in lifecycle logs. The code and tests make the opposite contract: module names stay visible for operator correlation. This is not a runtime blocker by itself, but it makes the release evidence untrustworthy.

Suggested fix:
Change the release note to say module names remain visible and runtime errors are redacted, or change the implementation/tests if the intended contract is actually to redact module names.

## Audited Clean From This Pass

- HTTP auth permission checks are fail-closed: `httpx/middleware/auth/auth.go:511-531` rejects absent permission context unless trusted S2S is explicitly marked.
- HTTP scope checks are fail-closed: `httpx/middleware/auth/scope.go:26-42` rejects absent/empty scopes unless trusted S2S is explicitly marked.
- gRPC permission/scope checks are fail-closed: `grpcx/interceptor/auth.go:742-772` requires permissions/scopes unless the trusted S2S marker is present.
- TLS hot reload is wired through public server, default outbound HTTP, AMQP, and NATS paths: `app/httpclient_module.go:45-52`, `app/amqp/amqp.go:151-164`, and `app/nats/nats.go:90-99`.
- TLS reload client verification fails closed without `ServerName`: `security/netutil/tls_reload.go:385-388`.
- Current TLS diagnostic changes preserve path secrecy and add safe reason classification: `security/netutil/tls.go:17-37`, `security/netutil/tls_reload.go:199-201`, and associated tests.
- Benchmark baseline docs now distinguish historical/preliminary files from canonical release-candidate baselines: `AGENTS.md:241`, `docs/release/RC_CHECKLIST_V2.md:23-25`, and `docs/release/benchmarks/v2.0.0/MANIFEST.md:3-21`.
- Release inventory is live-derived as 73 modules / 6 dependency levels from `RELEASE_MODE=all make release-plan`.

## Notes

- This was a fresh pass, not a replay of prior review artifacts. The findings came from current-tree source scans and manual reads.
- The review did not attempt a full line-by-line proof of all 73 modules; it covered release gates, dirty/recent changes, auth fail-closed behavior, TLS/credential rotation, logging/redaction, metric/release hygiene, and bounded I/O/shutdown scan surfaces.
- The repo still needs the heavier pre-tag gates listed above before a v2.0.0 tag.
