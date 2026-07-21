package clamav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/storagehttp/uploadsec"
)

const (
	defaultNetwork          = "tcp"
	defaultChunkSize        = 32 << 10
	maxChunkSize            = 1 << 20
	defaultScanTimeout      = 30 * time.Second
	defaultMaxSpool         = 256 << 20
	maxResponseBytes        = 4 << 10
	defaultMetricsValidator = "clamav"
)

// DialContextFunc dials a clamd endpoint.
type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Scanner streams uploads to clamd using INSTREAM.
type Scanner struct {
	network     string
	address     string
	dial        DialContextFunc
	chunkSize   int
	scanTimeout time.Duration

	// customDial is set by WithDialer so transport-safety checks know
	// the caller owns dialing (e.g. TLS/mTLS) and may reach remote hosts.
	customDial bool
	// allowInsecurePlaintext opts into cleartext TCP to a non-loopback
	// clamd. Prefer unix sockets or WithDialer with TLS instead.
	allowInsecurePlaintext bool

	// metrics is the optional Prometheus collector set. nil means
	// observability is disabled (the default). When set, every Scan
	// call records duration and outcome via metrics.observeScan.
	metrics *Metrics
	// metricsValidator is the label value used for the "validator"
	// dimension on clamav_* metrics. Defaults to "clamav" so a
	// single scanner shows up under a stable, predictable label.
	metricsValidator string
}

// Option configures a Scanner.
type Option func(*Scanner)

// New returns a ClamAV scanner for address, for example "127.0.0.1:3310".
//
// Transport safety: the default network is plaintext TCP. Loopback
// addresses and unix sockets are permitted as-is. A non-loopback TCP
// address requires either [WithDialer] (e.g. TLS/mTLS) or an explicit
// [WithAllowInsecurePlaintext] opt-in — remote uploads otherwise traverse
// the network in cleartext and an on-path attacker could tamper with the
// scan verdict. Prefer a local unix socket when clamd is co-located.
func New(address string, opts ...Option) *Scanner {
	address = strings.TrimSpace(address)
	if address == "" {
		panic("clamav: New requires a non-empty address")
	}
	dialer := &net.Dialer{}
	s := &Scanner{
		network:          defaultNetwork,
		address:          address,
		dial:             dialer.DialContext,
		chunkSize:        defaultChunkSize,
		scanTimeout:      defaultScanTimeout,
		metricsValidator: defaultMetricsValidator,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("clamav: New option must not be nil")
		}
		opt(s)
	}
	s.enforceTransportSafety()
	return s
}

func (s *Scanner) enforceTransportSafety() {
	switch s.network {
	case "tcp", "tcp4", "tcp6":
	default:
		return // unix / other non-TCP are local or caller-owned
	}
	if s.customDial || s.allowInsecurePlaintext {
		return
	}
	host := s.address
	if h, _, err := net.SplitHostPort(s.address); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if isLoopbackHost(host) {
		return
	}
	panic("clamav: non-loopback TCP clamd requires WithDialer (TLS/mTLS) or WithAllowInsecurePlaintext; prefer a unix socket for co-located clamd")
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WithNetwork sets the network used to dial clamd. Defaults to "tcp";
// "unix" is useful for local clamd sockets and is the preferred deployment
// when clamd is co-located (no cleartext payload on the wire).
func WithNetwork(network string) Option {
	network = strings.TrimSpace(network)
	if network == "" {
		panic("clamav: WithNetwork requires a non-empty network")
	}
	return func(s *Scanner) {
		s.network = network
	}
}

// WithDialer overrides network dialing. It is primarily useful for tests and
// for services that need custom mTLS or proxy dialing to a remote clamd.
// Setting a custom dialer satisfies the non-loopback TCP transport check
// (the caller owns encryption and path trust).
func WithDialer(dial DialContextFunc) Option {
	if dial == nil {
		panic("clamav: WithDialer requires a non-nil dialer")
	}
	return func(s *Scanner) {
		s.dial = dial
		s.customDial = true
	}
}

// WithAllowInsecurePlaintext permits cleartext TCP to a non-loopback
// clamd address. Use only on a private/trusted network path; prefer
// [WithNetwork]("unix") or [WithDialer] with TLS instead. Without this
// opt-in (or a custom dialer), [New] panics for non-loopback TCP.
func WithAllowInsecurePlaintext() Option {
	return func(s *Scanner) {
		s.allowInsecurePlaintext = true
	}
}

// WithChunkSize sets the INSTREAM chunk size. Values above 1 MiB are rejected
// so a misconfiguration cannot allocate very large buffers per upload.
func WithChunkSize(n int) Option {
	if n <= 0 || n > maxChunkSize {
		panic("clamav: WithChunkSize requires a valid chunk size")
	}
	return func(s *Scanner) {
		s.chunkSize = n
	}
}

// WithScanTimeout bounds the whole dial/write/read scan exchange, including
// reads from the caller-supplied body. The default is 30 seconds.
func WithScanTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("clamav: WithScanTimeout requires a positive duration")
	}
	return func(s *Scanner) {
		s.scanTimeout = d
	}
}

