package storage

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThrottledReader_DelegatesToProgressReader(t *testing.T) {
	t.Parallel()

	// The pacing contract is tested in io/progress. Storage owns only this
	// delegation wrapper, so verify byte-preserving delegation without
	// duplicating the progress package's real-time rate test.
	data := bytes.Repeat([]byte("x"), 1024)
	tr := NewThrottledReader(bytes.NewReader(data), 0)
	got, err := io.ReadAll(tr)

	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestThrottledReader_ZeroBytesPerSecond(t *testing.T) {
	t.Parallel()

	data := []byte("hello")
	tr := NewThrottledReader(bytes.NewReader(data), 0)

	got, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestThrottledReader_NegativeBytesPerSecond(t *testing.T) {
	t.Parallel()

	data := []byte("hello")
	tr := NewThrottledReader(bytes.NewReader(data), -1)

	got, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}
