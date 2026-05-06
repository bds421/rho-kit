package progress

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressReader(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("x"), 1024)
	var reports []int64

	pr := NewProgressReader(bytes.NewReader(data), 1024, func(bytesRead, totalBytes int64) {
		reports = append(reports, bytesRead)
		assert.Equal(t, int64(1024), totalBytes)
	})

	got, err := io.ReadAll(pr)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.NotEmpty(t, reports)

	// Last report should equal total bytes.
	assert.Equal(t, int64(1024), reports[len(reports)-1])
}

func TestProgressReader_NilFunc(t *testing.T) {
	t.Parallel()

	data := []byte("hello")
	r := NewProgressReader(bytes.NewReader(data), 5, nil)

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestProgressReader_UnknownTotal(t *testing.T) {
	t.Parallel()

	var lastTotal int64 = -999
	pr := NewProgressReader(bytes.NewReader([]byte("abc")), -1, func(_, total int64) {
		lastTotal = total
	})

	_, err := io.ReadAll(pr)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), lastTotal)
}

func TestProgressReader_WithMinDelta_CoalescesCallbacks(t *testing.T) {
	src := bytes.Repeat([]byte("x"), 1024)
	var fires int
	pr := NewProgressReader(bytes.NewReader(src), int64(len(src)), func(_, _ int64) {
		fires++
	}, WithMinDelta(256))

	buf := make([]byte, 64)
	for {
		_, err := pr.Read(buf)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}

	// 1024 bytes / 256 per-fire = 4 in-stream fires (first read fires once
	// because lastFireAt is zero). Final EOF callback adds one more if it
	// wasn't already fired by the delta. Either way, coalescing must reduce
	// the per-Read default of ~16 to a much smaller number.
	assert.Less(t, fires, 16, "WithMinDelta(256) must coalesce per-8KB-Read callbacks")
	assert.GreaterOrEqual(t, fires, 1, "at least one callback must fire")
}

func TestProgressReader_WithThrottle_FinalCallbackOnEOF(t *testing.T) {
	src := bytes.Repeat([]byte("y"), 64)
	var lastN int64
	pr := NewProgressReader(bytes.NewReader(src), int64(len(src)), func(n, _ int64) {
		lastN = n
	}, WithThrottle(time.Hour)) // throttle so aggressive that only first + EOF fire

	buf := make([]byte, 8)
	for {
		_, err := pr.Read(buf)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	// Final callback (on EOF) must report the full byte count.
	assert.Equal(t, int64(64), lastN)
}
