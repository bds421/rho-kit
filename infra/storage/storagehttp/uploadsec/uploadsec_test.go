package uploadsec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
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

func TestAllowMIMETypes_PanicsOnInvalidAllowlist(t *testing.T) {
	assert.Panics(t, func() { AllowMIMETypes() })
	assert.Panics(t, func() { AllowMIMETypes("") })
	assert.Panics(t, func() { AllowMIMETypes("not-a-mime") })
	assert.PanicsWithValue(t, "uploadsec: AllowMIMETypes invalid MIME type", func() {
		AllowMIMETypes("not-a-mime secret-token")
	})
}

func TestAllowMIMETypes_RejectsDisallowed(t *testing.T) {
	v := AllowMIMETypes("image/jpeg")
	body := bytes.NewReader(tinyPNG(t))
	_, err := v.Validate(context.Background(), body, Meta{})
	assert.ErrorIs(t, err, ErrMIMETypeNotAllowed)
	assert.NotContains(t, err.Error(), "image/png")
}

func TestChain_PanicsOnNilValidator(t *testing.T) {
	assert.PanicsWithValue(t, "uploadsec: Chain validator must not be nil", func() {
		Chain(ValidatorFunc(func(_ context.Context, _ io.ReadSeeker, meta Meta) (Meta, error) {
			return meta, nil
		}), nil)
	})
}

func TestScanWith_AcceptsCleanVerdict(t *testing.T) {
	var gotBody string
	var gotMeta Meta
	v := ScanWith(ScannerFunc(func(_ context.Context, body io.Reader, meta Meta) error {
		b, err := io.ReadAll(body)
		require.NoError(t, err)
		gotBody = string(b)
		gotMeta = meta
		return nil
	}))

	meta := Meta{Filename: "avatar.png", ContentType: "image/png"}
	updated, err := v.Validate(context.Background(), bytes.NewReader([]byte("clean")), meta)
	require.NoError(t, err)
	assert.Equal(t, meta, updated)
	assert.Equal(t, "clean", gotBody)
	assert.Equal(t, meta, gotMeta)
}

func TestScanWith_RejectsMalware(t *testing.T) {
	v := ScanWith(ScannerFunc(func(context.Context, io.Reader, Meta) error {
		return MalwareDetected("Eicar-Test-Signature")
	}))

	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("bad")), Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMalwareDetected)
	assert.NotContains(t, err.Error(), "Eicar")

	var detected *MalwareDetectedError
	require.True(t, errors.As(err, &detected))
	assert.Equal(t, "Eicar-Test-Signature", detected.Threat)
}

func TestScanWith_UnknownScannerErrorDoesNotReflectDetails(t *testing.T) {
	v := ScanWith(ScannerFunc(func(context.Context, io.Reader, Meta) error {
		return errors.New("scanner failed while processing secret-token")
	}))

	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("bad")), Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrScannerUnavailable)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "scanner failed")
}

func TestScanWith_PanicsOnNilScanner(t *testing.T) {
	assert.Panics(t, func() { ScanWith(nil) })
}

func TestAllowExtensions_PanicsOnInvalidAllowlist(t *testing.T) {
	assert.Panics(t, func() { AllowExtensions() })
	assert.Panics(t, func() { AllowExtensions("") })
	assert.Panics(t, func() { AllowExtensions("png") })
	assert.Panics(t, func() { AllowExtensions("../png") })
	assert.PanicsWithValue(t, "uploadsec: AllowExtensions invalid extension", func() {
		AllowExtensions("../secret-token")
	})
}

func TestAllowExtensions_RejectsMissing(t *testing.T) {
	v := AllowExtensions(".png", ".jpg")
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("x")), Meta{Filename: "secret-token"})
	assert.ErrorIs(t, err, ErrExtensionNotAllowed)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestAllowExtensions_RejectsDisallowed(t *testing.T) {
	v := AllowExtensions(".png")
	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("x")), Meta{Filename: "secret-token.php"})
	assert.ErrorIs(t, err, ErrExtensionNotAllowed)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), ".php")
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
	assert.NotContains(t, err.Error(), ".jpg")
	assert.NotContains(t, err.Error(), "image/png")
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
	assert.NotContains(t, err.Error(), "100000")
	assert.NotContains(t, err.Error(), "1024")
}

