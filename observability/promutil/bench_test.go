package promutil

import "testing"

// stringSink + errSink prevent the compiler from inlining the
// benchmarked call to nothing. Required for any benchmark whose
// callee has no observable side effects.
var (
	stringSink string
	errSink    error
)

// BenchmarkOpaqueLabelValue pins the cost of the kit's hashed-label
// projection. This helper is called on every metric emission that
// uses a high-cardinality input (per-tenant exchange names, per-
// route routing keys, per-channel class names), so a regression
// inflates cost across every hot path that emits Prometheus
// labels.
func BenchmarkOpaqueLabelValue(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stringSink = OpaqueLabelValue("exchange", "tenant-acme-orders")
	}
}

// BenchmarkValidateStaticLabelValue pins the cost of the kit's
// static-label validator. Called at construction time when
// services register collectors with bounded-cardinality labels.
func BenchmarkValidateStaticLabelValue(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		errSink = ValidateStaticLabelValue("election", "billing")
	}
}
