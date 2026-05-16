package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// ChecksumMetaKey is the ObjectMeta.Custom key used to store/retrieve SHA-256 checksums.
const ChecksumMetaKey = "sha256"

// ChecksumValidator returns a Validator that computes SHA-256 as the stream
// passes through and stores it in meta.Custom[ChecksumMetaKey].
// The input reader must implement [io.ReadSeeker] so the checksum can be
// computed before upload metadata is sent, then rewound for the backend.
func ChecksumValidator() Validator {
	return func(_ context.Context, r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		rs, ok := r.(io.ReadSeeker)
		if !ok {
			return nil, fmt.Errorf("%w: ChecksumValidator requires an io.ReadSeeker", ErrValidation)
		}
		start, err := rs.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, redact.WrapError("storage: checksum seek current", err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, rs); err != nil {
			_, _ = rs.Seek(start, io.SeekStart)
			return nil, redact.WrapError("storage: compute checksum", err)
		}
		if _, err := rs.Seek(start, io.SeekStart); err != nil {
			return nil, redact.WrapError("storage: checksum rewind", err)
		}
		meta.Custom = CloneCustomMeta(meta.Custom)
		if meta.Custom == nil {
			meta.Custom = make(map[string]string)
		}
		meta.Custom[ChecksumMetaKey] = hex.EncodeToString(h.Sum(nil))
		return rs, nil
	}
}

// VerifyChecksum wraps a reader and verifies the SHA-256 checksum at EOF.
// If the computed checksum does not match expected, the final Read returns
// an error wrapping ErrValidation.
//
// Use this on the Get path:
//
//	rc, meta, _ := backend.Get(ctx, key)
//	verified := storage.VerifyChecksum(rc, meta.Custom["sha256"])
func VerifyChecksum(r io.ReadCloser, expected string) io.ReadCloser {
	if expected == "" {
		return r
	}
	h := sha256.New()
	return &verifyReader{
		rc:       r,
		tee:      io.TeeReader(r, h),
		hash:     h,
		expected: expected,
	}
}

type verifyReader struct {
	rc       io.ReadCloser
	tee      io.Reader
	hash     hash.Hash
	expected string
	done     bool
	err      error // buffered mismatch error returned on the next Read after final bytes
}

func (v *verifyReader) Read(p []byte) (int, error) {
	if v.done {
		// Previous Read returned the final bytes; now return the buffered error.
		return 0, v.err
	}

	n, err := v.tee.Read(p)
	if err == io.EOF {
		v.done = true
		got := hex.EncodeToString(v.hash.Sum(nil))
		if got != v.expected {
			// Per the io.Reader contract, callers MUST process n > 0 bytes
			// before considering the error. Return the bytes from this read
			// with the mismatch error so the caller can process them.
			mismatchErr := fmt.Errorf("%w: checksum mismatch", ErrValidation)
			if n > 0 {
				// Buffer the error: return (n, nil) now and (0, mismatchErr)
				// on the next Read, ensuring the caller processes the final
				// bytes before seeing the error.
				v.err = mismatchErr
				return n, nil
			}
			return 0, mismatchErr
		}
		// Checksum matched. Buffer io.EOF so subsequent reads return (0, io.EOF)
		// per the io.Reader contract (reads after EOF must continue returning EOF).
		v.err = io.EOF
	}
	return n, err
}

func (v *verifyReader) Close() error {
	return v.rc.Close()
}
