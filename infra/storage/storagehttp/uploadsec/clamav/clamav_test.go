package clamav

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/storagehttp/uploadsec"
)

func TestScannerClean(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: OK\x00", seen)

	err := New(addr, WithScanTimeout(time.Second)).Scan(
		context.Background(),
		strings.NewReader("hello"),
		uploadsec.Meta{Filename: "hello.txt"},
	)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	req := <-seen
	if req.command != "zINSTREAM" {
		t.Fatalf("command = %q, want zINSTREAM", req.command)
	}
	if string(req.body) != "hello" {
		t.Fatalf("body = %q, want hello", req.body)
	}
}

func TestScannerInfected(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: Eicar-Test-Signature FOUND\x00", seen)

	err := New(addr, WithScanTimeout(time.Second)).Scan(
		context.Background(),
		strings.NewReader("bad"),
		uploadsec.Meta{},
	)
	if !errors.Is(err, uploadsec.ErrMalwareDetected) {
		t.Fatalf("Scan error = %v, want ErrMalwareDetected", err)
	}
	var detected *uploadsec.MalwareDetectedError
	if !errors.As(err, &detected) {
		t.Fatalf("Scan error type = %T, want MalwareDetectedError", err)
	}
	if detected.Threat != "Eicar-Test-Signature" {
		t.Fatalf("threat = %q, want Eicar-Test-Signature", detected.Threat)
	}
	if strings.Contains(err.Error(), "Eicar") {
		t.Fatalf("Scan error leaked malware signature: %v", err)
	}
	<-seen
}

func TestScannerProtocolErrorFailsClosed(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: secret-token protocol failure ERROR\x00", seen)

	err := New(addr, WithScanTimeout(time.Second)).Scan(
		context.Background(),
		strings.NewReader("oversized"),
		uploadsec.Meta{},
	)
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("Scan error = %v, want ErrScannerUnavailable", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("Scan error leaked scanner response: %v", err)
	}
	<-seen
}

func TestScannerChunksBody(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: OK\x00", seen)

	err := New(addr, WithChunkSize(3), WithScanTimeout(time.Second)).Scan(
		context.Background(),
		strings.NewReader("abcdefg"),
		uploadsec.Meta{},
	)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	req := <-seen
	if got := string(req.body); got != "abcdefg" {
		t.Fatalf("chunked body = %q, want abcdefg", got)
	}
	if req.chunks != 3 {
		t.Fatalf("chunks = %d, want 3", req.chunks)
	}
}

func TestScannerDialError(t *testing.T) {
	errDial := errors.New("dial blocked")
	scanner := New("clamd.invalid:3310",
		WithDialer(func(context.Context, string, string) (net.Conn, error) {
			return nil, errDial
		}),
		WithScanTimeout(time.Second),
	)
	err := scanner.Scan(context.Background(), strings.NewReader("x"), uploadsec.Meta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("Scan error = %v, want ErrScannerUnavailable", err)
	}
	if !errors.Is(err, errDial) {
		t.Fatalf("Scan error = %v, want wrapped dial error", err)
	}
}

func TestOptionPanics(t *testing.T) {
	assertPanic(t, func() { New("") })
	assertPanic(t, func() { New("127.0.0.1:3310", nil) })
	assertPanic(t, func() { WithNetwork("") })
	assertPanic(t, func() { WithDialer(nil) })
	assertPanic(t, func() { WithChunkSize(0) })
	assertPanic(t, func() { WithChunkSize(maxChunkSize + 1) })
	assertPanic(t, func() { WithScanTimeout(0) })
	assertPanic(t, func() { StorageValidator(nil) })
	assertPanic(t, func() { StorageValidator(fakeScanner{}, nil) })
	assertPanic(t, func() { WithMaxSpoolBytes(0) })
	assertPanic(t, func() { WithMetrics(nil) })
	assertPanic(t, func() { WithMetricsValidatorName("") })
	assertPanic(t, func() { WithMetricsValidatorName("   ") })
}

// TestScannerMetricsCleanScan asserts the duration histogram observes a
// sample and clamav_scans_total{outcome="clean"} increments after a
// successful scan. A fresh prometheus.NewRegistry() is used so the
// values are guaranteed not to be polluted by other tests in this file
// or by the default registry.
func TestScannerMetricsCleanScan(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: OK\x00", seen)

	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	err := New(addr,
		WithScanTimeout(time.Second),
		WithMetrics(m),
	).Scan(context.Background(), strings.NewReader("hello"), uploadsec.Meta{})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	<-seen

	assertHistogramCount(t, m.scanDuration, map[string]string{"validator": "clamav"}, 1)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "clean"}, 1)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "infected"}, 0)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "error"}, 0)
}

