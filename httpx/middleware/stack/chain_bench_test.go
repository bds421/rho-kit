package stack

import (
	"net/http"
	"testing"
)

var benchChainHeader http.Header

func BenchmarkChainThen(b *testing.B) {
	chain := NewChain(
		benchHeaderMiddleware("X-Bench-A", "a"),
		benchHeaderMiddleware("X-Bench-B", "b"),
		benchHeaderMiddleware("X-Bench-C", "c"),
	)
	base := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	var handler http.Handler
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		handler = chain.Then(base)
	}
	if handler == nil {
		b.Fatal("nil handler")
	}
}

func BenchmarkChainServeHTTP(b *testing.B) {
	chain := NewChain(
		benchHeaderMiddleware("X-Bench-A", "a"),
		benchHeaderMiddleware("X-Bench-B", "b"),
		benchHeaderMiddleware("X-Bench-C", "c"),
	)
	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := mustBenchmarkRequest(b)
	rw := &benchResponseWriter{header: make(http.Header)}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rw.reset()
		handler.ServeHTTP(rw, req)
		if rw.status != http.StatusNoContent {
			b.Fatalf("status = %d", rw.status)
		}
	}
	benchChainHeader = rw.header
}

func benchHeaderMiddleware(key, value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(key, value)
			next.ServeHTTP(w, r)
		})
	}
}

func mustBenchmarkRequest(b *testing.B) *http.Request {
	b.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://service.example.test/orders", nil)
	if err != nil {
		b.Fatal(err)
	}
	return req
}

type benchResponseWriter struct {
	header http.Header
	status int
}

func (w *benchResponseWriter) Header() http.Header { return w.header }

func (w *benchResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

func (w *benchResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *benchResponseWriter) reset() {
	for k := range w.header {
		delete(w.header, k)
	}
	w.status = 0
}
