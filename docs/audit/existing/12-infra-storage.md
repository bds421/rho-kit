# infra/storage — local, S3, Azure, GCS, SFTP, encryption, storagehttp, manager

## Landed

- ✅ **Local backend parent-dir fsync** — `Save` and `Copy` now `fsyncDir()` the destination after `os.Rename` so a crash post-rename can't leave the file with stale or zero contents (commit `1622196`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `cdf0be3`)

- ✅ **S3 SSE defaults** — `S3Config.SSE` defaults to `AES256`; `aws:kms` requires `SSEKMSKeyID` (validated at construction). `applySSE(input, cfg)` is shared between `Put` and presigned PUT so client direct uploads carry the same headers.
- ✅ **`storagehttp.MaxMemory` deprecated** — replaced with `MaxTotalSkippedBytes` (default 16 MiB) which actually bounds discard size; old field still in struct with a deprecation note + lint guard.
- ✅ **`UUIDKeyFunc` extension fallback** — `extensionFromFilename` allowlists alphanumeric extensions ≤ 8 chars; falls back to filename ext only when content type yields nothing; rejects `..`, slashes, query strings.
- ✅ **`encryption.WithMaxConcurrentEncryptions(n)`** — `putSem` (default `runtime.NumCPU()`) bounds concurrent in-memory encryptions; documented per-Put memory cost in package doc.
- ✅ **sftp generation cleanup** — `cleanupGen atomic.Uint64`; cleanup goroutines short-circuit when a newer generation is set; idle FDs no longer accumulate when the SFTP server flaps. Followup polish: poll loop now uses `time.NewTicker(200ms)` + `time.NewTimer(5s)` deadline rather than `time.Sleep` (no drift, no extra goroutine state).
- ✅ **`Manager.Default` invariant** — `Default()` panics on order/backends drift instead of returning nil; backed by a regression test.
- ✅ **S3 URL templating** — `URLTemplate` config field with `{bucket}` / `{region}` placeholders so China / GovCloud / R2 endpoints work.

### Migration checklist

- [x] Phase 2: S3 SSE defaults + presigned PUT enforcement. ✅ `cdf0be3`
- [x] Phase 2: storagehttp `MaxMemory` — fix or drop. ✅ `cdf0be3`
- [x] Phase 2: storagehttp `UUIDKeyFunc` extension fallback. ✅ `cdf0be3`
- [x] Phase 2: encryption Put concurrency cap. ✅ `cdf0be3`
- [x] Phase 3: sftp generation-based cleanup; Manager.Default invariant; URL templating. ✅ `cdf0be3`
