# CRITICAL findings (cross-package) — status

Twelve findings flagged in the original audit. **All closed** as of Wave 1–6.

| # | Title | Status | Commit / next step |
|---|---|---|---|
| 1 | Vulnerable Go runtime + grpc dep | ✅ done | gRPC v1.79.3 (`56bf04e`); Go 1.26.2 toolchain auto-fetched (`5df122f`) |
| 2 | Default middleware stack has no panic recovery | ✅ done | `e96ffdf` (httpx/middleware/recover sub-package; prepended in `stack.Default`) |
| 3 | `grpcx.NewServer` does not install Recovery interceptors | ✅ done | `e96ffdf` (Recovery interceptors prepended by default; `WithoutRecovery` opt-out) |
| 4 | AMQP publisher silently drops unroutable messages | ✅ done | `068eeb5` (mandatory=true + NotifyReturn) |
| 5 | `debughttp` Publish/Consume endpoints have no auth | ✅ done | `068eeb5` (Guard middleware + Authenticator) |
| 6 | Outbox tight retry loop with no backoff | ✅ done | `4b522b3` (next_retry_at + exponential backoff + DeleteFailedBefore) |
| 7 | Local storage doesn't fsync parent directory after rename | ✅ done | `1622196` |
| 8 | Postgres `sslmode` defaults to `disable` | ✅ done | `a8fa6ed` (default `prefer`; `verify-full` with TLS bundle; `Validate` rejects `disable` outside dev) |
| 9 | pgstore idempotency `Unlock` has no owner check (split-brain) | ✅ done | `1f06b5e` (owner_token migration + Store interface reshape) |
| 10 | Redis queue uses one shared `:processing` list across consumers | ✅ done | `f4a0a95` (per-consumer processing list) |
| 11 | Redis queue `LRem`-by-data races + recovery silently drops messages | ✅ done | `f4a0a95` (Lua removeByID + LRange peek + dispatch-failure preserves) |
| 12 | `data/lock` interface and redislock implementation are incompatible | ✅ done | `2408d15` (Locker refit; per-call returned Lock handle) |

## Closely-related HIGH items — status

These were called out in the original CRITICAL doc as operational footguns to bundle with the CRITICAL release:

- ✅ CSRF default secret per-process — Origin allowlist (`409cdbb`); shared-secret panic in non-dev + WithDevSecret opt-in (`7f0efe3`).
- ✅ `clientip` default trusts ALL RFC1918 — defaults tightened to loopback only; `ParseTrustedProxiesStrict` for fail-fast config (`ab4df5c`).
- ✅ Idempotency `WithTTL(0)` permanent lock — middleware panic (`36cf34b`); backends return `ErrInvalidTTL` (`a01fad7`).
- ✅ `ComputeCache` WaitGroup race — `36cf34b` (mutex around closed-check + Add).
- ✅ `ComputeCache` zero TTL contradicts cache contract — `6ba1e7d` (rejected at `ComputeFunc` boundary; divergence documented).
- ✅ `MemoryCache` default unbounded → OOM — `36cf34b` (default 64 MiB; opt-in unbounded).
- ✅ `retry.Loop` restarts after `nil` error — `270c901` (immediate return on nil).
- ✅ `secheaders` HSTS gated on `r.TLS != nil` — `b324d2e` (`WithTrustedProxiesForProto` + `WithForceHSTS`).
- ✅ Timeout buffer cap 10 MiB → 1 MiB default + `WithMaxBufferSize` — `30113f9`.
- ✅ `stack.Default` ships without timeout — Timeout(30s) included; `WithTimeout` / `WithoutTimeout` options — `a0b49e8`.
- ✅ gormmysql TLS registry leak — refcounted + `ReleaseTLS` (`af39f9c`).
- ✅ Nil-dependency constructor cluster — seven sites validated at construction (`6ba1e7d`).
- ✅ Go 1.26.2 toolchain bump — `5df122f`.

## Remaining work

All originally-flagged CRITICAL items are closed. The cross-package "operational footgun" cluster (CSRF secret, clientip default, idempotency TTL, ComputeCache race, MemoryCache cap, retry nil error, secheaders XFP, timeout buffer, stack Timeout, gormmysql TLS, nil-deps, Go 1.26.2) all closed as a side-effect of the same waves.

The `WithProductionDefaults()` follow-up (see [new/19-app-production-defaults.md](new/19-app-production-defaults.md)) was superseded in v2.0.0: production-safe defaults now run unconditionally inside `Builder.Build()`, with explicit `Without*()` opt-outs (`WithoutTLS`, `WithInternalNonLoopback`, `WithoutJWTIssuer`, `WithoutJWTAudience`) for the relaxations operators must acknowledge consciously. There is no longer a per-feature meta switch to call.