// WithMetrics enables Prometheus instrumentation on the Scanner. Each
// Scan records latency to clamav_scan_duration_seconds and increments
// clamav_scans_total with the outcome (clean | infected | error). Pass
// nil to disable (the default state). m must come from NewMetrics so
// the collectors share one registerer with the rest of the kit.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("clamav: WithMetrics requires non-nil metrics")
	}
	return func(s *Scanner) {
		s.metrics = m
	}
}

// WithMetricsValidatorName sets the "validator" label value attached to
// every clamav_* metric emitted by this Scanner. Default is "clamav".
// Use a different label when multiple validators run side-by-side so
// dashboards can split them (e.g. "primary-clamav", "shadow-clamav").
//
// The value is rejected if it is empty or contains characters Prometheus
// would refuse as a label value at scrape time.
func WithMetricsValidatorName(name string) Option {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("clamav: WithMetricsValidatorName requires a non-empty name")
	}
	// Prometheus label values may not contain raw newlines or NULs that
	// would corrupt exposition format; keep the value operator-safe.
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7f {
			panic("clamav: WithMetricsValidatorName rejects control characters")
		}
	}
	return func(s *Scanner) {
		s.metricsValidator = name
	}
}

// StorageValidator returns a storage.Validator that scans the whole upload
// before the object reaches the backend. It spools to a bounded temp file so
// clean content can be replayed after clamd has produced a verdict.
func (s *Scanner) StorageValidator(opts ...StorageValidatorOption) storage.Validator {
	return StorageValidator(s, opts...)
}

// StorageValidator adapts any uploadsec.Scanner to the storage.Validator
// contract used by storagehttp.ParseAndStore.
func StorageValidator(scanner uploadsec.Scanner, opts ...StorageValidatorOption) storage.Validator {
	if scanner == nil {
		panic("clamav: StorageValidator requires a non-nil scanner")
	}
	cfg := storageValidatorConfig{maxSpoolBytes: defaultMaxSpool}
	for _, opt := range opts {
		if opt == nil {
			panic("clamav: StorageValidator option must not be nil")
		}
		opt(&cfg)
	}
	return func(ctx context.Context, r io.Reader, meta *storage.ObjectMeta) (io.Reader, error) {
		if r == nil {
			return nil, fmt.Errorf("%w: nil upload reader", storage.ErrValidation)
		}
		if meta == nil {
			return nil, fmt.Errorf("%w: nil object metadata", storage.ErrValidation)
		}
		tmp, err := os.CreateTemp(cfg.tempDir, "rho-kit-clamav-*")
		if err != nil {
			return nil, fmt.Errorf("%w: create scan spool failed", uploadsec.ErrScannerUnavailable)
		}
		remove := true
		defer func() {
			if remove {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
			}
		}()

		if err := copyBounded(tmp, r, cfg.maxSpoolBytes); err != nil {
			return nil, err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("%w: rewind scan spool failed", uploadsec.ErrScannerUnavailable)
		}
		scanMeta := uploadsec.Meta{
			ContentType: meta.ContentType,
			Size:        meta.Size,
		}
		if err := scanner.Scan(ctx, tmp, scanMeta); err != nil {
			if errors.Is(err, uploadsec.ErrMalwareDetected) {
				return nil, fmt.Errorf("%w: %w", storage.ErrValidation, err) // kit:ok-fmt-errorf-wrap
			}
			// Preserve errors.Is(ErrScannerUnavailable) and the underlying
			// cause for triage, but redact cause text so Error() never
			// reflects scanner/backend details across the trust boundary.
			return nil, redact.WrapSentinel(uploadsec.ErrScannerUnavailable, err)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("%w: rewind clean spool failed", uploadsec.ErrScannerUnavailable)
		}
		remove = false
		replay := &removeOnEOF{File: tmp, path: tmp.Name()}
		runtime.SetFinalizer(replay, (*removeOnEOF).cleanup)
		return replay, nil
	}
}

