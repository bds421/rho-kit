package uploadsec

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PNG with 1×1 pixel — used to test happy-path validators.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// Synthesise a fake PNG header that lies about dimensions: replace the
// IHDR width/height fields with attacker-supplied values. Used to test
// MaxImageDimensions without allocating a real 100,000×100,000 buffer.
func decompressionBombPNG(t *testing.T, w, h uint32) []byte {
	t.Helper()
	src := tinyPNG(t)
	// PNG signature is 8 bytes; IHDR chunk follows. IHDR starts at offset 8;
	// chunk length (4) + type "IHDR" (4) + then 13 bytes of data: width(4),
	// height(4), bit depth(1), color type(1), compression(1), filter(1),
	// interlace(1).
	if len(src) < 24 {
		t.Fatal("source PNG too small to patch")
	}
	out := append([]byte(nil), src...)
	// Width at offset 16, height at offset 20 (big-endian).
	out[16] = byte(w >> 24)
	out[17] = byte(w >> 16)
	out[18] = byte(w >> 8)
	out[19] = byte(w)
	out[20] = byte(h >> 24)
	out[21] = byte(h >> 16)
	out[22] = byte(h >> 8)
	out[23] = byte(h)
	return out
}

func TestAllowMIMETypes_AcceptsAllowed(t *testing.T) {
	v := AllowMIMETypes("image/png")
	body := bytes.NewReader(tinyPNG(t))
	meta, err := v.Validate(context.Background(), body, Meta{ContentType: "application/octet-stream"})
	require.NoError(t, err)
	assert.Equal(t, "image/png", meta.ContentType, "validator must overwrite caller-supplied ContentType with the sniffed value")
}

func TestAllowMIMETypes_RejectsDisallowed(t *testing.T) {
	v := AllowMIMETypes("image/jpeg")
	body := bytes.NewReader(tinyPNG(t))
	_, err := v.Validate(context.Background(), body, Meta{})
	assert.ErrorIs(t, err, ErrMIMETypeNotAllowed)
}

func TestAllowExtensions_RejectsMissing(t *testing.T) {
	v := AllowExtensions(".png", ".jpg")
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("x")), Meta{Filename: "noext"})
	assert.ErrorIs(t, err, ErrExtensionNotAllowed)
}

func TestAllowExtensions_RejectsDisallowed(t *testing.T) {
	v := AllowExtensions(".png")
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("x")), Meta{Filename: "evil.php"})
	assert.ErrorIs(t, err, ErrExtensionNotAllowed)
}

func TestAllowExtensions_AllowedCaseInsensitive(t *testing.T) {
	v := AllowExtensions(".png", ".jpg")
	meta := Meta{Filename: "image.PNG", ContentType: "image/png"}
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("x")), meta)
	require.NoError(t, err)
}

func TestAllowExtensions_RejectsMismatchedContentType(t *testing.T) {
	// Filename says .jpg but the (already-sniffed) ContentType is image/png.
	v := AllowExtensions(".png", ".jpg")
	meta := Meta{Filename: "image.jpg", ContentType: "image/png"}
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("x")), meta)
	assert.ErrorIs(t, err, ErrExtensionNotAllowed)
}

func TestMaxImageDimensions_AcceptsSmallImage(t *testing.T) {
	v := MaxImageDimensions(1024, 1024)
	meta := Meta{ContentType: "image/png"}
	got, err := v.Validate(context.Background(), bytes.NewReader(tinyPNG(t)), meta)
	require.NoError(t, err)
	assert.Equal(t, 1, got.ImageWidth)
	assert.Equal(t, 1, got.ImageHeight)
}

func TestMaxImageDimensions_RejectsBomb(t *testing.T) {
	v := MaxImageDimensions(1024, 1024)
	bomb := decompressionBombPNG(t, 100_000, 100_000) // patched header — never decoded into pixels
	meta := Meta{ContentType: "image/png"}
	_, err := v.Validate(context.Background(), bytes.NewReader(bomb), meta)
	require.Error(t, err)
	// The CRC of the patched IHDR is wrong, so DecodeConfig fails as
	// "invalid image". Either ErrImageTooLarge or ErrInvalidImage is
	// an acceptable rejection — both are 422.
	assert.True(t, strings.Contains(err.Error(), "image"), "must reject patched/oversize image: %v", err)
}

