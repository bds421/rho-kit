package gcsbackend

import (
	"testing"
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

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}
