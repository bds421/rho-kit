// Package envelope implements envelope encryption: a fresh AES-256-GCM
// DEK is generated per Encrypt call, wrapped under a KEK (the
// key-encryption-key — typically a KMS-managed master key), and emitted
// alongside the ciphertext as a self-describing blob.
//
// Compared to a single static-key approach (crypto/encrypt), envelope
// encryption gives:
//
//   - Online rotation. Re-key by swapping the KEK; new writes use the
//     new key version, old reads still work via the embedded key-version
//     metadata. [Encryptor.Rewrap] re-keys an existing blob without
//     touching plaintext or AAD.
//   - KMS integration. The KEK is pluggable behind the [KEK] interface.
//     This package ships kekstatic for tests/dev; cloud KMS providers
//     (AWS KMS, GCP KMS, Vault transit) live in their own subpackages
//     so consumers only pull the SDK they use.
//   - Per-record DEKs. A single key compromise reveals only the records
//     written under that DEK, not the entire dataset.
//
// Blob format (network byte order, version 3 — current):
//
//		+--------+----+----+--------+----+-------+----+--------+
//		| magic  | v  | kL | keyID  | wL | wDEK  | n  | ct+tag |
//		|  3B    | 1B | 2B |  …     | 2B |  …    | 12B|   …    |
//		+--------+----+----+--------+----+-------+----+--------+
//
//	  - magic: "ENV" (3 bytes) — quick-reject for non-envelope blobs.
//	  - v: version, currently 3.
//	  - kL + keyID: KEK identifier (string), length-prefixed (uint16 BE).
//	  - wL + wDEK: wrapped DEK bytes, length-prefixed (uint16 BE).
//	  - n: AES-GCM nonce (12 bytes).
//	  - ct+tag: AES-256-GCM(plaintext, AAD := domainSep || varint(len(callerAAD)) || callerAAD).
//
// v2 blobs continue to decrypt: the version byte selects parser and
// AAD layout. v2 used uint8 kL and `callerAAD || domainSep` for the
// body AAD; v3 length-prefixes both keyID (uint16) and caller AAD
// (uvarint) and puts the domain separator first.
//
// AAD binding (v3): the AAD passed to the body GCM is the v3 domain
// separator followed by a varint length-prefixed caller AAD. The wrap
// header (keyID, wDEK) is NOT included in the body AAD — this is what
// makes [Encryptor.Rewrap] work without re-encrypting the plaintext.
// Tampering with the wrap header is detected by the KEK's own AEAD tag
// on the wrapped DEK: an attacker who swaps wDEK either gets rejected
// by the KEK or recovers a wrong DEK that fails the body's GCM-Open.
// Tampering with keyID is detected the same way (unknown keyID
// rejected by KEK, or wrong DEK fails body open).
//
// v3 also closes a collision in the v2 AAD derivation: two callers
// could craft AADs whose concatenated MAC pre-image was identical
// once the v2 suffix was appended. v3's length prefix removes that.
package envelope
