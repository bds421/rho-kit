# infra/storage — local, S3, Azure, GCS, SFTP, encryption, storagehttp, manager

## Landed

- ✅ **Local backend parent-dir fsync** — `Save` and `Copy` now `fsyncDir()` the destination after `os.Rename` so a crash post-rename can't leave the file with stale or zero contents (commit `1622196`).

## Open

### [HIGH] S3 backend: no SSE on PutObject or PresignPutURL
**File**: `infra/storage/s3backend/s3.go:194-203` + `s3backend/presign.go:59-67`
**Issue**: `PutObjectInput` constructed without `ServerSideEncryption` (or `SSEKMSKeyId`). For buckets without default-encryption policy, data lands unencrypted at rest. Presigned PUT URLs likewise omit SSE → client direct uploads bypass encryption. Unacceptable for SOC2/PCI.
**Fix**: Add `SSE`/`SSEKMSKeyID` fields to `S3Config`. Set `input.ServerSideEncryption = types.ServerSideEncryptionAes256` (or `Aws_kms`) by default. Sign the same header into presigned PUT URLs and require clients to send it.
**Effort**: M

### [HIGH] `storagehttp.UploadOptions.MaxMemory` is dead config
**File**: `infra/storage/storagehttp/upload.go:31-58,72`
**Issue**: Documented as multipart memory buffer but `ParseAndStore` calls `r.MultipartReader()` which streams parts directly — `MaxMemory` is only honored by `r.ParseMultipartForm(maxMem)`. Field is set, defaulted, never used. Misleading API → unsafe memory bound assumed by callers.
**Fix**: Either drop the field entirely, or call `r.ParseMultipartForm(opts.MaxMemory)` and use the resulting form. Update doc.
**Effort**: S

### [HIGH] `storagehttp.UUIDKeyFunc` loses extension when MIME unknown
**File**: `infra/storage/storagehttp/keyfunc.go:26-50`
**Issue**: When `meta.ContentType` is empty, `application/octet-stream`, or not in mimetype registry, returned key has no extension. `ServeFile` then falls back to `mime.TypeByExtension(path.Ext(filename))` → "" → all such downloads default to `application/octet-stream`. Breaks browser preview.
**Fix**: Fall back to `path.Ext(originalFilename)` when MIME extension lookup yields empty. Sanitize the fallback against attacks (allowlist alphanumeric + dot, length-limit).
**Effort**: S

### [HIGH] Encrypted storage `Put` buffers full 256 MiB plaintext in memory
**File**: `infra/storage/encryption/encryption.go:82-117`
**Issue**: GCM is all-or-nothing AEAD. Implementation reads the full body up to `MaxEncryptableSize` (256 MiB) into RAM before encrypting. Concurrent uploads exhaust memory. No concurrency safeguard.
**Fix**: Add semaphore on concurrent encryption operations sized by `runtime.NumCPU()`. Surface `MaxConcurrentEncryptions` option. Document the per-Put memory cost. Recommend S3 SSE for larger files.
**Effort**: S

### [MEDIUM] sftpbackend 5-second sleep before closing stale conn → FD accumulation on flapping
**File**: `infra/storage/sftpbackend/sftp.go:197-211`
**Issue**: When `connect()` replaces the client, a goroutine sleeps 5s then closes the old connection. With SFTP server flapping every few seconds, multiple cleanups accumulate, each holding 2 FDs (SSH + SFTP) for 5s.
**Fix**: Track an atomic "current cleanup generation"; each goroutine checks before closing — if a newer cleanup ran, close immediately. Or use a single channel-driven cleanup goroutine.

### [MEDIUM] `Manager.Default` fallback fragile if `Unregister` ever added
**File**: `infra/storage/manager.go:93-104`
**Issue**: `order` slice and `backends` map kept in sync only because no `Unregister` exists. If someone adds Unregister without splicing `order`, `Default()` returns nil or panics.
**Fix**: Wrap invariant in an `assert` helper or add a regression test. Or switch `Default()` to scan `backends` keys to be self-healing.

### [LOW] `storagehttp.ParseAndStore`: unbounded skipped-part body discard up to 10 MiB × 10 = 100 MiB
**File**: `infra/storage/storagehttp/upload.go:82-108`
**Fix**: Add a global `maxTotalSkipped` budget (e.g., 16 MiB) across all skipped parts.

### [LOW] `s3backend.URL` hard-codes `s3.<region>.amazonaws.com` (China/GovCloud)
**File**: `infra/storage/s3backend/url.go:36-37`
**Fix**: Build the URL via `aws.Endpoint` resolver, or accept a `URLTemplate` config field.

### [LOW] Encryption: empty plaintext is encrypted (28 bytes for empty payload)
**File**: `infra/storage/encryption/encryption.go:82-117`
**Fix**: Optional — special-case empty plaintext, or document the overhead.

### Migration checklist

- [ ] Phase 2: S3 SSE defaults + presigned PUT enforcement.
- [ ] Phase 2: storagehttp `MaxMemory` — fix or drop.
- [ ] Phase 2: storagehttp `UUIDKeyFunc` extension fallback.
- [ ] Phase 2: encryption Put concurrency cap.
- [ ] Phase 3: sftp generation-based cleanup; Manager.Default invariant; URL templating.
