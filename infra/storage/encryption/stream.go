package encryption

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/tink-crypto/tink-go/v2/streamingaead/subtle"
)

// The stored format is Tink's AES-GCM-HKDF streaming AEAD with these
// parameters, so any Tink implementation configured alike reads it:
//
//	header   = len (1 byte, 40) || salt (32 bytes) || nonce prefix (7 bytes)
//	segment  = AES-256-GCM(object key, nonce, plaintext segment) (16-byte tag)
//	nonce    = nonce prefix || segment number (4 bytes, big endian) || last (1 byte)
//
// The object key is HKDF-SHA256(main key, salt, info = associated data), so
// every object has its own key and the storage key is bound through the
// associated data. A segment is SegmentSize bytes of ciphertext; the first
// holds 40 fewer bytes of plaintext to make room for the header, the last
// may be shorter and is the only one sealed with last = 1.
const (
	// SegmentSize is the ciphertext size of every segment but the last.
	SegmentSize = 64 << 10

	keySize      = 32
	tagSize      = subtle.AESGCMHKDFTagSizeInBytes
	headerSize   = 1 + keySize + subtle.AESGCMHKDFNoncePrefixSizeInBytes
	plainSegment = SegmentSize - tagSize
	firstPlain   = plainSegment - headerSize
	hkdfHash     = "SHA256"
)

// ErrInvalidCiphertext reports an object that does not decrypt: it was
// truncated, its segments were reordered or changed, it was written under
// another storage key or encryption key, or it is not in this format.
var ErrInvalidCiphertext = errors.New("encryption: ciphertext is invalid, truncated or tampered with")

func newStreamingAEAD(key []byte) (aeadCipher, error) {
	return subtle.NewAESGCMHKDF(key, hkdfHash, keySize, SegmentSize, 0)
}

// ciphertextSize is the stored size of n plaintext bytes.
func ciphertextSize(n int64) int64 {
	segments := int64(1)
	if n > firstPlain {
		segments += (n - firstPlain + plainSegment - 1) / plainSegment
	}
	return headerSize + n + segments*tagSize
}

// plaintextSize is the plaintext size of a stored object of n bytes, or
// false when no plaintext encrypts to n bytes.
func plaintextSize(n int64) (int64, bool) {
	if n < headerSize+tagSize {
		return 0, false
	}
	// The header is part of the first segment.
	segments := (n + SegmentSize - 1) / SegmentSize
	plain := n - headerSize - segments*tagSize
	if plain < 0 || ciphertextSize(plain) != n {
		return 0, false
	}
	return plain, true
}

// encryptingReader encrypts src as it is read, one segment at a time, so
// the backend pulls ciphertext without the plaintext being held whole and
// without a goroutine: nothing reads src after the backend stops reading.
type encryptingReader struct {
	src      io.Reader
	declared int64 // the plaintext size the caller declared; 0 is unknown
	read     int64

	sealed *bytes.Buffer // ciphertext not yet returned
	w      io.WriteCloser
	chunk  []byte
	done   bool
	err    error
}

func newEncryptingReader(aead aeadCipher, src io.Reader, aad []byte, declared int64) (*encryptingReader, error) {
	sealed := bytes.NewBuffer(make([]byte, 0, 2*SegmentSize))
	w, err := aead.NewEncryptingWriter(sealed, aad)
	if err != nil {
		return nil, err
	}
	return &encryptingReader{src: src, declared: declared, sealed: sealed, w: w, chunk: make([]byte, plainSegment)}, nil
}

func (e *encryptingReader) Read(p []byte) (int, error) {
	for e.sealed.Len() == 0 {
		if e.err != nil {
			return 0, e.err
		}
		if e.done {
			return 0, io.EOF
		}
		e.err = e.fill()
	}
	return e.sealed.Read(p)
}

// fill reads the next chunk of plaintext and seals what it completes; at
// the end of src it seals the last segment.
func (e *encryptingReader) fill() error {
	n, err := io.ReadFull(e.src, e.chunk)
	e.read += int64(n)
	if e.declared > 0 && e.read > e.declared {
		return fmt.Errorf("%w: content longer than its declared size", errSize)
	}
	if n > 0 {
		if _, werr := e.w.Write(e.chunk[:n]); werr != nil {
			return werr
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		if e.declared > 0 && e.read != e.declared {
			return fmt.Errorf("%w: content shorter than its declared size", errSize)
		}
		e.done = true
		clear(e.chunk)
		return e.w.Close()
	default:
		return errSource{err}
	}
}

// errSize marks a plaintext whose size differs from ObjectMeta.Size.
var errSize = errors.New("encryption: size mismatch")

// errSource marks an error of the plaintext or ciphertext reader itself,
// as opposed to one of the encryption.
type errSource struct{ err error }

func (e errSource) Error() string { return e.err.Error() }
func (e errSource) Unwrap() error { return e.err }

// sourceReader records the backend reader's own failure, so it is reported
// as a failed read and not as a failed decryption: Tink folds the header
// read's errors into its own text and takes io.ErrUnexpectedEOF for the
// end of the object.
type sourceReader struct {
	r   io.Reader
	err error
}

func (s *sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
	}
	return n, err
}

// decryptingReader returns plaintext segment by segment. Each segment is
// authenticated before any of its bytes are returned, but a truncated
// object fails only at its end: io.EOF comes only after the last segment
// authenticated, so a reader that stops at the first error never takes a
// shortened object for a whole one.
type decryptingReader struct {
	plain  io.Reader
	src    *sourceReader
	closer io.Closer
	err    error
	// first is the byte the constructor read to authenticate the first
	// segment; held is whether it is still to be returned.
	first [1]byte
	held  bool
}

// newDecryptingReader reads the header and authenticates the first
// segment, so a wrong key, a moved object or a damaged start fails here,
// before the caller sends anything on; an object of one segment is then
// fully authenticated.
func newDecryptingReader(aead aeadCipher, rc io.ReadCloser, aad []byte) (*decryptingReader, error) {
	src := &sourceReader{r: rc}
	plain, err := aead.NewDecryptingReader(src, aad)
	if err != nil {
		return nil, decryptError(src)
	}
	d := &decryptingReader{plain: plain, src: src, closer: rc}
	n := 0
	for n == 0 && err == nil {
		n, err = plain.Read(d.first[:])
	}
	switch {
	case n == 1:
		d.held = true
	case errors.Is(err, io.EOF):
		d.err = io.EOF
	default:
		return nil, decryptError(src)
	}
	return d, nil
}

func (d *decryptingReader) Read(p []byte) (int, error) {
	if d.held && len(p) > 0 {
		d.held = false
		p[0] = d.first[0]
		if len(p) == 1 {
			return 1, nil
		}
		n, err := d.Read(p[1:])
		return n + 1, err
	}
	if d.err != nil {
		return 0, d.err
	}
	n, err := d.plain.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		d.err = decryptError(d.src)
		return n, d.err
	}
	return n, err
}

func (d *decryptingReader) Close() error { return d.closer.Close() }

// decryptError reports a failed read of the backend as such, and anything
// else as [ErrInvalidCiphertext]; the decryption's own error text says
// nothing useful.
func decryptError(src *sourceReader) error {
	if src.err != nil {
		return redactRead(src.err)
	}
	return ErrInvalidCiphertext
}

// aeadCipher is the streaming cipher of one Put or Get.
type aeadCipher = *subtle.AESGCMHKDF

// complete reports whether the backend read every byte of ciphertext.
func (e *encryptingReader) complete() bool { return e.done && e.sealed.Len() == 0 }
