// Package logattr provides standard [log/slog] attribute constructors so
// every kit package logs the same field names ("request_id", "tenant_id",
// "addr", "duration_ms", ...). Importing this keeps log shape uniform
// across services without making every package re-invent the vocabulary.
//
// # Use this package when
//
//   - You are writing a slog.Logger.Info / .Warn / .Error call and want
//     the field names to match what other kit packages emit.
//   - You need a redacted error attribute ([Error]) or a redacted addr
//     attribute ([Addr]) so secrets don't leak into logs.
//
// # Do NOT use this package for
//
//   - Audit logging — this is NOT an audit log. The functions here just
//     build slog.Attr values for ordinary application logs. If you need a
//     tamper-evident, HMAC-chained record of security-relevant actions,
//     use [github.com/bds421/rho-kit/observability/v2/auditlog] instead.
//   - Inventing new fields. Add the constructor here before using a new
//     field name in two or more packages, so log queries stay reliable.
//
// # Why this package exists alongside audit log
//
// "logattr" lives in the same observability/ tree as "auditlog" purely
// because both are observability concerns. They are completely independent:
//
//   - This package: tiny pure functions returning slog.Attr.
//   - observability/auditlog: HMAC-chained event ledger with a Store
//     interface and pluggable persistence.
//
// The package name is "logattr" (log attribute) not "audit" anything. If
// you searched for "audit" and landed here, you want
// [github.com/bds421/rho-kit/observability/v2/auditlog].
package logattr
