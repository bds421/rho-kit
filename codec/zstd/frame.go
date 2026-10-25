package zstd

import (
	"errors"

	kzstd "github.com/klauspost/compress/zstd"
)

var errTruncated = errors.New("zstd: truncated frame")

// frameLength is the length of the frame src starts with: its header,
// its blocks up to the last one and its checksum.
func frameLength(src []byte) (int, error) {
	var h kzstd.Header
	rest, err := h.DecodeAndStrip(src)
	if err != nil {
		return 0, err
	}
	n := len(src) - len(rest)
	for {
		if len(rest) < 3 {
			return 0, errTruncated
		}
		header := uint32(rest[0]) | uint32(rest[1])<<8 | uint32(rest[2])<<16
		size := int(header >> 3)
		if (header>>1)&3 == 1 { // an RLE block carries one byte
			size = 1
		}
		if len(rest) < 3+size {
			return 0, errTruncated
		}
		rest, n = rest[3+size:], n+3+size
		if header&1 == 1 {
			break
		}
	}
	if h.HasCheckSum {
		if len(rest) < 4 {
			return 0, errTruncated
		}
		n += 4
	}
	return n, nil
}