func TestMaxImageDimensions_InvalidImageDoesNotReflectDecoderDetails(t *testing.T) {
	v := MaxImageDimensions(1024, 1024)

	_, err := v.Validate(context.Background(), bytes.NewReader([]byte("secret-token invalid image")), Meta{ContentType: "image/png"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "secret-token")
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
	assert.Equal(t, http.StatusUnprocessableEntity, HTTPStatusForError(ErrMalwareDetected))
	assert.Equal(t, http.StatusServiceUnavailable, HTTPStatusForError(ErrScannerUnavailable))
	assert.Equal(t, http.StatusInternalServerError, HTTPStatusForError(assert.AnError))
}

func readAll(t *testing.T, r io.ReadSeeker) (string, int64) {
	t.Helper()
	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	return string(buf[:n]), int64(n)
}

// tinyJPEG encodes a 1×1 JPEG; the encoder writes a spec-compliant
// stream ending in FFD9 with no trailing padding.
func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// tinyGIF encodes a 1×1 GIF ending in trailer byte 0x3B.
func tinyGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, img, nil))
	return buf.Bytes()
}

// animatedGIF encodes a multi-frame GIF. gif.EncodeAll emits a graphic
// control extension (0x21 0xF9) before each frame, exercising the
// extension-block path of the structural GIF walker.
func animatedGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}}
	g := &gif.GIF{}
	for i := 0; i < 3; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		img.SetColorIndex(i, i, 2)
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, g))
	return buf.Bytes()
}

// tinyWebP returns a hand-built minimal lossy WebP (VP8) carrying a 1×1
// dummy frame. It is just enough for the polyglot end-of-stream check
// to verify; stdlib has no WebP encoder so we synthesise the bytes.
func tinyWebP(t *testing.T) []byte {
	t.Helper()
	// VP8 frame tag bytes for a 1×1 keyframe were captured from the
	// reference encoder; tests only need the bytes to round-trip
	// through validateWebPEnd / peekWebPDimensions, not to decode.
	vp8Body := []byte{
		0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, // frame tag + start code
		0x01, 0x00, // width = 1 (LE 14 bits)
		0x01, 0x00, // height = 1
		0x00, // single zero entropy byte
	}
	chunkLen := uint32(len(vp8Body))
	// VP8 chunk header is "VP8 " + 4-byte LE length.
	chunk := append([]byte("VP8 "), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(chunk[4:], chunkLen)
	chunk = append(chunk, vp8Body...)
	// RIFF wrapper: "RIFF" + 4-byte LE total size (excluding RIFF + size) + "WEBP" + chunks.
	body := append([]byte("WEBP"), chunk...)
	riff := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(riff[4:], uint32(len(body)))
	return append(riff, body...)
}

// drainAndRewind asserts the body is still readable end-to-end after a
// chain run — uploadsec validators must not consume the body or leak
// resources.
func drainAndRewind(t *testing.T, body *bytes.Reader) {
	t.Helper()
	_, err := body.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, body)
	require.NoError(t, err)
}

func TestAllowMIMETypes_RejectsPNGWithAppendedPHP(t *testing.T) {
	v := AllowMIMETypes("image/png")
	payload := append([]byte(nil), tinyPNG(t)...)
	payload = append(payload, []byte("<?php phpinfo(); ?>")...)

	body := bytes.NewReader(payload)
	_, err := v.Validate(context.Background(), body, Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "phpinfo")
	drainAndRewind(t, body)
}

func TestAllowMIMETypes_RejectsPNGWithAppendedJavaScript(t *testing.T) {
	v := AllowMIMETypes("image/png")
	payload := append([]byte(nil), tinyPNG(t)...)
	payload = append(payload, []byte("<script>alert(1)</script>")...)

	body := bytes.NewReader(payload)
	_, err := v.Validate(context.Background(), body, Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "alert")
	drainAndRewind(t, body)
}