// TestScannerMetricsInfectedScan locks in the infected outcome label so
// alert rules keyed on outcome="infected" cannot silently flip to
// outcome="error" if a future refactor reclassifies malware as an
// error. The two outcomes drive different on-call responses.
func TestScannerMetricsInfectedScan(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: Eicar-Test-Signature FOUND\x00", seen)

	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	err := New(addr,
		WithScanTimeout(time.Second),
		WithMetrics(m),
	).Scan(context.Background(), strings.NewReader("bad"), uploadsec.Meta{})
	if !errors.Is(err, uploadsec.ErrMalwareDetected) {
		t.Fatalf("Scan error = %v, want ErrMalwareDetected", err)
	}
	<-seen

	assertHistogramCount(t, m.scanDuration, map[string]string{"validator": "clamav"}, 1)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "infected"}, 1)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "clean"}, 0)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "error"}, 0)
}

// TestScannerMetricsErrorOutcome asserts that scanner unavailability
// (a dial failure here) records outcome="error", distinct from
// "infected". These are separate metrics so they can drive separate
// alerts: a dial-error burst is a clamd outage that fails closed; an
// infected burst can be a coordinated upload attack.
func TestScannerMetricsErrorOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	errDial := errors.New("dial blocked")
	err := New("clamd.invalid:3310",
		WithDialer(func(context.Context, string, string) (net.Conn, error) {
			return nil, errDial
		}),
		WithScanTimeout(time.Second),
		WithMetrics(m),
	).Scan(context.Background(), strings.NewReader("x"), uploadsec.Meta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("Scan error = %v, want ErrScannerUnavailable", err)
	}

	assertHistogramCount(t, m.scanDuration, map[string]string{"validator": "clamav"}, 1)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "error"}, 1)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "clean"}, 0)
	assertCounter(t, m.scansTotal, map[string]string{"validator": "clamav", "outcome": "infected"}, 0)
}

// TestScannerMetricsValidatorLabel verifies the validator label is
// honoured so dashboards with multiple side-by-side scanners can
// split them.
func TestScannerMetricsValidatorLabel(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: OK\x00", seen)

	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	err := New(addr,
		WithScanTimeout(time.Second),
		WithMetrics(m),
		WithMetricsValidatorName("primary-clamav"),
	).Scan(context.Background(), strings.NewReader("hello"), uploadsec.Meta{})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	<-seen

	assertCounter(t, m.scansTotal,
		map[string]string{"validator": "primary-clamav", "outcome": "clean"}, 1)
	// Default label must NOT have been touched.
	assertCounter(t, m.scansTotal,
		map[string]string{"validator": "clamav", "outcome": "clean"}, 0)
}

// TestScannerMetricsOptOutDefault confirms that a Scanner without
// WithMetrics is a no-op for instrumentation. Otherwise services that
// don't yet care about clamav metrics would still be forced to register
// the collectors against the default registry, polluting their /metrics
// output with empty series.
func TestScannerMetricsOptOutDefault(t *testing.T) {
	seen := make(chan clamdRequest, 1)
	addr := startClamd(t, "stream: OK\x00", seen)

	// No WithMetrics — must be safe and emit nothing.
	err := New(addr, WithScanTimeout(time.Second)).Scan(
		context.Background(), strings.NewReader("hi"), uploadsec.Meta{},
	)
	if err != nil {
		t.Fatalf("Scan without metrics returned error: %v", err)
	}
	<-seen
}

// assertCounter checks a labelled CounterVec value, panicking when the
// label set does not match the declared label names so a typo in the
// test surfaces fast.
func assertCounter(t *testing.T, cv *prometheus.CounterVec, labels map[string]string, want float64) {
	t.Helper()
	c, err := cv.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("GetMetricWith(%v): %v", labels, err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("Write counter %v: %v", labels, err)
	}
	if got := m.GetCounter().GetValue(); got != want {
		t.Fatalf("counter %v = %v, want %v", labels, got, want)
	}
}

