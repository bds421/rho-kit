package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEndpointURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rawURL        string
		allowInsecure bool
		wantErr       bool
	}{
		{name: "empty", rawURL: "", wantErr: false},
		{name: "https", rawURL: "https://storage.example.com", wantErr: false},
		{name: "https with path", rawURL: "https://storage.example.com/api/v1", wantErr: false},
		{name: "http without opt-in", rawURL: "http://localhost:9000", wantErr: true},
		{name: "http with opt-in", rawURL: "http://localhost:9000", allowInsecure: true, wantErr: false},
		{name: "missing scheme", rawURL: "localhost:9000", wantErr: true},
		{name: "missing host", rawURL: "https:///bucket", wantErr: true},
		{name: "empty hostname", rawURL: "https://:443/bucket", wantErr: true},
		{name: "empty port", rawURL: "https://storage.example.com:/bucket", wantErr: true},
		{name: "zero port", rawURL: "https://storage.example.com:0/bucket", wantErr: true},
		{name: "too large port", rawURL: "https://storage.example.com:65536/bucket", wantErr: true},
		{name: "zone identifier", rawURL: "https://[fe80::1%25lo0]:9000/bucket", wantErr: true},
		{name: "credentials", rawURL: "https://user:pass@storage.example.com", wantErr: true},
		{name: "query", rawURL: "https://storage.example.com?token=abc", wantErr: true},
		{name: "fragment", rawURL: "https://storage.example.com#frag", wantErr: true},
		{name: "unsupported scheme", rawURL: "ftp://storage.example.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tt.allowInsecure {
				err = ValidateEndpointURLAllowingInsecure("STORAGE_ENDPOINT", tt.rawURL)
			} else {
				err = ValidateEndpointURL("STORAGE_ENDPOINT", tt.rawURL)
			}
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateEndpointURL_ParseErrorDoesNotEchoValue(t *testing.T) {
	t.Parallel()

	err := ValidateEndpointURL("STORAGE_ENDPOINT", "https://storage.example.com/%zz?token=secret-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "STORAGE_ENDPOINT is invalid")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestValidateEndpointURL_SchemeErrorDoesNotEchoValue(t *testing.T) {
	t.Parallel()

	err := ValidateEndpointURL("STORAGE_ENDPOINT", "secret-token://storage.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "STORAGE_ENDPOINT scheme must be https")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestRedactedEndpointURL(t *testing.T) {
	t.Parallel()

	got := RedactedEndpointURL("https://token-user:secret@storage.example.com/api?token=query-secret#frag")
	assert.Contains(t, got, "storage.example.com")
	assert.NotContains(t, got, "token-user")
	assert.NotContains(t, got, "secret")
	assert.NotContains(t, got, "query-secret")
	assert.NotContains(t, got, "frag")
	assert.Equal(t, "", RedactedEndpointURL(""))
	assert.Equal(t, "[INVALID URL]", RedactedEndpointURL("://invalid"))
}

func TestValidateInstanceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "simple", value: "avatars"},
		{name: "max length", value: strings.Repeat("a", MaxInstanceNameBytes)},
		{name: "empty", value: "", wantErr: true},
		{name: "too long", value: strings.Repeat("a", MaxInstanceNameBytes+1), wantErr: true},
		{name: "invalid utf8", value: string([]byte{0xff}), wantErr: true},
		{name: "nul", value: "bad\x00name", wantErr: true},
		{name: "newline", value: "bad\nname", wantErr: true},
		{name: "carriage return", value: "bad\rname", wantErr: true},
		{name: "space", value: "bad name", wantErr: true},
		{name: "tab", value: "bad\tname", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateInstanceName(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.value != "" {
					assert.NotContains(t, err.Error(), tt.value)
				}
				assert.NotContains(t, err.Error(), fmt.Sprintf("%d", len(tt.value)))
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestAllowedMIMETypes(t *testing.T) {
	t.Parallel()

	t.Run("panics on empty allowlist", func(t *testing.T) {
		t.Parallel()
		require.Panics(t, func() {
			AllowedMIMETypes()
		})
	})

	t.Run("panics on malformed allowlist entries", func(t *testing.T) {
		t.Parallel()
		for _, value := range []string{"", "not-a-mime", "image/ png", "*/"} {
			t.Run(value, func(t *testing.T) {
				require.Panics(t, func() {
					AllowedMIMETypes(value)
				})
			})
		}
	})

	t.Run("panic does not reflect malformed allowlist entry", func(t *testing.T) {
		t.Parallel()
		require.PanicsWithValue(t, "storage: invalid MIME type", func() {
			AllowedMIMETypes("not-a-mime secret-token")
		})
		require.PanicsWithValue(t, "storage: invalid MIME wildcard", func() {
			AllowedMIMETypes("image/ secret-token/*")
		})
	})

	t.Run("accepts allowed type", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{}
		v := AllowedMIMETypes("image/png")

		r, err := v(context.Background(), bytes.NewReader(pngData), &meta)
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

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "not allowed")
		assert.NotContains(t, err.Error(), "image/png")
	})

	t.Run("overwrites declared content type", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{ContentType: "application/octet-stream"}
		v := AllowedMIMETypes("image/png")

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.NoError(t, err)
		assert.Equal(t, "image/png", meta.ContentType)
	})

	t.Run("accepts wildcard MIME pattern", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 10, 10)
		meta := ObjectMeta{}
		v := AllowedMIMETypes("image/*")

		r, err := v(context.Background(), bytes.NewReader(pngData), &meta)
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

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
	})

	t.Run("handles small content", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := AllowedMIMETypes("application/octet-stream")

		r, err := v(context.Background(), bytes.NewReader([]byte{0x01}), &meta)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, []byte{0x01}, got)
	})

	t.Run("handles empty content through MIME matching", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := AllowedMIMETypes("text/plain")

		r, err := v(context.Background(), bytes.NewReader(nil), &meta)
		require.NoError(t, err)
		assert.Equal(t, "text/plain", meta.ContentType)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("rejects empty content as validation when type is not allowed", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := AllowedMIMETypes("image/png")

		_, err := v(context.Background(), bytes.NewReader(nil), &meta)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidation)
	})

	t.Run("strips detected MIME parameters before matching", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := AllowedMIMETypes("text/plain")

		r, err := v(context.Background(), strings.NewReader("hello world"), &meta)
		require.NoError(t, err)
		assert.Equal(t, "text/plain", meta.ContentType)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(got))
	})
}

