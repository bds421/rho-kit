package redact

import (
	"errors"
	"testing"
)

// errSink prevents the compiler from optimising the WrapError call
// away. Without an observed result the optimiser proves the call
// has no side effects and inlines it to nothing, producing
// 0.2ns/op numbers that are not meaningful.
var errSink error

// BenchmarkWrapError pins the cost of the kit's redact.WrapError
// call. Every kit package wraps every cross-boundary error through
// this helper, so a regression here amplifies across the whole
// service. The regression gate (tools/check-bench-regression)
// fails CI when ns/op exceeds the baseline by more than the
// configured tolerance.
func BenchmarkWrapError(b *testing.B) {
	inner := errors.New("downstream connection refused: dial tcp 10.0.0.5:5432")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		errSink = WrapError("query: users by tenant", inner)
	}
}

// BenchmarkWrapError_NilError covers the fast-path where the inner
// error is nil — WrapError must return nil cheaply without
// allocating a wrapper.
func BenchmarkWrapError_NilError(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		errSink = WrapError("query: users by tenant", nil)
	}
}
