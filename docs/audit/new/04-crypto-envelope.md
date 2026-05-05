# NEW: crypto/envelope

**Phase**: 4 (Tier‑1 missing primitive)
**Module path**: `github.com/bds421/rho-kit/crypto/envelope`

## Why

`crypto/encrypt.FieldEncryptor` only supports a single static AES key with no rotation path. Real systems need:

- A **DEK** (data encryption key) generated per record.
- A **KEK** (key encryption key) that wraps the DEK and lives in a KMS.
- Key-version metadata in ciphertext so re-keying is online.
- Pluggable KMS providers (AWS KMS, GCP KMS, HashiCorp Vault, static-for-tests).

This package gives that. It complements `crypto/encrypt` rather than replacing it.

## Public API

```go
package envelope

// KEK abstracts a key-encryption-key provider (KMS).
type KEK interface {
    KeyID() string                                                // current/active key version
    Wrap(ctx context.Context, dek []byte) (wrapped []byte, err error)
    Unwrap(ctx context.Context, keyID string, wrapped []byte) (dek []byte, err error)
}

// Encryptor performs envelope encryption: generates a fresh AES-256 DEK,
// wraps it with the KEK, and emits a self-describing blob.
type Encryptor struct{ /* ... */ }

func New(kek KEK, opts ...Option) *Encryptor

// Encrypt returns: header || wrappedDEK || nonce || ciphertext || tag
// where the header carries:
//   - magic bytes
//   - version (uint8)
//   - key id length + key id (so Unwrap knows which KEK version to call)
//   - aad length + aad (optional, bound into AEAD)
func (e *Encryptor) Encrypt(ctx, plaintext, aad []byte) ([]byte, error)

// Decrypt parses the blob, calls KEK.Unwrap with the embedded keyID, and returns plaintext.
func (e *Encryptor) Decrypt(ctx, blob, aad []byte) ([]byte, error)

// Rewrap re-encrypts the DEK under the current KEK version without touching plaintext.
// Use this for online key rotation: read blob, Rewrap, write blob.
func (e *Encryptor) Rewrap(ctx, blob []byte) ([]byte, error)
```

### Subpackages

```
crypto/envelope/kekstatic    -- in-memory KEK for tests + dev
crypto/envelope/kekaws       -- AWS KMS GenerateDataKey + Decrypt
crypto/envelope/kekgcp       -- GCP KMS Encrypt/Decrypt
crypto/envelope/kekvault     -- HashiCorp Vault transit engine
```

Each subpackage is a separate Go module (matches the kit's per-module pattern) so consumers only depend on the cloud SDK they use.

## Integration with `crypto/encrypt`

`FieldEncryptor` stays for the single-key case. Add a thin adapter:

```go
// In crypto/encrypt: a NewWithEnvelope constructor that delegates Encrypt/Decrypt to envelope.Encryptor.
```

This way `gormhooks.FieldEncryptor` consumers can switch encryption strategy without changing the GORM hook.

## Definition of done

- [ ] Core package + tests (round-trip, AAD binding, version mismatch detection).
- [ ] `kekstatic` subpackage for tests.
- [ ] `kekaws` subpackage with `aws-sdk-go-v2/service/kms`.
- [ ] `kekgcp` subpackage with `cloud.google.com/go/kms`.
- [ ] `kekvault` subpackage with `vault/api`.
- [ ] Rewrap roundtrip test (rotate KEK; verify decrypt still works for both versions).
- [ ] Recipe in `docs/ai/security.md`.