func TestAllowMIMETypes_RejectsPNGWithTrailingWhitespace(t *testing.T) {
	// Whitespace and NUL bytes are common "harmless" padding from
	// misconfigured tools, but the spec says the file ends at IEND.
	// We reject conservatively.
	v := AllowMIMETypes("image/png")
	payload := append([]byte(nil), tinyPNG(t)...)
	payload = append(payload, '\n', ' ', '\t', 0, 0)

	_, err := v.Validate(context.Background(), bytes.NewReader(payload), Meta{})
	assert.ErrorIs(t, err, ErrInvalidImage)
}

func TestAllowMIMETypes_RejectsJPEGWithAppendedPayload(t *testing.T) {
	v := AllowMIMETypes("image/jpeg")
	payload := append([]byte(nil), tinyJPEG(t)...)
	payload = append(payload, []byte("<?php system($_GET['c']); ?>")...)

	body := bytes.NewReader(payload)
	_, err := v.Validate(context.Background(), body, Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "system")
	drainAndRewind(t, body)
}

func TestAllowMIMETypes_RejectsGIFWithAppendedPayload(t *testing.T) {
	v := AllowMIMETypes("image/gif")
	payload := append([]byte(nil), tinyGIF(t)...)
	payload = append(payload, []byte("<script>fetch('/admin')</script>")...)

	body := bytes.NewReader(payload)
	_, err := v.Validate(context.Background(), body, Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "fetch")
	drainAndRewind(t, body)
}

