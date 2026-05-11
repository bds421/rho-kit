# Storage — File Storage & Serving

Packages: `infra/storage`, `storage/s3backend`, `storage/azurebackend`, `storage/gcsbackend`, `storage/sftpbackend`, `storage/localbackend`, `storage/membackend`, `storage/encryption`, `storage/retry`, `storage/circuitbreaker`, `storage/storagehttp`, `storage/storagehttp/uploadsec`, `storage/storagehttp/uploadsec/clamav`

Snippet status: Go blocks in this recipe are illustrative fragments unless
explicitly introduced as generated or executable code. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## When to Use

Use the `infra/storage` package whenever a service needs to store, retrieve, or serve files. Choose the backend based on your infrastructure. Use `Manager` for multi-disk setups. Compose wrappers (encryption, retry, circuit breaker) for production resilience.

## Backend Decision Tree

| Scenario | Backend |
|---|---|
| AWS / S3-compatible (MinIO, LocalStack) | `s3backend` |
| Azure Blob Storage | `azurebackend` |
| Google Cloud Storage | `gcsbackend` |
| Legacy SFTP server | `sftpbackend` |
| Local development only | `localbackend` |
| Unit tests | `membackend` |

## Quick Start (S3)

```go
cfg, err := s3backend.LoadS3Config("MYAPP", cfg.Environment)
if err != nil { return err }

backend, err := s3backend.New(cfg)
if err != nil { return err }

app.New(...).
    WithStorage(backend).
    Router(func(infra app.Infrastructure) http.Handler {
        // infra.Storage is ready
    })
```

## The Storage Interface

```go
type Storage interface {
    Put(ctx context.Context, key string, r io.Reader, meta ObjectMeta) error
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error)
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
}
```

Optional capabilities via type assertion:
```go
if ps, ok := backend.(storage.PresignedStore); ok {
    url, _ := ps.PresignGetURL(ctx, key, 15*time.Minute) // S3 only
}
```

Shared helpers such as `storage.DeleteMany` and `storage.CopyMany` validate
keys before touching a backend and reject batches above
`storage.MaxBatchKeys`.
`storage.Migrate` streams objects one at a time and caps retained per-key
errors at `storage.MaxMigrationErrors`; the `Failed` counter and progress
callback still report every failure.

## Multi-Disk Manager

```go
s3, _ := s3backend.New(s3Cfg)
local, _ := localbackend.New(localCfg)

app.New(...).
    WithNamedStorage("s3", s3).
    WithNamedStorage("local", local).
    Router(func(infra app.Infrastructure) http.Handler {
        infra.StorageManager.Disk("s3").Put(ctx, key, r, meta)
        infra.StorageManager.Default().Get(ctx, key) // first registered = default
    })
```

## Composition Pattern (Production Stack)

Wrappers compose transparently — each implements `Storage` and forwards optional interfaces:

```go
base, _ := s3backend.New(cfg)
retried := retry.New(base, retry.WithMaxAttempts(3))
breaker := circuitbreaker.New(retried, circuitbreaker.WithThreshold(5))
enc := encryption.New(breaker, encryption.StaticKey(key32))
hooked := storage.WithHooks(enc, storage.Hooks{
    AfterPut:    func(ctx context.Context, key string, meta storage.ObjectMeta) { /* CDN invalidation */ },
    AfterDelete: func(ctx context.Context, key string) { /* cache purge */ },
})

app.New(...).WithStorage(hooked)
```

**Order matters**: retry wraps the base (retries I/O), circuit breaker wraps retry (fails fast when backend is down), encryption wraps circuit breaker (encrypt/decrypt happens in the caller).

Decorators validate keys, list prefixes, and list pagination options before
running hooks, retry policies, or circuit-breaker state transitions. Hook
callbacks receive cloned `ObjectMeta`, so callbacks can observe metadata but
cannot silently mutate the storage operation.

## Upload Validators

Stream-based storage validators run before the backend stores bytes:

```go
storage.AllowedMIMETypes("image/jpeg", "image/png", "image/*") // sniffs first 3072 bytes
storage.MaxFileSize(10 << 20)                                   // 10 MiB
storage.ImageDimensions(100, 100, 4000, 4000)                  // min/max width/height
```

Malware scanning uses `uploadsec` plus the split ClamAV adapter. The
ClamAV storage validator spools to a bounded temp file, scans with
`clamd` before storage, and returns a replay reader only after a clean
verdict:

```go
scanner := clamav.New("127.0.0.1:3310")

validators := []storage.Validator{
    storage.AllowedMIMETypes("image/jpeg", "image/png"),
    scanner.StorageValidator(clamav.WithMaxSpoolBytes(20 << 20)),
    storage.ImageDimensions(100, 100, 4000, 4000),
}
```

Custom validators receive the same `context.Context` passed to `Put` or
`storagehttp.ParseAndStore`, so scanners and other I/O validators must honor
cancellation and deadlines:

```go
validator := storage.Validator(func(ctx context.Context, r io.Reader, meta *storage.ObjectMeta) (io.Reader, error) {
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    default:
        return r, nil
    }
})
```

`errors.Is(err, uploadsec.ErrMalwareDetected)` identifies a positive
malware finding. `errors.Is(err, uploadsec.ErrScannerUnavailable)`
means fail closed and retry later; do not store the file.

Custom storage backends that run configured validators with
`storage.ApplyValidators(ctx, r, &meta, validators)` must pass the caller's
context and defer `storage.CloseValidatedReader(validated)` after a successful
validation chain. Some validators return cleanup-owning replay readers, such as
bounded temp files used by malware scanners, and they must be closed even when
later metadata validation or provider upload steps fail. Do not close a
caller-owned `Put` reader when no validators ran.

