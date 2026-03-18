// Package encrypt provides AES-256-GCM encryption helpers at two levels:
//
//   - Byte-level: [NewGCM], [SealBytes], [OpenBytes] for raw encryption.
//   - Field-level: [FieldEncryptor] for database column encryption with
//     base64 encoding and version-prefixed format.
package encrypt
