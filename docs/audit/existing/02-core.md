# core/ — apperror, config, contextutil, validate

Foundation packages. Bugs here ripple everywhere.

## Landed

- ✅ **`validate.RegisterValidation` TOCTOU race** — registration now serialised against `Struct()` via mutex (commit `270c901`).
- ✅ **`config.Load` `_FILE` reads** — switched from `TrimSpace` (which clobbered intentional leading/trailing whitespace) to `TrimRight(s, "\r\n")` (commit `270c901`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `d57a16a`)

- ✅ **`config.GetSecret(string, error) + MustGetSecret`** — error-returning variant added; existing call sites migrated to MustGetSecret via codemod.
- ✅ **`EnvReloader.WithImmediateLoad`** — option triggers an initial Load before the SIGHUP-listening loop starts.
- ✅ **`config.Load` rejects explicit empty for `required`** — `MY_SECRET=` no longer silently falls through to a default.
- ✅ **`contextutil.NewSecureID() (string, error)`** — returns an error when `crypto/rand` fails instead of falling back to the time + counter mode.

`apperror.HTTPStatus` decision deferred to v2 (cross-module migration; tracked separately).

### Migration checklist

- [x] Phase 2: split `GetSecret` into `(string, error)` + `MustGetSecret`.
- [x] Phase 3: `EnvReloader.WithImmediateLoad()`.
- [ ] v2: decide on `apperror.HTTPStatus` removal vs keep (deferred — cross-module impact).
