// Package masking provides helpers for rendering sensitive values
// safely in logs, HTTP responses, and operator-facing UIs.
//
// Use it when a value must remain partially visible — enough context to
// distinguish "configured" from "absent" or to debug across requests —
// but the full value would be a credential leak. For total redaction in
// logs, prefer [core/redact]: redact replaces the value with a length
// stamp, whereas masking keeps a structural prefix.
//
// Key entry points:
//
//   - [MaskURL] / [DecryptAndMaskURL] — strip path, query, userinfo,
//     and fragment from a URL, optionally after decrypting a field
//     stored under a [crypto/encrypt.FieldEncryptor].
//   - [MaskString] — keep a small rune prefix of a string and append
//     "****", refusing to render short strings even partially.
//
// All helpers operate on runes (not bytes) so multi-byte UTF-8 input
// is not split mid-codepoint.
package masking
