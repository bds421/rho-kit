# NEW: infra/leaderelection

**Phase**: 5 (Tier‑2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/infra/leaderelection`

## Why

Cron jobs that should run "once across the cluster" need leader election. Today every consumer reinvents this — most poorly. The kit should ship a stable primitive and Builder integration that gates Cron on leadership.

## Public API

```go
package leaderelection

// Elector is the interface every backend implements.
type Elector interface {
    // Run blocks while continuously trying to acquire and hold leadership.
    // OnAcquired is called when this instance becomes leader; OnLost is called
    // when leadership is lost (renewal failure, eviction, ctx cancel).
    Run(ctx context.Context, callbacks Callbacks) error

    // IsLeader is a fast, lock-free leader check (eventually consistent).
    IsLeader() bool
}

type Callbacks struct {
    OnAcquired func(ctx context.Context)
    OnLost     func()
}
```

### Subpackages

```
infra/leaderelection/k8slease   -- coordination.k8s.io/v1 Lease
infra/leaderelection/pgadvisory -- Postgres advisory lock (uses data/lock/pgadvisory)
infra/leaderelection/redislock  -- Redis (uses data/lock/redislock)
infra/leaderelection/etcd       -- etcd lease
```

`pgadvisory` and `redislock` are wrappers around the existing lock packages with a renew loop and callback wiring.

### Builder integration

```go
// app.Builder gains:
func (b *Builder) WithLeaderElection(e leaderelection.Elector) *Builder

// Cron jobs registered via WithCron run only on the leader. Non-leader
// instances still start the scheduler but jobs are no-ops.
```

## Definition of done

- [x] Top-level `Elector` interface and `Callbacks`. ✅ `7253ecb`
- [x] `pgadvisory` subpackage. ✅ `7253ecb`
- [x] `redislock` subpackage with renew-loop + lost-lock detection. ✅ this PR
- [ ] `k8slease` subpackage (deferred — requires k8s API stack).
- [ ] `etcd` subpackage (deferred — requires etcd client SDK).
- [ ] Builder `WithLeaderElection` (deferred — primitives ship first; Builder integration is a separate audit item).
- [ ] Cron integration: jobs check `IsLeader()` before running (deferred with Builder integration above).
- [x] Tests: leadership transfers when leader's ctx is cancelled or renewal fails. ✅
- [ ] Recipe in `docs/ai/utilities.md` (deferred to docs sweep).