type storageValidatorConfig struct {
	tempDir       string
	maxSpoolBytes int64
}

// StorageValidatorOption configures [StorageValidator].
type StorageValidatorOption func(*storageValidatorConfig)

// WithTempDir sets the directory used for scan spool files. Empty means the
// platform default temp directory.
func WithTempDir(dir string) StorageValidatorOption {
	return func(c *storageValidatorConfig) {
		c.tempDir = dir
	}
}

// WithMaxSpoolBytes caps the temp-file bytes accepted by StorageValidator.
// Defaults to 256 MiB.
func WithMaxSpoolBytes(n int64) StorageValidatorOption {
	if n <= 0 {
		panic("clamav: WithMaxSpoolBytes requires n > 0")
	}
	return func(c *storageValidatorConfig) {
		c.maxSpoolBytes = n
	}
}

// Scan implements uploadsec.Scanner.
func (s *Scanner) Scan(ctx context.Context, body io.Reader, _ uploadsec.Meta) (retErr error) {
	if s == nil || s.dial == nil || s.address == "" || s.network == "" || s.chunkSize <= 0 {
		return fmt.Errorf("%w: clamav scanner is not initialized", uploadsec.ErrScannerUnavailable)
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is required", uploadsec.ErrScannerUnavailable)
	}
	if body == nil {
		return fmt.Errorf("%w: nil upload body", uploadsec.ErrScannerUnavailable)
	}

	// Metrics span the whole exchange — dial, INSTREAM write, response
	// read — so the duration histogram captures everything the operator
	// can blame on the scanner. Wrap the rest of Scan in a deferred
	// observer rather than recording from every error site to keep the
	// outcome classification in one place (classifyScanOutcome).
	started := time.Now()
	defer func() {
		s.metrics.observeScan(s.metricsValidator, started, retErr)
	}()

	ctx, cancel := context.WithTimeout(ctx, s.scanTimeout)
	defer cancel()

	conn, err := s.dial(ctx, s.network, s.address)
	if err != nil {
		return fmt.Errorf("%w: dial clamd: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("%w: set clamd deadline: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
		}
	}

	if err := writeAll(conn, []byte("zINSTREAM\x00")); err != nil {
		return fmt.Errorf("%w: send INSTREAM command: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
	}
	if err := streamBody(ctx, conn, body, s.chunkSize); err != nil {
		return err
	}
	response, err := readResponse(conn)
	if err != nil {
		return fmt.Errorf("%w: read clamd response: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
	}
	return parseResponse(response)
}

func streamBody(ctx context.Context, w io.Writer, body io.Reader, chunkSize int) error {
	// Run the body→clamd copy under ctx so a stalled reader cannot hang
	// past WithScanTimeout. On cancel the conn deadline (set by Scan)
	// unblocks writes; a pathological body that never returns from Read
	// may leave a short-lived goroutine until that Read completes.
	errCh := make(chan error, 1)
	go func() {
		errCh <- streamBodyUncancellable(w, body, chunkSize)
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: scan timed out or cancelled: %w", uploadsec.ErrScannerUnavailable, ctx.Err()) // kit:ok-fmt-errorf-wrap
	case err := <-errCh:
		return err
	}
}

func streamBodyUncancellable(w io.Writer, body io.Reader, chunkSize int) error {
	buf := make([]byte, chunkSize)
	var lenbuf [4]byte
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			binary.BigEndian.PutUint32(lenbuf[:], uint32(n))
			if err := writeAll(w, lenbuf[:]); err != nil {
				return fmt.Errorf("%w: send INSTREAM chunk length: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
			}
			if err := writeAll(w, buf[:n]); err != nil {
				return fmt.Errorf("%w: send INSTREAM chunk: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			binary.BigEndian.PutUint32(lenbuf[:], 0)
			if err := writeAll(w, lenbuf[:]); err != nil {
				return fmt.Errorf("%w: send INSTREAM terminator: %w", uploadsec.ErrScannerUnavailable, err) // kit:ok-fmt-errorf-wrap
			}
			return nil
		}
		return fmt.Errorf("%w: read upload body: %w", uploadsec.ErrScannerUnavailable, readErr) // kit:ok-fmt-errorf-wrap
	}
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func readResponse(r io.Reader) (string, error) {
	var b strings.Builder
	var one [1]byte
	for i := 0; i < maxResponseBytes; i++ {
		n, err := r.Read(one[:])
		if n == 1 {
			switch one[0] {
			case 0, '\n':
				return b.String(), nil
			default:
				b.WriteByte(one[0])
			}
		}
		if err != nil {
			// clamd normally NUL-terminates its verdict, but a clean
			// connection close (io.EOF) after a complete-but-unterminated
			// response would otherwise discard the accumulated verdict and
			// fail closed. Treat a non-empty buffer at EOF as a terminated
			// response so e.g. "stream: OK" is not lost on socket close.
			if errors.Is(err, io.EOF) && b.Len() > 0 {
				return b.String(), nil
			}
			return "", err
		}
	}
	return "", fmt.Errorf("clamd response exceeds maximum size")
}

func parseResponse(response string) error {
	response = strings.TrimSpace(response)
	switch {
	case response == "OK", strings.HasSuffix(response, ": OK"):
		return nil
	case strings.HasSuffix(response, " FOUND"):
		threat := strings.TrimSuffix(response, " FOUND")
		if i := strings.LastIndex(threat, ":"); i >= 0 {
			threat = threat[i+1:]
		}
		return uploadsec.MalwareDetected(strings.TrimSpace(threat))
	default:
		return fmt.Errorf("%w: unexpected clamd response", uploadsec.ErrScannerUnavailable)
	}
}

func copyBounded(dst io.Writer, src io.Reader, maxBytes int64) error {
	// Defend against maxBytes near math.MaxInt64: maxBytes+1 would wrap
	// to math.MinInt64 and io.LimitReader would treat that as "no
	// data" instead of "no limit", silently truncating uploads. Cap
	// the limit at math.MaxInt64 so the +1 overflow becomes a no-op
	// instead of a silent truncation (L110, L113).
	limit := maxBytes
	if limit < math.MaxInt64 {
		limit = limit + 1
	}
	lr := io.LimitReader(src, limit)
	n, err := io.Copy(dst, lr)
	if err != nil {
		// Preserve cancellation/deadline identity for callers; do not
		// reflect underlying I/O error text (may include path fragments).
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: spool upload failed", uploadsec.ErrScannerUnavailable)
	}
	if n > maxBytes {
		return fmt.Errorf("%w: upload exceeds scan spool limit", storage.ErrValidation)
	}
	return nil
}

type removeOnEOF struct {
	*os.File
	path string
	// once guards the Close + os.Remove pair so the EOF-on-Read,
	// explicit Close, and finalizer paths never race. sync.Once is
	// enough here — we only need at-most-once cleanup, and the
	// underlying file's Close is itself idempotent enough that a
	// concurrent Read+Close pair won't double-free.
	once sync.Once
}

func (r *removeOnEOF) Read(p []byte) (int, error) {
	// Do NOT clean up on EOF: AWS SDK and other clients rewind seekable
	// bodies to retry transient Put failures. Deleting the spool on first
	// EOF makes Seek fail and converts a retryable error into a permanent
	// failure. Cleanup is Close + finalizer only.
	return r.File.Read(p)
}

func (r *removeOnEOF) Close() error {
	var closeErr error
	r.once.Do(func() {
		closeErr = r.File.Close()
		runtime.SetFinalizer(r, nil)
		_ = os.Remove(r.path)
	})
	// Second Close is a no-op (sync.Once); do not surface "file already closed".
	return closeErr
}

func (r *removeOnEOF) cleanup() {
	r.once.Do(func() {
		_ = r.File.Close()
		runtime.SetFinalizer(r, nil)
		_ = os.Remove(r.path)
	})
}
