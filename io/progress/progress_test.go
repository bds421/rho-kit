package progress

import (
	"bytes"
	"io"
	"testing"

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
