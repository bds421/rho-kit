package gcsbackend

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gcsstorage "cloud.google.com/go/storage"
)

func TestNewWithClient_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(nil, Config{Bucket: "b"})
	})
}

func TestNewWithClient_PanicsOnEmptyBucket(t *testing.T) {
	t.Parallel()
	// Pass a non-nil client so the nil-client guard is satisfied and the
	// empty-bucket panic path is the one actually exercised. Mirrors the
	// azurebackend sibling test which uses a stub client + empty container.
	assertPanics(t, func() {
		NewWithClient(&gcsstorage.Client{}, Config{Bucket: ""})
	})
}

func TestNewWithClient_PanicsOnNilOption(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(&gcsstorage.Client{}, Config{Bucket: "b"}, nil)
	})
}

func TestGCSBackend_InvalidReceiverSafety(t *testing.T) {
	t.Parallel()

	var nilBackend *Backend
	if err := nilBackend.Close(); err != nil {
		t.Fatalf("nil backend Close error = %v", err)
	}
	if err := (&Backend{}).Close(); err != nil {
		t.Fatalf("zero backend Close error = %v", err)
	}
}

func TestHealthy_NilAndZeroBackend(t *testing.T) {
	t.Parallel()

	var nilBackend *Backend
	if nilBackend.Healthy() {
		t.Fatal("nil backend Healthy = true, want false")
	}
	if (&Backend{}).Healthy() {
		t.Fatal("zero backend Healthy = true, want false")
	}
}

func TestHealthy_EmptyBucketIsHealthy(t *testing.T) {
	// An empty bucket lists zero objects (iterator.Done) but is reachable,
	// so Healthy must report true — same contract as azurebackend.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"storage#objects"}`))
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)
	if !b.Healthy() {
		t.Fatal("Healthy = false for reachable empty bucket, want true")
	}
}

func TestHealthy_NonEmptyBucketIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"storage#objects","items":[{"name":"a","bucket":"test-bucket"}]}`))
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)
	if !b.Healthy() {
		t.Fatal("Healthy = false for reachable non-empty bucket, want true")
	}
}

func TestHealthy_UnreachableBucketIsUnhealthy(t *testing.T) {
	// A 400 is non-retryable, so the probe fails fast (no backoff loop) and
	// Healthy must report false.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad bucket"}}`))
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)
	if b.Healthy() {
		t.Fatal("Healthy = true for unreachable bucket, want false")
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}