// assertHistogramCount checks a labelled HistogramVec sample count.
func assertHistogramCount(t *testing.T, hv *prometheus.HistogramVec, labels map[string]string, want uint64) {
	t.Helper()
	o, err := hv.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("GetMetricWith(%v): %v", labels, err)
	}
	h, ok := o.(prometheus.Histogram)
	if !ok {
		t.Fatalf("metric %v is not a Histogram: %T", labels, o)
	}
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatalf("Write histogram %v: %v", labels, err)
	}
	if got := m.GetHistogram().GetSampleCount(); got != want {
		t.Fatalf("histogram %v sample count = %d, want %d", labels, got, want)
	}
}

func TestStorageValidatorScansAndReplaysCleanBody(t *testing.T) {
	dir := t.TempDir()
	scanner := fakeScanner{
		fn: func(_ context.Context, body io.Reader, meta uploadsec.Meta) error {
			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("scanner read body: %v", err)
			}
			if string(got) != "clean body" {
				t.Fatalf("scanner body = %q, want clean body", got)
			}
			if meta.ContentType != "text/plain" {
				t.Fatalf("scanner content type = %q, want text/plain", meta.ContentType)
			}
			return nil
		},
	}

	meta := storage.ObjectMeta{ContentType: "text/plain"}
	reader, err := StorageValidator(scanner, WithTempDir(dir))(context.Background(), strings.NewReader("clean body"), &meta)
	if err != nil {
		t.Fatalf("StorageValidator returned error: %v", err)
	}
	replayed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read replayed body: %v", err)
	}
	if string(replayed) != "clean body" {
		t.Fatalf("replayed body = %q, want clean body", replayed)
	}
	assertDirEmpty(t, dir)
}

func TestStorageValidatorCleansTempFileWhenLaterValidatorRejects(t *testing.T) {
	dir := t.TempDir()
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		return nil
	}}
	reject := storage.Validator(func(context.Context, io.Reader, *storage.ObjectMeta) (io.Reader, error) {
		return nil, storage.ErrValidation
	})
	meta := storage.ObjectMeta{}

	_, err := storage.ApplyValidators(context.Background(), strings.NewReader("clean body"), &meta, []storage.Validator{
		StorageValidator(scanner, WithTempDir(dir)),
		reject,
	})
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("ApplyValidators error = %v, want storage.ErrValidation", err)
	}
	assertDirEmpty(t, dir)
}

func TestStorageValidatorPassesContextToScanner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanner := fakeScanner{fn: func(ctx context.Context, _ io.Reader, _ uploadsec.Meta) error {
		return ctx.Err()
	}}

	_, err := StorageValidator(scanner)(ctx, strings.NewReader("x"), &storage.ObjectMeta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("error = %v, want ErrScannerUnavailable", err)
	}
}

func TestStorageValidatorWrapsMalwareAsStorageValidation(t *testing.T) {
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		return uploadsec.MalwareDetected("Eicar-Test-Signature")
	}}

	_, err := StorageValidator(scanner)(context.Background(), strings.NewReader("bad"), &storage.ObjectMeta{})
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("error = %v, want storage.ErrValidation", err)
	}
	if !errors.Is(err, uploadsec.ErrMalwareDetected) {
		t.Fatalf("error = %v, want uploadsec.ErrMalwareDetected", err)
	}
	if strings.Contains(err.Error(), "Eicar") {
		t.Fatalf("error leaked malware signature: %v", err)
	}
}

func TestStorageValidatorPropagatesScannerUnavailable(t *testing.T) {
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		return uploadsec.ErrScannerUnavailable
	}}

	_, err := StorageValidator(scanner)(context.Background(), strings.NewReader("x"), &storage.ObjectMeta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("error = %v, want uploadsec.ErrScannerUnavailable", err)
	}
	if errors.Is(err, storage.ErrValidation) {
		t.Fatalf("scanner outage must not be reported as client validation: %v", err)
	}
}

func TestStorageValidatorSpoolCreateErrorDoesNotReflectPath(t *testing.T) {
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		t.Fatal("scanner must not run when spool creation fails")
		return nil
	}}
	dir := filepath.Join(t.TempDir(), "missing", "secret-token")

	_, err := StorageValidator(scanner, WithTempDir(dir))(context.Background(), strings.NewReader("x"), &storage.ObjectMeta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("error = %v, want ErrScannerUnavailable", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), dir) {
		t.Fatalf("spool create error leaked temp path: %v", err)
	}
}