## HTTP Upload & Serve

```go
// Upload (streams multipart directly to backend):
result, err := storagehttp.ParseAndStore(ctx, r, backend, storagehttp.UploadOptions{
    FormField:  "file",
    KeyFunc:    storagehttp.UUIDKeyFunc("avatars"), // "avatars/<uuid>.jpg"
    Validators: []storage.Validator{
        storage.AllowedMIMETypes("image/*"),
        scanner.StorageValidator(clamav.WithMaxSpoolBytes(5 << 20)),
    },
    MaxFileSize: 5 << 20,
})
// result.Key, result.ContentType, result.Size
// storage.ErrValidation → 422, other errors → 500

// Serve (conditional GET, ETag, Range support for local files):
err := storagehttp.ServeFile(w, r, backend, key, storagehttp.ServeOptions{
    ContentDisposition: "inline",
    CacheControl:       "public, max-age=3600",
})
// storage.ErrObjectNotFound → 404
```

## Environment Variables

### S3
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_S3_REGION` | Yes | e.g. `eu-central-1` |
| `STORAGE_S3_BUCKET` | Yes | |
| `STORAGE_S3_ENDPOINT` | No | HTTPS endpoint override for MinIO/LocalStack/CDN |
| `STORAGE_S3_URL_TEMPLATE` | No | HTTPS public URL template for `PublicURLer`, supports `{bucket}` and `{region}` |
| `STORAGE_S3_FORCE_PATH_STYLE` | No | Required for MinIO/LocalStack |
| `STORAGE_S3_ALLOW_INSECURE_ENDPOINT` | No | Set `true` only for local `http://` emulators |
| `STORAGE_S3_SSE` | No | Default `AES256`; set `aws:kms` with `STORAGE_S3_SSE_KMS_KEY_ID`, or empty to rely on bucket policy |
| `STORAGE_S3_SSE_KMS_KEY_ID` | Conditional | Required when `STORAGE_S3_SSE=aws:kms` |
| `{PREFIX}_S3_ACCESS_KEY_ID` | Yes | |
| `{PREFIX}_S3_SECRET_ACCESS_KEY` | Yes | Supports `_FILE` suffix |

### Azure
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_AZURE_ACCOUNT_NAME` | Yes | |
| `{PREFIX}_AZURE_ACCOUNT_KEY` | Yes | Supports `_FILE` suffix |
| `STORAGE_AZURE_CONTAINER_NAME` | Yes | |
| `STORAGE_AZURE_ENDPOINT` | No | HTTPS endpoint override for Azurite/sovereign clouds |
| `STORAGE_AZURE_ALLOW_INSECURE_ENDPOINT` | No | Set `true` only for local `http://` emulators |

### GCS
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_GCS_BUCKET` | Yes | |
| `STORAGE_GCS_PROJECT_ID` | Yes | |
| `STORAGE_GCS_CREDENTIALS_FILE` | No | Omit for ADC |
| `STORAGE_GCS_ENDPOINT` | No | HTTPS endpoint override for fake-gcs-server/custom endpoints |
| `STORAGE_GCS_ALLOW_INSECURE_ENDPOINT` | No | Set `true` only for local `http://` emulators |

### SFTP
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_SFTP_HOST` | Yes | |
| `STORAGE_SFTP_PORT` | No | Default `22` |
| `STORAGE_SFTP_USER` | Yes | |
| `{PREFIX}_SFTP_PASSWORD` | Conditional | Mutually exclusive with key |
| `STORAGE_SFTP_KEY_FILE` | Conditional | SSH private key path |
| `STORAGE_SFTP_KNOWN_HOSTS_FILE` | Yes | OpenSSH known_hosts file |
| `STORAGE_SFTP_ROOT_PATH` | No | Default `/` |

SFTP storage refuses symlinked root paths, symlinked path components, and
symlink objects during listing. Use a real directory as `STORAGE_SFTP_ROOT_PATH`
and store only regular object paths under it. Host-key verification is required.

### Local
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_LOCAL_ROOT` | Yes | Base directory |

Local storage rejects symlinked root directories, symlinked path components,
and symlink objects. Treat the root as a private test/development directory, not
a shared user-writable tree.

## Encryption Notes

- `encryption.StaticKey(key)` — 32-byte AES-256-GCM key. Each object gets a unique nonce.
- **256 MiB max** per object (AES-GCM requires full buffer).
- For larger files, use S3 SSE instead.
- `KeyProvider` interface supports AWS KMS or Vault integration.

## Retry Notes

- `Put` only retries if the reader implements `io.Seeker` (e.g. `*bytes.Reader`, `*os.File`).
- Non-seekable readers fail immediately — the stream can't be replayed.

## Anti-Patterns

- **Never** use raw client filenames as storage keys — use `storagehttp.UUIDKeyFunc`.
- **Never** wrap encryption inside retry — encrypt/decrypt should happen at the caller level, not retried.
- **Never** skip validators on user uploads — always validate MIME type and size.
- **Never** treat scanner outages as clean uploads — fail closed on `uploadsec.ErrScannerUnavailable`.
- **Never** use `localbackend` in production multi-instance deployments.

## Testing

```go
// Unit tests: use membackend
backend := membackend.New()

// Compliance suite: verify any backend implementation
storagetest.BackendSuite(t, myBackend)
storagetest.ListerSuite(t, myBackend, myBackend)

// Local backend helper:
backend := storagetest.NewLocalBackend(t) // auto-cleaned via t.TempDir()
```
