package azurebackend

import (
	"testing"
)

func TestNewWithClient_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(nil, "container")
	})
}

func TestNewWithClient_PanicsOnEmptyContainer(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(stubBlobClient{}, "")
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

type stubBlobClient struct{ BlobClient }
