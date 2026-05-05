# CRITICAL findings (cross-package) — status

Twelve findings flagged in the original audit. As of the Wave 1+2+3 work, ten are closed; two remain open. The two open items both depend on **new** packages (recover middleware, recovery gRPC interceptor) and are tracked there.

| # | Title | Status | Commit / next step |
|---|---|---|---|
| 1 | Vulnerable Go runtime + grpc dep | 🟡 partial | gRPC v1.79.3 done (`56bf04e`); Go 1.26.2+ runtime bump is operator action |
| 2 | Default middleware stack has no panic recovery | 🔴 open | depends on [new/01](new/01-httpx-middleware-recover.md) |
| 3 | `grpcx.NewServer` does not install Recovery interceptors | 🔴 open | depends on [new/02](new/02-grpcx-recovery-default.md) |
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

- ✅ CSRF default secret per-process — Origin allowlist landed (`409cdbb`); shared-secret enforcement still **open** (existing/05).
- 🔴 `clientip` default trusts ALL RFC1918 — still **open** (existing/05).
- ✅ Idempotency `WithTTL(0)` permanent lock — `36cf34b` (panic on non-positive durations).
- ✅ `ComputeCache` WaitGroup race — `36cf34b` (mutex around closed-check + Add).
- ✅ `MemoryCache` default unbounded → OOM — `36cf34b` (default 64 MiB; opt-in unbounded).
- ✅ `retry.Loop` restarts after `nil` error — `270c901` (immediate return on nil).

## Remaining critical work

Two items left, both blocked on new-package creation:

1. **Recover middleware in `stack.Default`** — implement `httpx/middleware/recover` per [new/01](new/01-httpx-middleware-recover.md), then prepend in `stack.Default`.
2. **Recovery interceptors in `grpcx.NewServer`** — implement `grpcx/interceptor/recovery` defaults per [new/02](new/02-grpcx-recovery-default.md), prepend in `NewServer` and `app.NewGRPCModule`.

Both are small once the new packages exist. Bundle with the [new/19-app-production-defaults.md](new/19-app-production-defaults.md) builder switch so consumers get crash-safety by default.