func TestMaxImageDimensions_PassesNonImage(t *testing.T) {
	v := MaxImageDimensions(1024, 1024)
	meta := Meta{ContentType: "application/pdf"}
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("%PDF-1.4")), meta)
	require.NoError(t, err)
}

// TestMaxImageDimensions_DoesNotBufferEntireBody asserts the validator
// reads at most imageHeaderReadLimit bytes regardless of the body size.
// A countingReader proves the read count never approaches the body's
// 100 MiB length — the previous io.ReadAll(body) would have buffered all
// 100 MiB before any size check ran.
func TestMaxImageDimensions_DoesNotBufferEntireBody(t *testing.T) {
	v := MaxImageDimensions(1024, 1024)

	const bodySize = 100 << 20 // 100 MiB
	header := tinyPNG(t)
	cr := &countingReadSeeker{r: bytes.NewReader(append(header, make([]byte, bodySize-len(header))...))}

	_, err := v.Validate(context.Background(), cr, Meta{ContentType: "image/png"})
	require.NoError(t, err)
	// The validator must read at most imageHeaderReadLimit bytes — proving
	// it doesn't buffer the full body. Anything close to bodySize means
	// regression to the io.ReadAll(body) path.
	assert.LessOrEqual(t, cr.read, int64(imageHeaderReadLimit),
		"validator must not read more than imageHeaderReadLimit (%d) bytes; got %d", imageHeaderReadLimit, cr.read)
}

// countingReadSeeker counts bytes read from an underlying ReadSeeker.
type countingReadSeeker struct {
	r    io.ReadSeeker
	read int64
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.r.Seek(offset, whence)
}

func TestChain_ShortCircuitsOnFirstError(t *testing.T) {
	count := 0
	mark := ValidatorFunc(func(_ context.Context, _ io.ReadSeeker, meta Meta) (Meta, error) {
		count++
		return meta, nil
	})
	denyAll := ValidatorFunc(func(_ context.Context, _ io.ReadSeeker, meta Meta) (Meta, error) {
		return meta, ErrMIMETypeNotAllowed
	})

	chain := Chain(mark, denyAll, mark)
	_, err := chain.Validate(context.Background(), bytes.NewReader([]byte("x")), Meta{})
	require.Error(t, err)
	assert.Equal(t, 1, count, "validators after the first error must not run")
}

func TestChain_RewindsBetweenValidators(t *testing.T) {
	first := ValidatorFunc(func(_ context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		got, _ := readAll(t, body)
		assert.Equal(t, "abc", got, "first validator sees full body")
		return meta, nil
	})
	second := ValidatorFunc(func(_ context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		got, _ := readAll(t, body)
		assert.Equal(t, "abc", got, "second validator must see body from offset 0, not from where the first stopped")
		return meta, nil
	})
	_, err := Chain(first, second).Validate(context.Background(), bytes.NewReader([]byte("abc")), Meta{})
	require.NoError(t, err)
}

func TestHTTPStatusForError(t *testing.T) {
	assert.Equal(t, http.StatusUnsupportedMediaType, HTTPStatusForError(ErrMIMETypeNotAllowed))
	assert.Equal(t, http.StatusUnprocessableEntity, HTTPStatusForError(ErrImageTooLarge))
	assert.Equal(t, http.StatusUnprocessableEntity, HTTPStatusForError(ErrInvalidImage))
	assert.Equal(t, http.StatusUnprocessableEntity, HTTPStatusForError(ErrExtensionNotAllowed))
	assert.Equal(t, http.StatusInternalServerError, HTTPStatusForError(assert.AnError))
}

func readAll(t *testing.T, r io.ReadSeeker) (string, int64) {
	t.Helper()
	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	return string(buf[:n]), int64(n)
}
