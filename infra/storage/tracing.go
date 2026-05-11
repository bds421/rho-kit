package storage

import "github.com/bds421/rho-kit/core/v2/redact"

// SpanErrorDescription returns a status description safe for tracing spans.
//
// Backend errors can include object keys, bucket names, request URLs, endpoint
// details, or SDK payload fragments. Preserve the concrete error type for
// triage while keeping runtime values out of exported telemetry.
func SpanErrorDescription(err error) string {
	return redact.ErrorValue(err)
}