func TestStorageValidatorSpoolReadErrorDoesNotReflectDetails(t *testing.T) {
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		t.Fatal("scanner must not run when upload spool copy fails")
		return nil
	}}
	reader := errReader{err: errors.New("read failed for secret-token")}

	_, err := StorageValidator(scanner)(context.Background(), reader, &storage.ObjectMeta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("error = %v, want ErrScannerUnavailable", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "read failed") {
		t.Fatalf("spool read error leaked details: %v", err)
	}
}

func TestStorageValidatorUnknownScannerErrorDoesNotReflectDetails(t *testing.T) {
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		return errors.New("scanner failed for secret-token")
	}}

	_, err := StorageValidator(scanner)(context.Background(), strings.NewReader("x"), &storage.ObjectMeta{})
	if !errors.Is(err, uploadsec.ErrScannerUnavailable) {
		t.Fatalf("error = %v, want ErrScannerUnavailable", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "scanner failed") {
		t.Fatalf("scanner error details leaked: %v", err)
	}
}

func TestStorageValidatorCapsSpool(t *testing.T) {
	called := false
	scanner := fakeScanner{fn: func(context.Context, io.Reader, uploadsec.Meta) error {
		called = true
		return nil
	}}

	_, err := StorageValidator(scanner, WithMaxSpoolBytes(4))(context.Background(), strings.NewReader("12345"), &storage.ObjectMeta{})
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("error = %v, want storage.ErrValidation", err)
	}
	if called {
		t.Fatal("scanner must not run after spool limit rejection")
	}
}

type clamdRequest struct {
	command string
	body    []byte
	chunks  int
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}

func startClamd(t *testing.T, response string, seen chan<- clamdRequest) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		cmd, err := readUntilZero(conn)
		if err != nil {
			t.Errorf("read command: %v", err)
			return
		}
		var body []byte
		chunks := 0
		var lenbuf [4]byte
		for {
			if _, err := io.ReadFull(conn, lenbuf[:]); err != nil {
				t.Errorf("read chunk length: %v", err)
				return
			}
			n := binary.BigEndian.Uint32(lenbuf[:])
			if n == 0 {
				break
			}
			chunk := make([]byte, n)
			if _, err := io.ReadFull(conn, chunk); err != nil {
				t.Errorf("read chunk: %v", err)
				return
			}
			body = append(body, chunk...)
			chunks++
		}
		seen <- clamdRequest{command: cmd, body: body, chunks: chunks}
		if _, err := conn.Write([]byte(response)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}()
	t.Cleanup(func() { <-done })
	return ln.Addr().String()
}

func readUntilZero(r io.Reader) (string, error) {
	var b strings.Builder
	var one [1]byte
	for {
		_, err := io.ReadFull(r, one[:])
		if err != nil {
			return "", err
		}
		if one[0] == 0 {
			return b.String(), nil
		}
		b.WriteByte(one[0])
	}
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}

type fakeScanner struct {
	fn func(context.Context, io.Reader, uploadsec.Meta) error
}

func (s fakeScanner) Scan(ctx context.Context, body io.Reader, meta uploadsec.Meta) error {
	return s.fn(ctx, body, meta)
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir has %d entries after replay; want empty", len(entries))
	}
}

// TestCopyBounded_OverflowGuard guards L110/L113: maxBytes near
// math.MaxInt64 must not wrap to a negative limit when the function
// computes maxBytes+1 for the LimitReader. Without the overflow guard
// io.LimitReader would treat the negative limit as "no data" and
// silently truncate uploads to zero bytes — a hostile peer could
// trigger that by triggering a config path that ended up with
// math.MaxInt64 as the cap.
func TestCopyBounded_OverflowGuard(t *testing.T) {
	src := strings.NewReader("hello world")
	var dst bytes.Buffer
	// Pass math.MaxInt64 to exercise the overflow boundary. The wrapped
	// upload is far smaller than the limit so the call must succeed,
	// not silently truncate.
	err := copyBounded(&dst, src, math.MaxInt64)
	require.NoError(t, err)
	require.Equal(t, "hello world", dst.String())
}

// TestCopyBounded_RejectsAtCap verifies the existing cap behaviour
// holds for ordinary maxBytes values — an upload exactly one byte
// over the cap is rejected with a validation error.
func TestCopyBounded_RejectsAtCap(t *testing.T) {
	src := strings.NewReader("aaaaaaa") // 7 bytes
	var dst bytes.Buffer
	err := copyBounded(&dst, src, 6)
	require.Error(t, err)
	// The reader read at most maxBytes+1 = 7 bytes; the assertion
	// is on the error path.
}
