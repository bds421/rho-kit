# data/stream — Redis Streams consumer

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `7f9c49f`)

- ✅ **Per-stream Consumer + single-use enforcement** — Consumer is now single-use; `Consume` panics on a second call. `StartConsumers` clones the prototype Consumer per binding via `cloneForStream()` and gives each goroutine a fresh UUID. Eliminates the cross-stream `DELCONSUMER` tangle the audit called out.
- ✅ **Dead `goredis.Nil` branch dropped** — with `Block:-1` the server returns an empty result, not `goredis.Nil`; the empty-len check below already handles it. The `readNew` branch (which uses a positive Block) keeps its `goredis.Nil` check because it's reachable.

### Migration checklist

- [x] Phase 2: per-stream consumer ID. ✅ `7f9c49f`
- [x] Phase 3: drop dead `errors.Is(err, goredis.Nil)` branch in `processPending`. ✅ `7f9c49f`
