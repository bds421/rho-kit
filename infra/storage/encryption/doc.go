// Package encryption provides streaming client-side encryption for
// [storage.Storage] backends.
//
// Objects are sealed with Tink's AES-GCM-HKDF streaming AEAD, an instance of
// the STREAM construction for online authenticated encryption. A random
// salt in a 40-byte header derives a fresh AES-256 key per object from the
// main key with HKDF-SHA256, with the storage key as the HKDF info. The
// plaintext is cut into [SegmentSize] segments, each sealed with
// AES-256-GCM under a nonce of a random prefix, the segment's number and a
// flag that marks the last one. Put encrypts while the backend reads and
// Get decrypts while the caller reads, so each holds about two segments of
// memory however large the object is.
//
// Every segment is authenticated before its plaintext is returned; one
// changed, moved, dropped or appended segment, a cut-off end, or an object
// moved to another storage key fails with [ErrInvalidCiphertext]. Get
// authenticates the first segment before it returns, so a wrong key fails
// there; damage further on surfaces as a Read error after the segments
// before it, and io.EOF only follows an authenticated last segment.
//
// The main key is provided by a [KeyProvider] so callers can plug in AWS
// KMS, HashiCorp Vault, or a static key.
// [WithoutKey] builds a store without one that deletes and lists objects
// and refuses to read or write them.
//
// Usage:
//
//	key := encryption.StaticKey(myKey) // 32-byte main key
//	enc := encryption.New(backend, key)
//	enc.Put(ctx, "secret.txt", reader, meta) // encrypted at rest
//	rc, meta, _ := enc.Get(ctx, "secret.txt") // decrypted on read
//
//	purge := encryption.WithoutKey(backend) // Delete and List, no Put or Get
package encryption
