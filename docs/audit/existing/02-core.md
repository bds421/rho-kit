# core/ — apperror, config, contextutil, validate

Foundation packages. Bugs here ripple everywhere.

## Landed

- ✅ **`validate.RegisterValidation` TOCTOU race** — registration now serialised against `Struct()` via mutex (commit `270c901`).
- ✅ **`config.Load` `_FILE` reads** — switched from `TrimSpace` (which clobbered intentional leading/trailing whitespace) to `TrimRight(s, "\r\n")` (commit `270c901`).

## Open

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

- [ ] Phase 2: split `GetSecret` into `(string, error)` + `MustGetSecret`.
- [ ] Phase 3: `EnvReloader.WithImmediateLoad()`.
- [ ] Phase 3: decide on `apperror.HTTPStatus` removal vs keep.
