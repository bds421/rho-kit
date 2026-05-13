// Package config provides struct-tag-based configuration loading from
// environment variables with validation, defaults, and secret file support.
//
// # Secret file hygiene
//
// Fields tagged `secret:"true"` are read from `<NAME>_FILE` (Docker /
// Kubernetes secret-volume convention) when that env var is set,
// falling back to the plain env var otherwise. The on-disk bytes
// are read into a []byte that is zeroed immediately after the value
// is parsed / copied into the destination field — secrets do not
// linger on the heap as immutable strings (Lens F A.9).
//
// # Secret file error classification
//
// [GetSecret] / [Load] wrap the underlying os errors so consumers
// can errors.Is against the standard fs sentinels:
//
//   - [io/fs.ErrPermission] — secret file is configured but the
//     process cannot read it.
//   - [io/fs.ErrNotExist]   — secret file path points at nothing
//     (typo, missing mount, race during deploy).
//   - [io/fs.ErrInvalid]    — secret file is a directory, a special
//     device, or exceeds the configured size cap.
//
// The file path itself is REDACTED from error messages so
// surfaces (Slack alerts, structured logs) do not leak the
// secret-store layout.
package config
