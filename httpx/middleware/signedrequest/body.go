package signedrequest

import (
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"math"
	"net/http"
	"os"
)

// defaultInMemoryBodyMax caps how many body bytes stay in memory before
// the verifier spools the rest to a bounded temp file. The threshold
// bounds heap amplification per authenticated request: a caller who
// presents a valid key id but never produces a verifiable MAC can no
// longer pin bodyMaxSize bytes of heap (default 10 MiB) per request —
// they can only pin inMemoryBodyMax (default 64 KiB). Disk usage stays
// bounded by bodyMaxSize and by the OS process limits on open files.
const defaultInMemoryBodyMax = 64 * 1024

// spooledBody is a hashed-and-spooled view of a request body. Bytes
// up to inMemoryMax stay in mem; bytes beyond live in file (created
// lazily). The body hash is computed streamingly so the verifier can
// MAC-compare without retaining the full body in memory.
//
// The verifier owns lifecycle: it computes the hash and discards the
// spooledBody if the MAC fails; only on a successful MAC does it
// attach an io.ReadCloser via Body that hands the spooled bytes to
// the downstream handler and removes the temp file on Close.
type spooledBody struct {
	mem  []byte
	file *os.File
	size int64
}

// readSpooledBody streams r.Body up to max bytes through a SHA-256
// hasher. Up to inMemoryMax bytes are retained in memory; anything
// beyond is appended to a private temp file. Returns ErrBodyTooLarge
// when r.Body exceeds max. The hash covers exactly the bytes streamed
// (i.e. up to max bytes when the body fits).
func readSpooledBody(r *http.Request, max int64, inMemoryMax int64, spoolDir string) (*spooledBody, [32]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return &spooledBody{}, sha256.Sum256(nil), nil
	}
	if inMemoryMax <= 0 {
		inMemoryMax = defaultInMemoryBodyMax
	}
	if inMemoryMax > max {
		inMemoryMax = max
	}

	originalBody := r.Body
	defer func() { _ = originalBody.Close() }()

	hasher := sha256.New()
	// Grow the in-memory buffer on demand (appendChunk caps it at
	// inMemoryMax) instead of preallocating the full cap. A 200-byte webhook
	// no longer allocates the entire inMemoryMax (default 64 KiB, larger when
	// WithInMemoryBodyMax is raised) per request.
	sb := &spooledBody{}

	// limited grants one extra byte over max so we can detect overflow
	// without consuming arbitrarily large input. Guard against MaxInt64
	// so max+1 does not wrap to a negative LimitReader limit.
	limit := max
	if limit < math.MaxInt64 {
		limit = max + 1
	}
	limited := io.LimitReader(originalBody, limit)
	buf := make([]byte, 32*1024)
	for {
		n, err := limited.Read(buf)
		if n > 0 {
			if perr := sb.appendChunk(buf[:n], inMemoryMax, spoolDir, hasher); perr != nil {
				sb.cleanup()
				return nil, [32]byte{}, perr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			sb.cleanup()
			// Client-attributable transport failure (not a server fault).
			// Stable redacted Error() text; errors.Is matches ErrBodyReadFailed
			// and the underlying cause without leaking it into Error().
			return nil, [32]byte{}, newBodyReadError(err)
		}
	}
	if sb.size > max {
		sb.cleanup()
		return nil, [32]byte{}, ErrBodyTooLarge
	}
	var sum [32]byte
	copy(sum[:], hasher.Sum(nil))
	return sb, sum, nil
}

func (sb *spooledBody) appendChunk(chunk []byte, inMemoryMax int64, spoolDir string, hasher hash.Hash) error {
	if _, err := hasher.Write(chunk); err != nil {
		return safeWrap("signedrequest: hash body failed", err)
	}
	sb.size += int64(len(chunk))

	// Fill the in-memory portion first.
	if int64(len(sb.mem)) < inMemoryMax {
		room := inMemoryMax - int64(len(sb.mem))
		take := int64(len(chunk))
		if take > room {
			take = room
		}
		sb.mem = append(sb.mem, chunk[:take]...)
		chunk = chunk[take:]
	}
	if len(chunk) == 0 {
		return nil
	}

	// Spool overflow to a private temp file. We create the file lazily
	// so small bodies never touch disk.
	if sb.file == nil {
		f, err := os.CreateTemp(spoolDir, "rho-signedrequest-body-*.bin")
		if err != nil {
			return safeWrap("signedrequest: spool body failed", err)
		}
		// On Unix, unlinking immediately keeps the file invisible to
		// other processes and ensures cleanup even on crash; we still
		// keep a Remove in cleanup for Windows where Unlink-after-open
		// is unreliable.
		_ = os.Remove(f.Name())
		sb.file = f
	}
	if _, err := sb.file.Write(chunk); err != nil {
		return safeWrap("signedrequest: spool body failed", err)
	}
	return nil
}

// Body returns an io.ReadCloser that yields the spooled body once and
// removes the temp file on Close. Intended to be assigned to r.Body
// after the MAC has been verified. The returned reader is single-use.
func (sb *spooledBody) Body() io.ReadCloser {
	if sb == nil || (len(sb.mem) == 0 && sb.file == nil) {
		return http.NoBody
	}
	return &spooledReader{
		mem:  sb.mem,
		file: sb.file,
	}
}

// cleanup releases any underlying temp file. Safe to call on a nil
// receiver and idempotent.
func (sb *spooledBody) cleanup() {
	if sb == nil || sb.file == nil {
		return
	}
	name := sb.file.Name()
	_ = sb.file.Close()
	_ = os.Remove(name)
	sb.file = nil
}

type spooledReader struct {
	mem         []byte
	memPos      int
	file        *os.File
	fileStarted bool
	closed      bool
}

func (sr *spooledReader) Read(p []byte) (int, error) {
	if sr.closed {
		return 0, io.ErrClosedPipe
	}
	if sr.memPos < len(sr.mem) {
		n := copy(p, sr.mem[sr.memPos:])
		sr.memPos += n
		return n, nil
	}
	if sr.file == nil {
		return 0, io.EOF
	}
	if !sr.fileStarted {
		if _, err := sr.file.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		sr.fileStarted = true
	}
	return sr.file.Read(p)
}

func (sr *spooledReader) Close() error {
	if sr.closed {
		return nil
	}
	sr.closed = true
	if sr.file != nil {
		name := sr.file.Name()
		_ = sr.file.Close()
		_ = os.Remove(name)
		sr.file = nil
	}
	// Drop the mem slice so the GC can reclaim it promptly even if
	// callers keep the reader alive.
	sr.mem = nil
	return nil
}
