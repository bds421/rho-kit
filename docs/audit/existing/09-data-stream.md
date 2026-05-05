# data/stream — Redis Streams consumer

### [HIGH] Consumer reuses one consumer ID across all bound streams
**File**: `data/stream/redisstream/consumer.go:264-285` + `data/stream/redisstream/start.go:42-60`
**Issue**: `NewConsumer` generates one UUID stored on the struct. `StartConsumers` runs the same Consumer across many `Binding`s in parallel goroutines. Every stream sees XREADGROUP with the same consumer name (fine for delivery), but `removeConsumer` (deferred per `consumeOnce`) deletes the consumer from one stream while other goroutines may still be live on others. After backoff restart on stream A, the active consumer on stream B is removed via `XGROUP DELCONSUMER` — pending messages on B are then unowned and only recoverable via the 5-min `claimMinIdle` window.
**Fix**: Either bind one Consumer per stream (panic if `Consume` is called for >1 stream), or generate a per-stream consumer ID inside `consumeOnce` and pass it through.
**Effort**: M
**Phase**: 2

### [LOW] `processPending` `errors.Is(err, goredis.Nil)` branch is dead code with `Block: -1`
**File**: `data/stream/redisstream/consumer.go:382-413`
**Issue**: With `Block: -1`, `XREADGROUP` returns empty result, not `redis.Nil`. The empty-len check handles it correctly. The `errors.Is(err, goredis.Nil)` branch is dead and could mask a real semantic change in go-redis.
**Fix**: Drop the branch and rely on the empty-len check, or pin go-redis behavior with a comment.

### Migration checklist

- [ ] Phase 2: per-stream consumer ID (or panic on multi-stream Consume).
- [ ] Phase 3: drop dead `errors.Is(err, goredis.Nil)` branch.
