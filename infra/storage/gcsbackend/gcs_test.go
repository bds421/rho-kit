package gcsbackend

import (
	"testing"

	gcsstorage "cloud.google.com/go/storage"
)

func TestNewWithClient_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(nil, GCSConfig{Bucket: "b"})
	})
}

func TestNewWithClient_PanicsOnEmptyBucket(t *testing.T) {
	t.Parallel()
	defer func() {
		_ = recover()
	}()
	assertPanics(t, func() {
		NewWithClient(nil, GCSConfig{Bucket: ""})
	})
}

func TestNewWithClient_PanicsOnNilOption(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(&gcsstorage.Client{}, GCSConfig{Bucket: "b"}, nil)
	})
}

func TestGCSBackend_InvalidReceiverSafety(t *testing.T) {
	t.Parallel()

	var nilBackend *GCSBackend
	if err := nilBackend.Close(); err != nil {
		t.Fatalf("nil backend Close error = %v", err)
	}
	if err := (&GCSBackend{}).Close(); err != nil {
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
