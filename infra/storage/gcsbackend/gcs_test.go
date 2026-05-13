package gcsbackend

import (
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
	defer func() {
		_ = recover()
	}()
	assertPanics(t, func() {
		NewWithClient(nil, Config{Bucket: ""})
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

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}
