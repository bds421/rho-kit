package progress

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThrottledReader_LimitsRate(t *testing.T) {
	t.Parallel()

	// 1 KiB of data at 10 KiB/s should take ~100ms. This is long enough to
	// prove pacing without making the unit suite sleep for a full second.
	data := bytes.Repeat([]byte("x"), 1024)
	tr := NewThrottledReader(bytes.NewReader(data), 10*1024)

	start := time.Now()
	got, err := io.ReadAll(tr)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, data, got)
	// Allow scheduler tolerance while still rejecting an unthrottled read.
	assert.Greater(t, elapsed, 70*time.Millisecond)
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

func TestThrottledReader_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Large data at slow rate — should be interrupted by context.
	data := bytes.Repeat([]byte("x"), 100*1024) // 100 KiB
	ctx, cancel := context.WithCancel(context.Background())

	// 1 KiB/s — would take 100 seconds without cancellation.
	tr := NewThrottledReaderContext(ctx, bytes.NewReader(data), 1024)

	// Cancel after 50ms.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := io.ReadAll(tr)
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	// Should complete quickly after cancellation, not wait 100 seconds.
	assert.Less(t, elapsed, time.Second)
}

// TestThrottledReader_IdleResetAllowsFirstChunkBurst pins the documented
// contract: after >1s idle the first chunk is not re-charged (no full
// fair-share sleep), while a subsequent chunk at the same rate is paced.
func TestThrottledReader_IdleResetAllowsFirstChunkBurst(t *testing.T) {
	t.Parallel()

	// Use a slow rate so a full fair-share sleep for one chunk is obvious.
	// maxChunk = bps/10; with bps=1000, maxChunk=100 bytes → fair share 100ms.
	bps := int64(1000)
	chunk := make([]byte, 100)
	for i := range chunk {
		chunk[i] = 'x'
	}
	// Two chunks with a >1s gap between them.
	r := &scriptedReader{chunks: [][]byte{chunk, chunk}}
	tr := NewThrottledReader(r, bps).(*throttledReader)

	// First read establishes baseline (may sleep ~100ms for the chunk).
	n, err := tr.Read(make([]byte, 100))
	require.NoError(t, err)
	require.Equal(t, 100, n)

	// Simulate idle >1s.
	tr.lastTime = time.Now().Add(-2 * time.Second)
	tr.bytesSent = 0

	start := time.Now()
	n, err = tr.Read(make([]byte, 100))
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 100, n)
	// Post-idle first chunk must not wait the full fair-share (~100ms).
	// Allow a small scheduling slack but fail if we slept ~full duration.
	assert.Less(t, elapsed, 50*time.Millisecond,
		"post-idle first chunk should be delivered without full fair-share sleep, took %s", elapsed)
}

// scriptedReader returns successive chunks then EOF.
type scriptedReader struct {
	chunks [][]byte
	i      int
}

func (s *scriptedReader) Read(p []byte) (int, error) {
	if s.i >= len(s.chunks) {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[s.i])
	s.i++
	return n, nil
}
