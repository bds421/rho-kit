# core/ — apperror, config, contextutil, validate

Foundation packages. Bugs here ripple everywhere.

### [HIGH] `validate.RegisterValidation` TOCTOU race with `Struct()`
**File**: `core/validate/validate.go:72-77`
**Issue**: Checks `frozen.Load()` then calls `get().RegisterValidation(...)` non-atomically. A concurrent goroutine can call `Struct()` between the two operations: it does `frozen.CompareAndSwap(false, true)` then forwards to the underlying validator — which is **not concurrency-safe between Register and Struct**. The check claims to prevent this but doesn't actually serialize the operations.
**Fix**: Wrap both `RegisterValidation` and the read+forward in `Struct` with a `sync.Mutex`. Or move registration into a single-shot init-only API that does not need the runtime check.
**Effort**: S
**Migration**: None — internal change.

### [MEDIUM] `config.Load` `_FILE` reads use `TrimSpace` (strips meaningful whitespace)
**File**: `core/config/load.go:192`
**Issue**: `strings.TrimSpace` removes ALL leading/trailing whitespace. Most secret-injection tools only add a single trailing `\n`. A secret that legitimately ends in a space gets silently mangled.
**Fix**: Use `bytes.TrimRight(data, "\r\n")`.
**Effort**: S

### [MEDIUM] `config.GetSecret` panics on unreadable secret file
**File**: `core/config/envutil.go:35`
**Issue**: Mixing panic-getter and error-Load conventions in the same package. Caller can't decide between fail-startup vs log-and-continue.
**Fix**: Add `(string, error)` variant; rename current to `MustGetSecret`.
**Effort**: S
**Migration**: Existing call sites continue to work via `MustGetSecret`; add `GetSecret` as the new error-returning variant.

### [MEDIUM] `EnvReloader.Start` doesn't do an initial Load
**File**: `core/config/watcher.go:218-243`
**Issue**: Reloads only on SIGHUP. The Watchable holds whatever `initial` value was passed at construction; if the env changed between construction and Start, that change is invisible until the first SIGHUP.
**Fix**: Add a `WithImmediateLoad()` option (or default true and add `WithoutImmediateLoad()`).
**Effort**: S

### [MEDIUM] `config.Load` `required` doesn't detect explicit empty env
**File**: `core/config/load.go:148-153`
**Issue**: `MY_SECRET=` (set but empty) silently falls through to default. There is no way to say "must be non-empty even if a default exists".
**Fix**: Treat explicitly-set empty as missing for `required` validation; document the change.
**Effort**: S

### [LOW] `apperror.HTTPStatus` deprecated but still exported, drops `RetryAfter`
**File**: `core/apperror/http.go:8-41`
**Issue**: Marked deprecated but still in API surface; replacement is in `httpx` (cross-module). Doesn't surface `RateLimitError.RetryAfter`.
**Fix**: Either keep until v2 or remove and bump major. Either way, the new `httpx.HTTPStatus` should set the `Retry-After` header from `RateLimitError`.

### [LOW] `contextutil.NewID` UUIDv7 fallback isn't random
**File**: `core/contextutil/generate.go:18-38`
**Issue**: When `crypto/rand` fails, the fallback uses time + atomic counter. Two processes restarting in the same nanosecond collide. The doc says "sufficient for tracing" — fine, but expose `NewSecureID()` that returns an error rather than falling back.
**Fix**: Add `NewSecureID() (string, error)`.

### Migration checklist

- [ ] Phase 1: fix `validate` race; fix `_FILE` TrimSpace.
- [ ] Phase 2: split `GetSecret` into `(string, error)` + `MustGetSecret`.
- [ ] Phase 3: `EnvReloader.WithImmediateLoad()`.
- [ ] Phase 3: decide on `apperror.HTTPStatus` removal vs keep.
