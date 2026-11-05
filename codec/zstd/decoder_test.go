package zstd_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	kzstd "github.com/klauspost/compress/zstd"

	"github.com/bds421/rho-kit/codec/zstd/v2"
)

func encode(t *testing.T, plain []byte, withSize bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := kzstd.NewWriter(&buf, kzstd.WithEncoderLevel(kzstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	if withSize {
		return w.EncodeAll(plain, nil)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeKeepsWithinItsLimits(t *testing.T) {
	d, err := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 200})
	if err != nil {
		t.Fatal(err)
	}
	text := bytes.Repeat([]byte("log line with some variety 0123456789\n"), 100)
	tiny := []byte("tiny")
	for _, plain := range [][]byte{text, tiny} {
		if got, err := d.Decode(encode(t, plain, true)); err != nil || !bytes.Equal(got, plain) {
			t.Errorf("%d bytes: %v", len(plain), err)
		}
	}
	bomb := make([]byte, 8<<20)
	for _, withSize := range []bool{true, false} {
		if _, err := d.Decode(encode(t, bomb, withSize)); !errors.Is(err, zstd.ErrTooLarge) {
			t.Errorf("a bomb, size declared %v: %v", withSize, err)
		}
	}
	ratio := make([]byte, 512<<10)
	if _, err := d.Decode(encode(t, ratio, true)); !errors.Is(err, zstd.ErrTooLarge) {
		t.Errorf("within MaxSize but beyond MaxRatio: %v", err)
	}
}

func TestRequireSizeRefusesFramesWithoutOne(t *testing.T) {
	d, err := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 200, RequireSize: true, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	text := bytes.Repeat([]byte("abc"), 100)
	unsized := encode(t, []byte("tiny"), true)
	var h kzstd.Header
	if err := h.Decode(unsized); err != nil || h.HasFCS {
		t.Fatalf("the test frame declares its size: %v", err)
	}
	if _, err := d.Decode(unsized); !errors.Is(err, zstd.ErrTooLarge) {
		t.Errorf("a frame without its size: %v", err)
	}
	if _, err := d.Decode(encode(t, text, true)); err != nil {
		t.Errorf("a frame with its size: %v", err)
	}
	if _, err := zstd.NewDecoder(zstd.Limits{}); err == nil {
		t.Error("limits of zero were accepted")
	}
}

func TestNewDecoderRefusesAMaxSizeBelowTheWindowMinimum(t *testing.T) {
	_, err := zstd.NewDecoder(zstd.Limits{MaxSize: zstd.MinMaxSize - 1, MaxRatio: 200})
	if err == nil || !strings.Contains(err.Error(), "MaxSize must be at least 1024 bytes") {
		t.Fatalf("MaxSize below the minimum: %v", err)
	}
	d, err := zstd.NewDecoder(zstd.Limits{MaxSize: zstd.MinMaxSize, MaxRatio: 200})
	if err != nil {
		t.Fatalf("MaxSize at the minimum: %v", err)
	}
	defer d.Close()
	if got, err := d.Decode(encode(t, []byte("tiny"), true)); err != nil || string(got) != "tiny" {
		t.Errorf("decode at the minimum: %q, %v", got, err)
	}
	if _, err := zstd.NewDecoder(zstd.Limits{MaxSize: zstd.MinMaxSize}); err == nil {
		t.Error("a MaxRatio of zero was accepted")
	}
}

func TestCloseWaitsForDecodesAndRefusesLaterOnes(t *testing.T) {
	d, err := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 200, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	frame := encode(t, bytes.Repeat([]byte("abc"), 1000), true)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := d.Decode(frame); err != nil && !errors.Is(err, zstd.ErrClosed) {
				t.Errorf("decode around Close: %v", err)
			}
		})
	}
	d.Close()
	wg.Wait()
	if _, err := d.Decode(frame); !errors.Is(err, zstd.ErrClosed) {
		t.Errorf("decode after Close: %v", err)
	}
	d.Close()
}