func TestMaxFileSize(t *testing.T) {
	t.Parallel()

	t.Run("accepts content within limit", func(t *testing.T) {
		t.Parallel()
		data := []byte("hello")
		meta := ObjectMeta{}
		v := MaxFileSize(100)

		r, err := v(context.Background(), bytes.NewReader(data), &meta)
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

		r, err := v(context.Background(), bytes.NewReader(data), &meta)
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

		r, err := v(context.Background(), bytes.NewReader(data), &meta)
		require.NoError(t, err)

		_, err = io.ReadAll(r)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "exceeds max")
		assert.NotContains(t, err.Error(), "5")
		assert.NotContains(t, err.Error(), "6")
	})

	t.Run("rejects immediately when declared size exceeds limit", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{Size: 200}
		v := MaxFileSize(100)

		_, err := v(context.Background(), bytes.NewReader(nil), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "declared size")
		assert.NotContains(t, err.Error(), "100")
		assert.NotContains(t, err.Error(), "200")
	})

	t.Run("does not reject when declared size is within limit", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{Size: 50}
		v := MaxFileSize(100)

		_, err := v(context.Background(), bytes.NewReader(make([]byte, 50)), &meta)
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

		r, err := v(context.Background(), bytes.NewReader(pngData), &meta)
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

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "smaller than minimum")
		assert.NotContains(t, err.Error(), "10")
		assert.NotContains(t, err.Error(), "50")
	})

	t.Run("rejects image below minimum height", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 100, 10)
		meta := ObjectMeta{}
		v := ImageDimensions(50, 50, 0, 0)

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "smaller than minimum")
		assert.NotContains(t, err.Error(), "10")
		assert.NotContains(t, err.Error(), "50")
	})

	t.Run("rejects image above maximum width", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 500, 100)
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 200, 200)

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "width")
		assert.NotContains(t, err.Error(), "500")
		assert.NotContains(t, err.Error(), "200")
	})

	t.Run("rejects image above maximum height", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 100, 500)
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 200, 200)

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "height")
		assert.NotContains(t, err.Error(), "500")
		assert.NotContains(t, err.Error(), "200")
	})

	t.Run("zero max means no upper limit", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 5000, 5000)
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 0, 0)

		_, err := v(context.Background(), bytes.NewReader(pngData), &meta)
		require.NoError(t, err)
	})

	t.Run("rejects non-image content", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := ImageDimensions(0, 0, 0, 0)

		_, err := v(context.Background(), strings.NewReader("not an image"), &meta)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.Contains(t, err.Error(), "cannot decode")
		assert.NotContains(t, err.Error(), "not an image")
	})
}

func TestImageDimensionsConfigPanics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		minW int
		minH int
		maxW int
		maxH int
	}{
		{name: "negative minimum width", minW: -1},
		{name: "negative minimum height", minH: -1},
		{name: "negative maximum width", maxW: -1},
		{name: "negative maximum height", maxH: -1},
		{name: "minimum width exceeds maximum", minW: 200, maxW: 100},
		{name: "minimum height exceeds maximum", minH: 200, maxH: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Panics(t, func() {
				_ = ImageDimensions(tt.minW, tt.minH, tt.maxW, tt.maxH)
			})
		})
	}
}

