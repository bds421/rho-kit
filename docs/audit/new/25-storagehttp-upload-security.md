# NEW: infra/storage/storagehttp/uploadsec — upload security helpers

**Phase**: 5 (Tier‑2)
**Module path**: extends `github.com/bds421/rho-kit/infra/storage/storagehttp`

## Why

`storagehttp` accepts uploads but does no content validation beyond size. Real services need:

- **MIME sniffing** — don't trust `Content-Type`, check the actual bytes.
- **Extension allowlists** — restrict to image/document types.
- **Image dimension limits** — reject 100,000×100,000 pixel "decompression bombs".
- **Antivirus scan adapter** — pluggable interface to clamd / VirusTotal / cloud AV APIs.
- **Per-user/per-tenant quota enforcement**.

Each of these is a finding waiting to happen. Ship them as a coherent package.

## Public API

```go
package uploadsec

// Validator is invoked on every upload. Returns nil to allow, or an error
// to reject. Errors are mapped to HTTP 415 / 422 by the upload handler.
type Validator interface {
    Validate(ctx context.Context, body io.Reader, meta storagehttp.Meta) (storagehttp.Meta, error)
}

// AllowMIMETypes returns a Validator that sniffs the first 512 bytes,
// determines the actual MIME type via http.DetectContentType + the kit's
// existing MIME registry, and rejects anything not in the allowlist.
// Sets meta.ContentType to the SNIFFED type, not the client-supplied one.
func AllowMIMETypes(allowed ...string) Validator

// AllowExtensions returns a Validator that rejects files with an extension
// outside the allowlist. Use ALONGSIDE AllowMIMETypes (extensions and MIME
// must agree).
func AllowExtensions(allowed ...string) Validator

// MaxImageDimensions returns a Validator that rejects images exceeding the
// given pixel count. Reads the image header; aborts before decoding the
// full pixel buffer (prevents decompression bombs).
func MaxImageDimensions(maxWidth, maxHeight int) Validator

// AntivirusScan returns a Validator that sends the upload through a
// pluggable AV scanner. The Scanner interface is satisfied by adapters
// for clamd, VirusTotal, AWS GuardDuty Malware Protection, etc.
func AntivirusScan(scanner Scanner) Validator

type Scanner interface {
    Scan(ctx context.Context, r io.Reader) (clean bool, threat string, err error)
}

// Quota returns a Validator that checks/decrements a per-tenant byte quota
// using the Limiter interface (use ratelimit.Limiter for cross-instance).
func Quota(limiter QuotaLimiter, keyFn func(ctx context.Context) string) Validator

type QuotaLimiter interface {
    Reserve(ctx context.Context, key string, bytes int64) (allowed bool, err error)
}
```

### Subpackages for scanners

```
infra/storage/storagehttp/uploadsec/scanner/clamd     -- TCP clamd protocol
infra/storage/storagehttp/uploadsec/scanner/virustotal -- VirusTotal HTTP API
infra/storage/storagehttp/uploadsec/scanner/noop      -- always-clean for tests
```

Each is a separate go module to avoid pulling cloud SDKs into base storagehttp.

## Integration

`storagehttp.UploadOptions` already supports a Validator chain (or should — confirm in the existing-package fix from [existing/12](../existing/12-infra-storage.md)). This package provides ready-made validators.

```go
opts := storagehttp.UploadOptions{
    Validators: []storagehttp.Validator{
        uploadsec.AllowMIMETypes("image/png", "image/jpeg", "application/pdf"),
        uploadsec.AllowExtensions(".png", ".jpg", ".jpeg", ".pdf"),
        uploadsec.MaxImageDimensions(8192, 8192),
        uploadsec.AntivirusScan(clamd.New("tcp://clamd:3310")),
        uploadsec.Quota(redisQuota, tenant.FromContextKey),
    },
}
```

## Definition of done

- [ ] Core package + each Validator above.
- [ ] `clamd` and `noop` scanner subpackages.
- [ ] Tests using known-evil EICAR test signature.
- [ ] Tests for decompression-bomb image rejection.
- [ ] Recipe in `docs/ai/storage.md`.

## Related

- [existing/12-infra-storage.md](../existing/12-infra-storage.md) — `storagehttp.UploadOptions` lifecycle.
- [new/20-multitenant-primitives.md](20-multitenant-primitives.md) — Quota validator works alongside tenant context.
