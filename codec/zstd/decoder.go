package zstd

import (
	"errors"

	kzstd "github.com/klauspost/compress/zstd"
)

// ErrTooLarge means the data would expand beyond its limits; it is not
// decoded.
var ErrTooLarge = errors.New("zstd: data expands beyond its limits")

// Limits bound what one Decode may produce.
type Limits struct {
	// MaxSize is the most bytes one frame may decode to.
	MaxSize int
	// MaxRatio is how many times its compressed size a frame may expand.
	MaxRatio int
	// RequireSize refuses frames that do not declare their content size;
	// without it, such frames decode within MaxSize and MaxRatio.
	RequireSize bool
	// Concurrency is the most decodes holding memory at once (0: 1).
	Concurrency int
}

// Decoder decodes zstd within its Limits; it is safe for concurrent use.
type Decoder struct {
	limits Limits
	dec    *kzstd.Decoder
	slots  chan struct{}
}

// NewDecoder builds a Decoder; MaxSize and MaxRatio must be positive.
func NewDecoder(l Limits) (*Decoder, error) {
	if l.MaxSize <= 0 || l.MaxRatio <= 0 {
		return nil, errors.New("zstd: MaxSize and MaxRatio must be positive")
	}
	if l.Concurrency <= 0 {
		l.Concurrency = 1
	}
	dec, err := kzstd.NewReader(nil,
		kzstd.WithDecoderMaxMemory(uint64(l.MaxSize)), kzstd.WithDecoderMaxWindow(uint64(l.MaxSize)),
		kzstd.WithDecoderConcurrency(l.Concurrency))
	if err != nil {
		return nil, err
	}
	return &Decoder{limits: l, dec: dec, slots: make(chan struct{}, l.Concurrency)}, nil
}

// Decode decodes src whole within the limits.
func (d *Decoder) Decode(src []byte) ([]byte, error) {
	var h kzstd.Header
	if err := h.Decode(src); err != nil {
		return nil, err
	}
	bound := d.bound(len(src))
	switch {
	case h.HasFCS && h.FrameContentSize > bound:
		return nil, ErrTooLarge
	case !h.HasFCS && d.limits.RequireSize:
		return nil, ErrTooLarge
	}
	var dst []byte
	if h.HasFCS {
		dst = make([]byte, 0, h.FrameContentSize)
	}
	d.slots <- struct{}{}
	defer func() { <-d.slots }()
	out, err := d.dec.DecodeAll(src, dst)
	switch {
	case errors.Is(err, kzstd.ErrDecoderSizeExceeded), errors.Is(err, kzstd.ErrWindowSizeExceeded):
		return nil, ErrTooLarge
	case err != nil:
		return nil, err
	case uint64(len(out)) > bound, h.HasFCS && uint64(len(out)) != h.FrameContentSize:
		return nil, ErrTooLarge
	}
	return out, nil
}

func (d *Decoder) bound(compressed int) uint64 {
	return min(uint64(d.limits.MaxSize), uint64(compressed)*uint64(d.limits.MaxRatio))
}