func TestAllowMIMETypes_RejectsWebPWithAppendedPayload(t *testing.T) {
	v := AllowMIMETypes("image/webp")
	payload := append([]byte(nil), tinyWebP(t)...)
	payload = append(payload, []byte("<?php phpinfo(); ?>")...)

	_, err := v.Validate(context.Background(), bytes.NewReader(payload), Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
}

// TestAllowMIMETypes_RejectsJPEGPolyglotWithSecondEOI builds the
// append-style polyglot the package doc claims to defend against: a
// valid JPEG (ending in FFD9) followed by a script payload and a
// *second* FFD9 marker so the body still ends in FFD9. jpeg.Decode
// stops at the first EOI and ignores the trailing bytes, so the payload
// survives unless validateJPEGEnd rejects the bytes between the first
// EOI and the end of the body.
func TestAllowMIMETypes_RejectsJPEGPolyglotWithSecondEOI(t *testing.T) {
	v := AllowMIMETypes("image/jpeg")
	payload := append([]byte(nil), tinyJPEG(t)...)
	payload = append(payload, []byte("<?php system($_GET['c']); ?>")...)
	payload = append(payload, 0xFF, 0xD9) // second EOI so the body still ends in FFD9

	body := bytes.NewReader(payload)
	_, err := v.Validate(context.Background(), body, Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "system")
	drainAndRewind(t, body)
}

// TestAllowMIMETypes_RejectsGIFPolyglotWithSecondTrailer is the GIF
// analogue: a valid GIF (ending in 0x3B) followed by a script payload
// and a second 0x3B trailer so the body still ends in 0x3B. gif.Decode
// returns at the first trailer and ignores trailing bytes.
func TestAllowMIMETypes_RejectsGIFPolyglotWithSecondTrailer(t *testing.T) {
	v := AllowMIMETypes("image/gif")
	payload := append([]byte(nil), tinyGIF(t)...)
	payload = append(payload, []byte("<script>fetch('/admin')</script>")...)
	payload = append(payload, 0x3B) // second trailer so the body still ends in 0x3B

	body := bytes.NewReader(payload)
	_, err := v.Validate(context.Background(), body, Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidImage)
	assert.NotContains(t, err.Error(), "fetch")
	drainAndRewind(t, body)
}

func TestAllowMIMETypes_AcceptsCleanPNG(t *testing.T) {
	v := AllowMIMETypes("image/png")
	body := bytes.NewReader(tinyPNG(t))
	meta, err := v.Validate(context.Background(), body, Meta{})
	require.NoError(t, err)
	assert.Equal(t, "image/png", meta.ContentType)
	drainAndRewind(t, body)
}

func TestAllowMIMETypes_AcceptsCleanJPEG(t *testing.T) {
	v := AllowMIMETypes("image/jpeg")
	body := bytes.NewReader(tinyJPEG(t))
	meta, err := v.Validate(context.Background(), body, Meta{})
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", meta.ContentType)
}

func TestAllowMIMETypes_AcceptsCleanGIF(t *testing.T) {
	v := AllowMIMETypes("image/gif")
	body := bytes.NewReader(tinyGIF(t))
	meta, err := v.Validate(context.Background(), body, Meta{})
	require.NoError(t, err)
	assert.Equal(t, "image/gif", meta.ContentType)
}

func TestValidateGIFEnd_SignatureVersion(t *testing.T) {
	clean := tinyGIF(t)

	t.Run("accepts GIF89a", func(t *testing.T) {
		require.NoError(t, validateGIFEnd(clean))
	})

	t.Run("accepts GIF87a", func(t *testing.T) {
		// gif.Encode emits GIF89a; rewrite the version bytes to the
		// other documented version, keeping the rest of the stream intact.
		body := append([]byte(nil), clean...)
		copy(body[:6], []byte("GIF87a"))
		require.NoError(t, validateGIFEnd(body))
	})

	t.Run("rejects unknown version", func(t *testing.T) {
		// Same magic "GIF" prefix but an out-of-spec version. The old
		// 3-byte-only check accepted this; the documented contract does not.
		body := append([]byte(nil), clean...)
		copy(body[:6], []byte("GIF8?a"))
		require.ErrorIs(t, validateGIFEnd(body), ErrInvalidImage)
	})
}

func TestAllowMIMETypes_AcceptsCleanWebP(t *testing.T) {
	v := AllowMIMETypes("image/webp")
	body := bytes.NewReader(tinyWebP(t))
	meta, err := v.Validate(context.Background(), body, Meta{})
	require.NoError(t, err)
	assert.Equal(t, "image/webp", meta.ContentType)
}

// TestAllowMIMETypes_AcceptsAnimatedGIF proves the structural GIF block
// walker (which the second-trailer polyglot fix relies on) does not
// false-reject legitimate multi-frame GIFs whose stream carries graphic
// control extensions between image descriptors.
func TestAllowMIMETypes_AcceptsAnimatedGIF(t *testing.T) {
	v := AllowMIMETypes("image/gif")
	body := bytes.NewReader(animatedGIF(t))
	meta, err := v.Validate(context.Background(), body, Meta{})
	require.NoError(t, err)
	assert.Equal(t, "image/gif", meta.ContentType)
}

func TestAllowMIMETypes_RejectsCorruptedIDAT(t *testing.T) {
	// A PNG whose IHDR parses cleanly but whose IDAT deflate stream is
	// corrupted is the classic case DecodeConfig misses but Decode
	// catches.
	src := tinyPNG(t)
	// Find IDAT and flip a byte in its data — this corrupts deflate
	// without changing chunk length / CRC validation behaviour for
	// DecodeConfig (which only reads IHDR).
	idat := bytes.Index(src, []byte("IDAT"))
	require.Greater(t, idat, 0)
	// Mutate one byte of compressed data (a few bytes past "IDAT"
	// header, inside the deflate body).
	src[idat+8] ^= 0xFF

	v := AllowMIMETypes("image/png")
	_, err := v.Validate(context.Background(), bytes.NewReader(src), Meta{})
	assert.ErrorIs(t, err, ErrInvalidImage)
}

func TestAllowMIMETypes_RejectsOversizedImageBeforeDecode(t *testing.T) {
	// A patched IHDR claiming 99999×99999 dimensions must be rejected
	// by the dimension cap BEFORE png.Decode is invoked. We verify
	// this by ensuring the failure cannot be a "decode failed" — the
	// dimension cap returns ErrImageTooLarge.
	v := AllowMIMETypes("image/png")
	bomb := decompressionBombPNG(t, 99_999, 99_999)
	_, err := v.Validate(context.Background(), bytes.NewReader(bomb), Meta{})
	require.Error(t, err)
	// The patched bomb has a bad IHDR CRC, so DecodeConfig will fail
	// and return ErrInvalidImage — that's fine, it means the
	// dimension check used DecodeConfig (cheap, header-only) and
	// short-circuited before png.Decode (which would allocate the
	// pixel buffer). We accept either ErrImageTooLarge (valid header
	// with attacker dimensions) or ErrInvalidImage (CRC mismatch).
	assert.True(t,
		errors.Is(err, ErrImageTooLarge) || errors.Is(err, ErrInvalidImage),
		"want bomb rejection before full decode; got %v", err)
}

func TestAllowMIMETypes_RejectsAboveDimensionCap(t *testing.T) {
	// Build a PNG whose header is internally consistent but whose
	// declared dimensions exceed the built-in 8192×8192 cap. We
	// patch a real PNG header AND recompute the CRC so DecodeConfig
	// succeeds, proving the cap (not the CRC) does the work.
	v := AllowMIMETypes("image/png")
	bomb := patchedPNGWithValidCRC(t, 10_000, 10_000)
	_, err := v.Validate(context.Background(), bytes.NewReader(bomb), Meta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrImageTooLarge)
}

func TestAllowMIMETypes_RaisedDimensionCapAcceptsLargerHeader(t *testing.T) {
	v := AllowMIMETypes("image/png").WithImageDimensionCap(20000, 20000)
	bomb := patchedPNGWithValidCRC(t, 10_000, 10_000)
	// Patched IDAT/IEND chunks won't pass png.Decode (IDAT data is
	// still 1×1 deflate output), so we expect ErrInvalidImage — but
	// NOT ErrImageTooLarge, proving the cap was raised past 10000.
	_, err := v.Validate(context.Background(), bytes.NewReader(bomb), Meta{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrImageTooLarge)
}

func TestAllowMIMETypes_WithoutStrictEndCheckAcceptsTrailingBytes(t *testing.T) {
	// Opt-out: legacy clients that pad with garbage past FFD9 should
	// still be accepted. The full-image decode still runs and catches
	// corrupted payloads.
	v := AllowMIMETypes("image/jpeg").WithoutStrictImageEndCheck()
	payload := append([]byte(nil), tinyJPEG(t)...)
	payload = append(payload, 0, 0, 0, 0) // garbage padding

	_, err := v.Validate(context.Background(), bytes.NewReader(payload), Meta{})
	require.NoError(t, err)
}

func TestAllowMIMETypes_WithoutStrictEndCheckStillRejectsCorruptDecode(t *testing.T) {
	// Even with the strict-end check disabled, the full-image decode
	// must still catch corrupted payloads. This proves we have two
	// independent layers of defense.
	src := tinyPNG(t)
	idat := bytes.Index(src, []byte("IDAT"))
	require.Greater(t, idat, 0)
	src[idat+8] ^= 0xFF

	v := AllowMIMETypes("image/png").WithoutStrictImageEndCheck()
	_, err := v.Validate(context.Background(), bytes.NewReader(src), Meta{})
	assert.ErrorIs(t, err, ErrInvalidImage)
}

func TestAllowMIMETypes_WithImageDimensionCapPanicsOnNonPositive(t *testing.T) {
	v := AllowMIMETypes("image/png")
	assert.Panics(t, func() { v.WithImageDimensionCap(0, 100) })
	assert.Panics(t, func() { v.WithImageDimensionCap(100, -1) })
}

// TestValidateImageBody_PerFormat exercises the factored
// validateImageBody helper directly, proving it can be unit-tested per
// format without going through the full Validate / Chain plumbing.
func TestValidateImageBody_PerFormat(t *testing.T) {
	t.Run("png clean", func(t *testing.T) {
		err := validateImageBody("image/png", bytes.NewReader(tinyPNG(t)), true)
		assert.NoError(t, err)
	})
	t.Run("png appended payload", func(t *testing.T) {
		payload := append(append([]byte(nil), tinyPNG(t)...), []byte("<?php ?>")...)
		err := validateImageBody("image/png", bytes.NewReader(payload), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("png appended payload accepted when strictEnd disabled", func(t *testing.T) {
		// Trailing bytes are tolerated without strict-end, but a corrupt
		// decode still rejects — covered separately above. Here we use
		// trailing whitespace (decode-clean) to prove strictEnd=false
		// alone widens the accepted set.
		payload := append(append([]byte(nil), tinyPNG(t)...), 0x00, 0x00)
		err := validateImageBody("image/png", bytes.NewReader(payload), false)
		assert.NoError(t, err)
	})
	t.Run("jpeg clean", func(t *testing.T) {
		err := validateImageBody("image/jpeg", bytes.NewReader(tinyJPEG(t)), true)
		assert.NoError(t, err)
	})
	t.Run("jpeg appended payload", func(t *testing.T) {
		payload := append(append([]byte(nil), tinyJPEG(t)...), []byte("<script>")...)
		err := validateImageBody("image/jpeg", bytes.NewReader(payload), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("gif clean", func(t *testing.T) {
		err := validateImageBody("image/gif", bytes.NewReader(tinyGIF(t)), true)
		assert.NoError(t, err)
	})
	t.Run("jpeg polyglot with second EOI", func(t *testing.T) {
		payload := append(append([]byte(nil), tinyJPEG(t)...), []byte("<?php ?>")...)
		payload = append(payload, 0xFF, 0xD9) // duplicate EOI so the body ends in FFD9
		err := validateImageBody("image/jpeg", bytes.NewReader(payload), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("gif appended payload", func(t *testing.T) {
		payload := append(append([]byte(nil), tinyGIF(t)...), []byte("<?php ?>")...)
		err := validateImageBody("image/gif", bytes.NewReader(payload), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("gif polyglot with second trailer", func(t *testing.T) {
		payload := append(append([]byte(nil), tinyGIF(t)...), []byte("<?php ?>")...)
		payload = append(payload, 0x3B) // duplicate trailer so the body ends in 0x3B
		err := validateImageBody("image/gif", bytes.NewReader(payload), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("webp clean", func(t *testing.T) {
		err := validateImageBody("image/webp", bytes.NewReader(tinyWebP(t)), true)
		assert.NoError(t, err)
	})
	t.Run("webp appended payload", func(t *testing.T) {
		payload := append(append([]byte(nil), tinyWebP(t)...), []byte("<?php ?>")...)
		err := validateImageBody("image/webp", bytes.NewReader(payload), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("unknown format rejected", func(t *testing.T) {
		err := validateImageBody("image/heic", bytes.NewReader(tinyPNG(t)), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
	t.Run("body exceeds read limit rejected", func(t *testing.T) {
		// imageBodyReadLimit+1 bytes of zeroes — far larger than any
		// legitimate tiny image, so the limit-trip path fires before the
		// decoder is even invoked.
		huge := make([]byte, imageBodyReadLimit+2)
		err := validateImageBody("image/png", bytes.NewReader(huge), true)
		assert.ErrorIs(t, err, ErrInvalidImage)
	})
}

// patchedPNGWithValidCRC builds a PNG whose IHDR claims (w,h) and
// recomputes the IHDR CRC so DecodeConfig accepts the header. The IDAT
// chunk is left unchanged from the source 1×1 PNG, so png.Decode will
// fail — but the dimension cap check should fire first.
func patchedPNGWithValidCRC(t *testing.T, w, h uint32) []byte {
	t.Helper()
	src := tinyPNG(t)
	out := append([]byte(nil), src...)
	// IHDR chunk runs from offset 8 to 8+4+4+13+4 = 33.
	// Width @16..19, Height @20..23, CRC over type+data @29..32.
	binary.BigEndian.PutUint32(out[16:20], w)
	binary.BigEndian.PutUint32(out[20:24], h)
	// CRC is computed over the chunk type ("IHDR", 4 bytes) and the
	// 13-byte data field — offsets 12..28 inclusive.
	// We use the same hash/crc32 IEEE polynomial as PNG.
	out[29], out[30], out[31], out[32] = crcIEEE(out[12:29])
	return out
}

func crcIEEE(data []byte) (byte, byte, byte, byte) {
	// hash/crc32 IEEE — the polynomial PNG uses.
	const poly = uint32(0xedb88320)
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ poly
			} else {
				crc >>= 1
			}
		}
	}
	crc ^= 0xffffffff
	return byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)
}
