// Package encrypt is AES-256-GCM over bytes: [NewGCM] builds the [AEAD]
// from a 32-byte key, [EncryptBytesAAD] and [DecryptBytesAAD] seal and
// open "iv ‖ ciphertext ‖ tag" bound to associated data.
//
// It is the primitive the kit's stores build on (infra/storage/encryption
// encrypts objects with it). A value kept at rest, such as a credential
// or token in a database column, is sealed with crypto/envelope instead:
// [crypto/envelope.Box] binds each value to its purpose and record and
// its KEK rotates the key without losing what it sealed. A random 96-bit
// IV collides with a probability near 2^-32 after 2^32 encryptions under
// one key (NIST SP 800-38D §8.3); envelope encryption mints a key per
// value, so that ceiling is never reached there.
package encrypt
