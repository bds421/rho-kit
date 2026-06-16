# AGENTS.md — `crypto/envelope`

## When to use this package

- The service stores or transmits data that must be encrypted at rest with key rotation support (database columns, S3 objects, file storage).
- Envelope encryption is the right pattern: a per-blob DEK (AES-256-GCM), wrapped with a KEK that lives in an external KMS.

## When to use something else

- **Symmetric encryption with a hand-managed key:** out of scope — use `golang.org/x/crypto` directly. The kit's envelope contract assumes KMS-wrapped KEKs.
- **TLS / wire encryption:** stdlib `crypto/tls`. Envelope encryption is for data at rest, not in flight.
- **Hashing / MAC:** `golang.org/x/crypto/blake2b` or stdlib `crypto/hmac`.

## Key APIs

- `envelope.NewEncryptor(kek)` — binds a `KEK` for the lifetime of the returned `*Encryptor`. The KEK is fixed at construction, not passed per call.
- `(*Encryptor).Encrypt(ctx, plaintext, aad)` — generates a fresh DEK, encrypts plaintext, wraps the DEK with the bound KEK. Returns a length-prefixed v3 blob.
- `(*Encryptor).Decrypt(ctx, blob, aad)` — parses the blob, unwraps the DEK, verifies AAD, and decrypts.
- `(*Encryptor).Rewrap(ctx, blob)` — re-wraps the embedded DEK under the current KEK version for online rotation without touching the plaintext (no AAD needed).
- `KEK` interface — implemented by the KMS-specific sub-packages.

## KMS adapters

- `crypto/envelope/awskms` — AWS KMS.
- `crypto/envelope/azurekeyvault` — Azure Key Vault keys API.
- `crypto/envelope/gcpkms` — Google Cloud KMS. Borrows the CRC32C integrity check from the GCP SDK.
- `crypto/envelope/vaulttransit` — HashiCorp Vault Transit secrets engine.

Each adapter is a separate go.mod so the heavy SDK only lands in services that use it.

## Common mistakes

- **Reusing AAD across blobs** — AAD is bound to the ciphertext; mixing AADs is a confused-deputy class of bug. Use a stable, blob-specific AAD (e.g. `"users/v1/" + userID`).
- **Storing the AAD inside the blob** — the v3 length-prefixed format stores ciphertext + wrapped DEK; AAD is supplied at decrypt time. If you can't reconstruct the AAD without the blob, you've defeated the binding.
- **Downgrading a reader to pre-v3 envelope format** — v3 blobs are NOT readable by older code. The migration path is forward-only.
- **Not pinning the wrapping key version (where the adapter supports it)** — by default an adapter wraps with the key's current primary version. To pin wrapping to a specific version, set the adapter's `Config.KeyVersion` (e.g. `azurekeyvault.Config.KeyVersion`, `vaulttransit.Config.KeyVersion`); for `gcpkms` the version is encoded in the key resource path. Unwrapping always uses the keyID embedded in the blob.

## Observability

- Metrics: KMS-side latencies + outcomes per adapter. Each adapter exposes its own metric set.
- No envelope-level spans yet (could be a v2.x follow-up; per-encrypt span overhead would be high).
