// Package encrypt provides AES-256-GCM encryption helpers at two levels:
//
//   - Byte-level: [NewGCM], [EncryptBytes], [DecryptBytes] for raw encryption.
//   - Field-level: [FieldEncryptor] for database column encryption with
//     base64 encoding and version-prefixed format.
//
// # AES-256-GCM random-IV ceiling (read before deploying)
//
// AES-256-GCM uses a 96-bit random IV. The probability of an IV
// collision under birthday-bound analysis is approximately 2^-32
// after 2^32 encryptions performed with the same key. A collision
// catastrophically breaks confidentiality and integrity for the
// affected records, so deployments must stay well below that ceiling.
//
// The kit recommendation, anchored at a NIST-style 1-in-a-billion
// collision-probability target (NIST SP 800-38D §8.3):
//
//   - Up to ~10^9 encryptions per key per year: a single static key
//     via [FieldEncryptor] is appropriate. Track usage with
//     [FieldEncryptor.OpsCount] / [FieldEncryptor.RegisterMetrics].
//   - Above ~10^9 encryptions per key per year: switch to envelope
//     encryption (`crypto/envelope`) with per-DEK rotation. The
//     envelope flow mints a fresh DEK per row (or per session) so no
//     single DEK ever encrypts more than a handful of plaintexts;
//     the 2^32 ceiling effectively never matters.
//
// Operators can observe the per-key encrypt count via
// [FieldEncryptor.OpsCount] (in-process) or by wiring
// [FieldEncryptor.RegisterMetrics] into Prometheus / OpenTelemetry —
// the example in [FieldEncryptor.RegisterMetrics] uses
// `field_encryptor_ops_total{key_id, operation}` as the canonical
// metric shape.
package encrypt
