# NEW: cmd/kit-doctor

**Phase**: 6 (Agent-readiness)
**Module path**: `github.com/bds421/rho-kit/cmd/kit-doctor`

## Why

This audit found ~110 places where the kit ships dangerous defaults or where consumers can configure things insecurely. A programmatic checker — runnable in CI and by agents — is the only way to make sure the audit's recommendations stay enforced as the kit and consuming services evolve.

`kit-doctor` is the programmatic version of this audit. Run it against a service's startup config and it tells you which dangerous defaults are still in use.

## What it checks

Categorized rules, each emitting a finding with severity, file:line, and a fix recommendation.

### Static analysis (AST scan of the consuming service)
- `app.Builder.WithJWT(...)` is called WITHOUT both `WithExpectedIssuer` and `WithExpectedAudience`.
- `app.Builder.WithPostgres(...)` is called and `Config.SSLMode` is empty/`disable` for non-dev environments.
- `idempotency.Middleware(...)` is called without `WithUserExtractor`.
- `infra/storage/s3backend.New(...)` without `WithSSE` or `WithSSEKMSKey`.
- `crypto/encrypt.NewFieldEncryptor(...)` called and `Encrypt` consumers lack the AAD-binding pattern.
- `ratelimit.NewRateLimiter` / `NewKeyedRateLimiter` constructed but never registered with `app.Builder.WithIPRateLimit` or run via lifecycle.
- `httpx.NewServer(...)` called and `WithErrorLog` not set.
- `httpx/middleware/stack.Default(...)` not called (consumer hand-rolling the stack and probably missing recover).
- Direct use of `http.DefaultClient` or `http.DefaultTransport`.
- Direct use of `crypto/encrypt.FieldEncryptor.Encrypt` on values that may collide with the prefix shortcut (until the shortcut is removed).

### Runtime checks (against a `--config-file` or env)
- `LOG_LEVEL=debug` in production.
- `OTEL_SAMPLE_RATE=1` (or unset → defaulting to 1) in production.
- `JWKS_URL` resolves to a private IP without `WithAllowPrivateIPs`.
- mTLS env vars missing while `RequireS2SAuth` is in the chain.
- `IDEMPOTENCY_STORE=memory` in production.

### Output

Formatted as a checklist:

```
✗ CRITICAL: app.WithJWT called without WithExpectedAudience
  at internal/app/wire.go:42
  fix: chain .WithExpectedAudience("https://my-service.example.com")

✗ HIGH: WithPostgres without sslmode (production env)
  at internal/app/config.go:18
  fix: set DB_SSL_MODE=require, or add Config.SSLMode = "require"

✓ recover middleware installed (via stack.Default)
✓ idempotency.Middleware uses WithUserExtractor
```

Exit code 0 if no findings ≥ HIGH; 1 if CRITICAL/HIGH present; 2 on tool error.

## Implementation notes

- AST scan via `go/ast` + `golang.org/x/tools/go/packages`.
- Rules in `cmd/kit-doctor/rules/*.go`, each implementing a small interface so adding a rule is one file.
- Per-rule unit tests with golden expected output.

## Definition of done

- [ ] CLI binary that takes a service's package path.
- [ ] At least the rules above.
- [ ] CI integration sample (`go run ./cmd/kit-doctor ./...` in a workflow).
- [ ] Exit codes documented.
- [ ] Doc explaining how to write a new rule.