func TestApplyValidators(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("CloneValidators panics on nil validator", func(t *testing.T) {
		t.Parallel()
		require.PanicsWithValue(t, "storage: validator must not be nil", func() {
			v := func(_ context.Context, r io.Reader, _ *ObjectMeta) (io.Reader, error) { return r, nil }
			CloneValidators(v, nil)
		})
	})

	t.Run("CloneValidators returns detached copy", func(t *testing.T) {
		t.Parallel()
		v1 := func(_ context.Context, r io.Reader, _ *ObjectMeta) (io.Reader, error) { return r, nil }
		v2 := func(_ context.Context, r io.Reader, _ *ObjectMeta) (io.Reader, error) { return r, nil }
		in := []Validator{v1}

		out := CloneValidators(in...)
		in[0] = v2

		meta := ObjectMeta{}
		r, err := ApplyValidators(ctx, strings.NewReader("x"), &meta, out)
		require.NoError(t, err)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "x", string(got))
	})

	t.Run("chains validators in order", func(t *testing.T) {
		t.Parallel()
		pngData := createTestPNG(t, 50, 50)
		meta := ObjectMeta{}

		validators := []Validator{
			AllowedMIMETypes("image/png"),
			MaxFileSize(int64(len(pngData) + 1000)),
		}

		r, err := ApplyValidators(ctx, bytes.NewReader(pngData), &meta, validators)
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
			func(_ context.Context, r io.Reader, m *ObjectMeta) (io.Reader, error) {
				called = true
				return r, nil
			},
		}

		_, err := ApplyValidators(ctx, strings.NewReader("not jpeg"), &meta, validators)
		require.Error(t, err)
		assert.False(t, called, "second validator should not have been called")
	})

	t.Run("nil validators is a no-op", func(t *testing.T) {
		t.Parallel()
		data := []byte("hello")
		meta := ObjectMeta{}

		r, err := ApplyValidators(ctx, bytes.NewReader(data), &meta, nil)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("rejects nil context", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		var nilContext context.Context

		_, err := ApplyValidators(nilContext, strings.NewReader("x"), &meta, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context")
		assert.False(t, errors.Is(err, ErrValidation))
	})

	t.Run("rejects nil reader", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		_, err := ApplyValidators(ctx, nil, &meta, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
	})

	t.Run("rejects nil metadata pointer", func(t *testing.T) {
		t.Parallel()
		_, err := ApplyValidators(ctx, strings.NewReader("x"), nil, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
	})

	t.Run("rejects nil validator element", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		v := func(_ context.Context, r io.Reader, _ *ObjectMeta) (io.Reader, error) { return r, nil }
		_, err := ApplyValidators(ctx, strings.NewReader("x"), &meta, []Validator{v, nil})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.NotContains(t, err.Error(), "1")
	})

	t.Run("rejects validator returning nil reader", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		_, err := ApplyValidators(ctx, strings.NewReader("x"), &meta, []Validator{
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return nil, nil
			},
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidation))
		assert.NotContains(t, err.Error(), "0")
	})

	t.Run("closes returned read closer when later validator rejects", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		closed := false
		owned := &trackingReadCloser{Reader: strings.NewReader("payload"), closed: &closed}

		_, err := ApplyValidators(ctx, strings.NewReader("ignored"), &meta, []Validator{
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return owned, nil
			},
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return nil, errors.New("reject")
			},
		})
		require.Error(t, err)
		assert.True(t, closed, "validator-owned reader should be closed on rejection")
	})

	t.Run("MIME wrapper preserves close on rejection", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		closed := false
		owned := &trackingReadCloser{Reader: strings.NewReader("hello world"), closed: &closed}

		_, err := ApplyValidators(ctx, strings.NewReader("ignored"), &meta, []Validator{
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return owned, nil
			},
			AllowedMIMETypes("text/plain"),
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return nil, errors.New("reject")
			},
		})
		require.Error(t, err)
		assert.True(t, closed, "wrapped reader should still close the underlying resource")
	})

	t.Run("image wrapper preserves close on rejection", func(t *testing.T) {
		t.Parallel()
		meta := ObjectMeta{}
		closed := false
		owned := &trackingReadCloser{Reader: bytes.NewReader(createTestPNG(t, 20, 20)), closed: &closed}

		_, err := ApplyValidators(ctx, strings.NewReader("ignored"), &meta, []Validator{
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return owned, nil
			},
			ImageDimensions(1, 1, 100, 100),
			func(context.Context, io.Reader, *ObjectMeta) (io.Reader, error) {
				return nil, errors.New("reject")
			},
		})
		require.Error(t, err)
		assert.True(t, closed, "wrapped image reader should still close the underlying resource")
	})
}

func TestCloseValidatedReader(t *testing.T) {
	t.Parallel()

	t.Run("closes read closer", func(t *testing.T) {
		t.Parallel()
		closed := false
		r := &trackingReadCloser{Reader: strings.NewReader("payload"), closed: &closed}

		require.NoError(t, CloseValidatedReader(r))
		assert.True(t, closed)
	})

	t.Run("non closer is no-op", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, CloseValidatedReader(strings.NewReader("payload")))
	})
}

type trackingReadCloser struct {
	io.Reader
	closed *bool
}

func (r *trackingReadCloser) Close() error {
	*r.closed = true
	return nil
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
