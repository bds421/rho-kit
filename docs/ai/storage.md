# Storage — File Storage & Serving

Packages: `infra/storage`, `storage/s3backend`, `storage/azurebackend`, `storage/gcsbackend`, `storage/sftpbackend`, `storage/localbackend`, `storage/membackend`, `storage/encryption`, `storage/retry`, `storage/circuitbreaker`, `storage/storagehttp`

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

## Upload Validators

Stream-based validators — never buffer the full file:

```go
storage.AllowedMIMETypes("image/jpeg", "image/png", "image/*") // sniffs first 3072 bytes
storage.MaxFileSize(10 << 20)                                   // 10 MiB
storage.ImageDimensions(100, 100, 4000, 4000)                  // min/max width/height
```

## HTTP Upload & Serve

```go
// Upload (streams multipart directly to backend):
result, err := storagehttp.ParseAndStore(ctx, r, backend, storagehttp.UploadOptions{
    FormField:  "file",
    KeyFunc:    storagehttp.UUIDKeyFunc("avatars"), // "avatars/<uuid>.jpg"
    Validators: []storage.Validator{
        storage.AllowedMIMETypes("image/*"),
        storage.MaxFileSize(5 << 20),
    },
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
| `STORAGE_S3_ENDPOINT` | No | For MinIO/LocalStack |
| `STORAGE_S3_FORCE_PATH_STYLE` | No | Required for MinIO/LocalStack |
| `{PREFIX}_S3_ACCESS_KEY_ID` | Yes | |
| `{PREFIX}_S3_SECRET_ACCESS_KEY` | Yes | Supports `_FILE` suffix |

### Azure
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_AZURE_ACCOUNT_NAME` | Yes | |
| `{PREFIX}_AZURE_ACCOUNT_KEY` | Yes | Supports `_FILE` suffix |
| `STORAGE_AZURE_CONTAINER_NAME` | Yes | |
| `STORAGE_AZURE_ENDPOINT` | No | For Azurite |

### GCS
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_GCS_BUCKET` | Yes | |
| `STORAGE_GCS_PROJECT_ID` | Yes | |
| `STORAGE_GCS_CREDENTIALS_FILE` | No | Omit for ADC |

### SFTP
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_SFTP_HOST` | Yes | |
| `STORAGE_SFTP_PORT` | No | Default `22` |
| `STORAGE_SFTP_USER` | Yes | |
| `{PREFIX}_SFTP_PASSWORD` | Conditional | Mutually exclusive with key |
| `STORAGE_SFTP_KEY_FILE` | Conditional | SSH private key path |
| `STORAGE_SFTP_ROOT_PATH` | No | Default `/` |

### Local
| Variable | Required | Notes |
|---|---|---|
| `STORAGE_LOCAL_ROOT` | Yes | Base directory |

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
