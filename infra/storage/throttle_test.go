package storage

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThrottledReader_LimitsRate(t *testing.T) {
	t.Parallel()

	// 10 KiB of data at 10 KiB/s should take ~1 second.
	data := bytes.Repeat([]byte("x"), 10*1024)
	tr := NewThrottledReader(bytes.NewReader(data), 10*1024)

	start := time.Now()
	got, err := io.ReadAll(tr)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, data, got)
	// Should take at least ~800ms (allowing some tolerance).
	assert.Greater(t, elapsed, 800*time.Millisecond)
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
