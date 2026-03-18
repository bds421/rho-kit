package storage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowedMIMETypes(t *testing.T) {
	t.Parallel()

	t.Run("accepts allowed type", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{}
		v := AllowedMIMETypes("image/png")

		r, err := v(bytes.NewReader(pngData), &meta)
		require.NoError(t, err)
		assert.Equal(t, "image/png", meta.ContentType)

		// Verify the full content is still readable.
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, pngData, got)
	})

	t.Run("rejects disallowed type", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{}
		v := AllowedMIMETypes("image/jpeg")

		_, err := v(bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("overwrites declared content type", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{ContentType: "application/octet-stream"}
		v := AllowedMIMETypes("image/png")

		_, err := v(bytes.NewReader(pngData), &meta)
		require.NoError(t, err)
		assert.Equal(t, "image/png", meta.ContentType)
	})

	t.Run("accepts wildcard MIME pattern", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{}
		v := AllowedMIMETypes("image/*")

		r, err := v(bytes.NewReader(pngData), &meta)
		require.NoError(t, err)
		assert.Equal(t, "image/png", meta.ContentType)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, pngData, got)
	})

	t.Run("rejects non-matching wildcard", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{}
		v := AllowedMIMETypes("text/*")

		_, err := v(bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
	})

	t.Run("handles small content", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := AllowedMIMETypes("application/octet-stream")

		r, err := v(bytes.NewReader([]byte{0x01}), &meta)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, []byte{0x01}, got)
	})
}

func TestMaxFileSize(t *testing.T) {
	t.Parallel()

	t.Run("accepts content within limit", func(t *testing.T) {
		t.Parallel()
		data := []byte("hello")
		meta := ObjectMeta{}
		v := MaxFileSize(100)

		r, err := v(bytes.NewReader(data), &meta)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("accepts content at exact limit", func(t *testing.T) {
		t.Parallel()
		data := []byte("12345")
		meta := ObjectMeta{}
		v := MaxFileSize(5)

		r, err := v(bytes.NewReader(data), &meta)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("rejects content exceeding limit during read", func(t *testing.T) {
		t.Parallel()
		data := []byte("123456")
		meta := ObjectMeta{}
		v := MaxFileSize(5)

		r, err := v(bytes.NewReader(data), &meta)
		require.NoError(t, err)

		_, err = io.ReadAll(r)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "exceeds max")
	})

	t.Run("rejects immediately when declared size exceeds limit", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{Size: 200}
		v := MaxFileSize(100)

		_, err := v(bytes.NewReader(nil), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "declared size")
	})

	t.Run("does not reject when declared size is within limit", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{Size: 50}
		v := MaxFileSize(100)

		_, err := v(bytes.NewReader(make([]byte, 50)), &meta)
		require.NoError(t, err)
	})
}

func TestImageDimensions(t *testing.T) {
	t.Parallel()

	t.Run("accepts image within bounds", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 100, 100)
		meta := ObjectMeta{}
		v := ImageDimensions(50, 50, 200, 200)

		r, err := v(bytes.NewReader(pngData), &meta)
		require.NoError(t, err)

		// Verify the full content is still readable.
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, pngData, got)
	})

	t.Run("rejects image below minimum width", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 100)
		meta := ObjectMeta{}
		v := ImageDimensions(50, 50, 0, 0)

		_, err := v(bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "smaller than minimum")
	})

	t.Run("rejects image below minimum height", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 100, 10)
		meta := ObjectMeta{}
		v := ImageDimensions(50, 50, 0, 0)

		_, err := v(bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "smaller than minimum")
	})

	t.Run("rejects image above maximum width", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 500, 100)
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 200, 200)

		_, err := v(bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "width")
	})

	t.Run("rejects image above maximum height", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 100, 500)
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 200, 200)

		_, err := v(bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "height")
	})

	t.Run("zero max means no upper limit", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 5000, 5000)
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 0, 0)

		_, err := v(bytes.NewReader(pngData), &meta)
		require.NoError(t, err)
	})

	t.Run("rejects non-image content", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 0, 0)

		_, err := v(strings.NewReader("not an image"), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "cannot decode")
	})
}

func TestApplyValidators(t *testing.T) {
	t.Parallel()

	t.Run("chains validators in order", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 50, 50)
		meta := ObjectMeta{}

		validators := []Validator{
			AllowedMIMETypes("image/png"),
			MaxFileSize(int64(len(pngData) + 1000)),
		}

		r, err := ApplyValidators(bytes.NewReader(pngData), &meta, validators)
		require.NoError(t, err)
		assert.Equal(t, "image/png", meta.ContentType)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, pngData, got)
	})

	t.Run("stops on first error", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		called := false

		validators := []Validator{
			AllowedMIMETypes("image/jpeg"), // Will fail on non-JPEG
			func(r io.Reader, m *ObjectMeta) (io.Reader, error) {
				called = true
				return r, nil
			},
		}

		_, err := ApplyValidators(strings.NewReader("not jpeg"), &meta, validators)
		require.Error(t, err)
		assert.False(t, called, "second validator should not have been called")
	})

	t.Run("nil validators is a no-op", func(t *testing.T) {
		t.Parallel()
		data := []byte("hello")
		meta := ObjectMeta{}

		r, err := ApplyValidators(bytes.NewReader(data), &meta, nil)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})
}

// createTestPNG generates a minimal PNG image of the given dimensions.
func createTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with a single color to keep the PNG small.
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
