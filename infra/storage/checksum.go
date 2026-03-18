package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// ChecksumMetaKey is the ObjectMeta.Custom key used to store/retrieve SHA-256 checksums.
const ChecksumMetaKey = "sha256"

// ChecksumValidator returns a Validator that computes SHA-256 as the stream
// passes through and stores it in meta.Custom[ChecksumMetaKey].
// The checksum is computed incrementally without buffering the entire content.
func ChecksumValidator() Validator {
	return func(r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		h := sha256.New()
		wrapped := &checksumReader{r: io.TeeReader(r, h), hash: h, meta: meta}
		return wrapped, nil
	}
}

type checksumReader struct {
	r    io.Reader
	hash hash.Hash
	meta *ObjectMeta
	done bool
}

func (c *checksumReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF && !c.done {
		// Store checksum only on EOF — the stream has been fully read.
		// Storing on non-EOF errors would produce a checksum over partial
		// data, which is incorrect and could cause verification failures
		// on subsequent Get operations.
		c.done = true
		c.storeChecksum()
	}
	return n, err
}

func (c *checksumReader) storeChecksum() {
	sum := hex.EncodeToString(c.hash.Sum(nil))
	if c.meta.Custom == nil {
		c.meta.Custom = make(map[string]string)
	}
	c.meta.Custom[ChecksumMetaKey] = sum
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
			mismatchErr := fmt.Errorf("%w: checksum mismatch: expected %s, got %s", ErrValidation, v.expected, got)
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
