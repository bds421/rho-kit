// Package encryption provides client-side encryption for [storage.Storage]
// backends using AES-256-GCM (authenticated encryption with associated data).
//
// Each object is encrypted with a unique random nonce prepended to the
// ciphertext. The encryption key is provided by a [KeyProvider] interface
// so callers can plug in AWS KMS, HashiCorp Vault, or a static key.
//
// Usage:
//
//	key := encryption.StaticKey(myKey) // 32-byte AES-256 key
//	enc := encryption.New(backend, key)
//	enc.Put(ctx, "secret.txt", reader, meta) // encrypted at rest
//	rc, meta, _ := enc.Get(ctx, "secret.txt") // decrypted on read
package encryption
