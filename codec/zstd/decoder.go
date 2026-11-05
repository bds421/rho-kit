package zstd

import (
	"errors"
	"fmt"
	"sync"

	kzstd "github.com/klauspost/compress/zstd"
)

var (
	// ErrTooLarge means the data would expand beyond its limits; it is not
	// decoded.
	ErrTooLarge = errors.New("zstd: data expands beyond its limits")
	// ErrFrames means the data holds more than one frame, or a skippable
	// one, and the Limits do not allow that.
	ErrFrames = errors.New("zstd: the data holds more than one frame")
	// ErrClosed means Decode was called after Close.
	ErrClosed = errors.New("zstd: decoder closed")
)

// MinMaxSize is the smallest [Limits.MaxSize]: the decoder window, which
// MaxSize also bounds, cannot be smaller.
const MinMaxSize = kzstd.MinWindowSize

// maxWindow is the largest decoder window the library accepts.
const maxWindow = (1 << 41) + 7*(1<<38)

// Limits bound what one Decode may produce.
type Limits struct {
	// MaxSize is the most bytes one Decode may produce, at least
	// [MinMaxSize].
	MaxSize int
	// MaxRatio is how many times its compressed size the data may expand.
	MaxRatio int
	// RequireSize refuses frames that do not declare their content size;
	// without it, such frames decode within MaxSize and MaxRatio.
	RequireSize bool
	// MultiFrame allows several frames, skippable ones included, decoded
	// together within the same limits; otherwise the data must be one
	// frame and nothing after it.
	MultiFrame bool
	// Concurrency is the most decodes holding memory at once (0: 1).
	Concurrency int
}

// Decoder decodes zstd within its Limits; it is safe for concurrent use.
// Close releases it.
type Decoder struct {
	limits Limits
	dec    *kzstd.Decoder
	slots  chan struct{}
	// mu is held shared by each Decode and exclusively by Close: the
	// library decoder must not be closed under a running decode.
	mu     sync.RWMutex
	closed bool
	// held, set only by tests, sees the bytes a decode reserves once it
	// holds a slot, and their release (negative).
	held func(bytes int64)
}

// NewDecoder builds a Decoder; MaxSize must be at least [MinMaxSize] and
// MaxRatio positive.
func NewDecoder(l Limits) (*Decoder, error) {
	if l.MaxSize < MinMaxSize {
		return nil, fmt.Errorf("zstd: MaxSize must be at least %d bytes", MinMaxSize)
	}
	if l.MaxRatio <= 0 {
		return nil, errors.New("zstd: MaxRatio must be positive")
	}
	if l.Concurrency <= 0 {
		l.Concurrency = 1
	}
	dec, err := kzstd.NewReader(nil,
		kzstd.WithDecoderMaxMemory(uint64(l.MaxSize)), kzstd.WithDecoderMaxWindow(min(uint64(l.MaxSize), maxWindow)),
		kzstd.WithDecodeAllCapLimit(true), kzstd.WithDecoderConcurrency(l.Concurrency))
	if err != nil {
		return nil, err
	}
	return &Decoder{limits: l, dec: dec, slots: make(chan struct{}, l.Concurrency)}, nil
}

// Decode decodes src whole within the limits. The decoder itself stops at
// the bound, so neither later frames nor a false size get past it.
func (d *Decoder) Decode(src []byte) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	var h kzstd.Header
	if err := h.Decode(src); err != nil {
		return nil, err
	}
	bound := min(uint64(d.limits.MaxSize), uint64(len(src))*uint64(d.limits.MaxRatio))
	if err := d.check(h, src, bound); err != nil {
		return nil, err
	}
	capacity := bound
	if h.HasFCS && !d.limits.MultiFrame {
		capacity = h.FrameContentSize
	}
	d.slots <- struct{}{}
	defer func() { <-d.slots }()
	if d.held != nil {
		d.held(int64(capacity))
		defer d.held(-int64(capacity))
	}
	out, err := d.dec.DecodeAll(src, make([]byte, 0, capacity))
	switch {
	case errors.Is(err, kzstd.ErrDecoderSizeExceeded), errors.Is(err, kzstd.ErrWindowSizeExceeded):
		return nil, ErrTooLarge
	case err != nil:
		return nil, err
	case h.HasFCS && !d.limits.MultiFrame && uint64(len(out)) != h.FrameContentSize:
		return nil, ErrTooLarge
	}
	return out, nil
}

// Close waits for running decodes and releases the library decoder; a
// later Decode returns [ErrClosed]. A second Close does nothing.
func (d *Decoder) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	d.dec.Close()
}

func (d *Decoder) check(h kzstd.Header, src []byte, bound uint64) error {
	switch {
	case h.HasFCS && h.FrameContentSize > bound:
		return ErrTooLarge
	case !h.HasFCS && d.limits.RequireSize:
		return ErrTooLarge
	case d.limits.MultiFrame:
		return nil
	case h.Skippable:
		return ErrFrames
	}
	n, err := frameLength(src)
	if err != nil {
		return err
	}
	if n != len(src) {
		return ErrFrames
	}
	return nil
}
