package zstd_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/bds421/rho-kit/codec/zstd/v2"
)

// Every frame declares a small size, together they expand far beyond the
// limit: the decoder stops at the bound either way.
func TestManyFramesCannotPassTheLimit(t *testing.T) {
	frame := encode(t, make([]byte, 512<<10), true)
	bomb := bytes.Repeat(frame, 64)
	for _, multi := range []bool{false, true} {
		d, err := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 1 << 20, MultiFrame: multi})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Decode(bomb); err == nil {
			t.Errorf("multi-frame %v: a bomb of frames decoded", multi)
		}
	}
	two := append(encode(t, []byte("first frame of text, long enough"), true), encode(t, []byte("second"), true)...)
	d, _ := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 200})
	if _, err := d.Decode(two); !errors.Is(err, zstd.ErrFrames) {
		t.Errorf("a second frame was accepted: %v", err)
	}
	trailing := append(encode(t, bytes.Repeat([]byte("abc"), 100), true), 0, 1, 2)
	if _, err := d.Decode(trailing); !errors.Is(err, zstd.ErrFrames) {
		t.Errorf("trailing bytes were accepted: %v", err)
	}
	multi, _ := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 200, MultiFrame: true})
	if got, err := multi.Decode(two); err != nil || string(got) != "first frame of text, long enoughsecond" {
		t.Errorf("multi-frame: %q %v", got, err)
	}
}

func TestASkippableFrameIsRefusedUnlessAllowed(t *testing.T) {
	skippable := make([]byte, 8, 8+16)
	binary.LittleEndian.PutUint32(skippable, 0x184D2A50)
	binary.LittleEndian.PutUint32(skippable[4:], 16)
	skippable = append(skippable, make([]byte, 16)...)
	data := append(skippable, encode(t, bytes.Repeat([]byte("abc"), 100), true)...)
	d, _ := zstd.NewDecoder(zstd.Limits{MaxSize: 1 << 20, MaxRatio: 200})
	if _, err := d.Decode(data); !errors.Is(err, zstd.ErrFrames) {
		t.Errorf("a skippable frame was accepted: %v", err)
	}
}

// Oversized requests wait for a slot before anything is allocated: with
// one slot, many at once hold the memory of one.
func TestConcurrentDecodesHoldNoMoreThanTheirSlots(t *testing.T) {
	plain := bytes.Repeat([]byte("0123456789abcdef"), 4<<20/16)
	frame := encode(t, plain, true)
	d, err := zstd.NewDecoder(zstd.Limits{MaxSize: 8 << 20, MaxRatio: 1 << 20, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var wg sync.WaitGroup
	var peak uint64
	var mu sync.Mutex
	for range 16 {
		wg.Go(func() {
			out, err := d.Decode(frame)
			if err != nil || len(out) != len(plain) {
				t.Errorf("decode: %v", err)
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			mu.Lock()
			peak = max(peak, m.HeapInuse)
			mu.Unlock()
		})
	}
	wg.Wait()
	if grown := peak - min(peak, before.HeapInuse); grown > 6*uint64(len(plain)) {
		t.Errorf("16 decodes with one slot held %d MiB", grown>>20)
	}
}
