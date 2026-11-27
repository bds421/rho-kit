package stack

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const transferSize = 25 << 20

// streamingServer serves h through Default with the server's read and
// write timeouts at serverTimeout, so a transfer that outlives them
// passes only on its route's own deadlines.
func streamingServer(t *testing.T, h http.Handler, serverTimeout time.Duration, opts ...Option) string {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewUnstartedServer(Default(h, logger, append([]Option{WithoutMetrics()}, opts...)...))
	srv.Config.ReadTimeout = serverTimeout
	srv.Config.WriteTimeout = serverTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func transferMux(uploaded chan<- int64, uploadErr chan<- error) *http.ServeMux {
	payload := bytes.Repeat([]byte("x"), transferSize)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /files", func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if uploadErr != nil && err != nil {
			uploadErr <- err
		}
		if uploaded != nil {
			uploaded <- n
		}
		w.WriteHeader(http.StatusNoContent)
	})
	download := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }
	mux.HandleFunc("GET /files/{id}/content", download)
	mux.HandleFunc("GET /export", download)
	return mux
}

var transferRoutes = WithStreamingRoutes(
	StreamingRoute{Pattern: "POST /files", Timeout: 10 * time.Second, Idle: 2 * time.Second},
	StreamingRoute{Pattern: "GET /files/{id}/content", Timeout: 10 * time.Second, Idle: 2 * time.Second},
)

func TestStreamingRoutesMoveLargeBodiesThroughDefault(t *testing.T) {
	uploaded := make(chan int64, 1)
	url := streamingServer(t, transferMux(uploaded, nil), 15*time.Second, transferRoutes)

	resp, err := http.Post(url+"/files", "application/octet-stream", bytes.NewReader(bytes.Repeat([]byte("u"), transferSize)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || <-uploaded != transferSize {
		t.Fatalf("upload: status %d", resp.StatusCode)
	}

	resp, err = http.Get(url + "/files/a1/content")
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || n != transferSize {
		t.Fatalf("download: status %d, %d bytes, %v", resp.StatusCode, n, err)
	}

	// Every other route keeps the buffering timeout and its 1 MiB limit.
	resp, err = http.Get(url + "/export")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("ordinary route: status %d, want 500 from the buffer limit", resp.StatusCode)
	}
}

// pausedBody sends half the body, waits for pause, then the rest.
type pausedBody struct {
	half  []byte
	pause <-chan time.Time
	sent  int
}

func (b *pausedBody) Read(p []byte) (int, error) {
	if b.sent == 2 {
		return 0, io.EOF
	}
	if b.sent == 1 && b.pause != nil {
		<-b.pause
	}
	b.sent++
	return copy(p, b.half), nil
}

func TestStreamingRouteOutlivesTheServerTimeouts(t *testing.T) {
	uploaded := make(chan int64, 1)
	serverTimeout := 100 * time.Millisecond
	url := streamingServer(t, transferMux(uploaded, nil), serverTimeout, transferRoutes)

	body := &pausedBody{half: []byte("half"), pause: time.After(3 * serverTimeout)}
	resp, err := http.Post(url+"/files", "application/octet-stream", body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || <-uploaded != 8 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestStreamingRouteCutsOffAStalledUpload(t *testing.T) {
	uploadErr := make(chan error, 1)
	url := streamingServer(t, transferMux(make(chan int64, 1), uploadErr), 15*time.Second,
		WithStreamingRoutes(StreamingRoute{Pattern: "POST /files", Timeout: 10 * time.Second, Idle: 100 * time.Millisecond}))

	stall := make(chan time.Time)
	t.Cleanup(func() { close(stall) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/files", &pausedBody{half: []byte("half"), pause: stall})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	if err := <-uploadErr; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read error %v, want a deadline", err)
	}
}

func TestStreamingRouteCutsOffAStalledDownload(t *testing.T) {
	writeErr := make(chan error, 1)
	payload := bytes.Repeat([]byte("x"), transferSize)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(payload)
		writeErr <- err
	})
	url := streamingServer(t, h, 15*time.Second,
		WithStreamingRoutes(StreamingRoute{Pattern: "GET /files", Timeout: 10 * time.Second, Idle: 100 * time.Millisecond}))

	resp, err := http.Get(url + "/files")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The client reads nothing more; the socket buffers fill and the write
	// waits until Idle ends it.
	if err := <-writeErr; err == nil {
		t.Fatal("write to a stalled client succeeded")
	}
}

func TestStreamingRoutesMatchLikeServeMux(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	large := bytes.Repeat([]byte("a"), 2<<20)
	h := Default(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(large)
	}), logger, WithoutMetrics(), WithStreamingRoutes(
		StreamingRoute{Pattern: "GET /files/{id}/content", Timeout: time.Minute, Idle: time.Second},
	))
	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/files/a1/content", http.StatusOK},
		{http.MethodHead, "/files/a1/content", http.StatusOK},
		{http.MethodPost, "/files/a1/content", http.StatusInternalServerError},
		{http.MethodGet, "/files/a1/other", http.StatusInternalServerError},
		{http.MethodGet, "/files/a1/../a1/content", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestStreamingRouteBoundsTheHandlerContext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var deadline time.Time
	h := Default(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
	}), logger, WithoutMetrics(), WithoutTimeout(), WithStreamingRoutes(
		StreamingRoute{Pattern: "POST /files", Timeout: time.Minute, Idle: time.Second},
	))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/files", strings.NewReader("x")))
	if left := time.Until(deadline); left <= 50*time.Second || left > time.Minute {
		t.Fatalf("deadline in %v, want about a minute", left)
	}
}

func TestWithStreamingRoutesRejectsInvalidRoutes(t *testing.T) {
	ok := StreamingRoute{Pattern: "POST /files", Timeout: time.Minute, Idle: time.Second}
	for name, routes := range map[string][]StreamingRoute{
		"empty pattern":    {{Timeout: time.Minute, Idle: time.Second}},
		"invalid pattern":  {{Pattern: "POST files", Timeout: time.Minute, Idle: time.Second}},
		"conflict":         {ok, ok},
		"no timeout":       {{Pattern: "POST /files", Idle: time.Second}},
		"no idle":          {{Pattern: "POST /files", Timeout: time.Minute}},
		"idle over limit":  {{Pattern: "POST /files", Timeout: time.Second, Idle: time.Minute}},
		"negative minrate": {{Pattern: "POST /files", Timeout: time.Minute, Idle: time.Second, MinRate: -1}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s accepted", name)
				}
			}()
			WithStreamingRoutes(routes...)
		}()
	}
}

// fakeClock is a clock tests move by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestTransferBudget(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	var deadlines []time.Time
	b := &transferBudget{
		idle: 2 * time.Second, minRate: 100, end: clock.t.Add(time.Minute),
		set: func(d time.Time) error { deadlines = append(deadlines, d); return nil }, now: clock.now,
	}

	start, err := b.arm()
	if err != nil || !deadlines[0].Equal(clock.t.Add(2*time.Second)) {
		t.Fatalf("first arm: %v, deadline %v", err, deadlines)
	}
	clock.t = clock.t.Add(1500 * time.Millisecond)
	b.done(start, 100) // 1.5 s spent; budget is 2 s + 100 B / 100 B/s = 3 s
	if !deadlines[1].Equal(b.end) {
		t.Fatalf("not disarmed to the end: %v", deadlines[1])
	}

	start, err = b.arm()
	if err != nil || !deadlines[2].Equal(clock.t.Add(1500*time.Millisecond)) {
		t.Fatalf("second arm: %v, deadline %v, want what is left of the budget", err, deadlines[2])
	}
	clock.t = clock.t.Add(1500 * time.Millisecond)
	b.done(start, 0)

	if _, err = b.arm(); !errors.Is(err, ErrTransferTooSlow) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("exhausted budget: %v", err)
	}
	if last := deadlines[len(deadlines)-1]; !last.Equal(clock.t) {
		t.Fatalf("exhausted budget left deadline %v, want now", last)
	}
}

func TestTransferBudgetNeverPastTheEnd(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	var deadline time.Time
	b := &transferBudget{
		idle: time.Minute, end: clock.t.Add(time.Second),
		set: func(d time.Time) error { deadline = d; return nil }, now: clock.now,
	}
	if _, err := b.arm(); err != nil || !deadline.Equal(b.end) {
		t.Fatalf("deadline %v, want the end %v", deadline, b.end)
	}
}

func TestAtRateDoesNotOverflow(t *testing.T) {
	if got := atRate(1<<40, 1<<20); got != (1<<20)*time.Second {
		t.Fatalf("atRate = %v", got)
	}
	if got := atRate(150, 100); got != 1500*time.Millisecond {
		t.Fatalf("atRate = %v", got)
	}
}

// deadlineRecorder records the connection deadlines a ResponseController
// sets through it.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	read, write []time.Time
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.read = append(d.read, t)
	return nil
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.write = append(d.write, t)
	return nil
}

func TestStreamingRouteBoundsTheTailByIdle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Default(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}), logger, WithoutMetrics(), WithStreamingRoutes(
		StreamingRoute{Pattern: "POST /files", Timeout: time.Minute, Idle: time.Second},
	))
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/files", strings.NewReader("body")))
	if len(rec.read) < 2 || len(rec.write) < 2 {
		t.Fatalf("deadlines not set: read %v, write %v", rec.read, rec.write)
	}
	// After the handler both deadlines are Idle (1 s) from its end, not the
	// route's minute and not cleared.
	for name, last := range map[string]time.Time{"read": rec.read[len(rec.read)-1], "write": rec.write[len(rec.write)-1]} {
		if left := time.Until(last); left <= 0 || left > time.Second {
			t.Errorf("%s deadline after the handler in %v, want within Idle", name, left)
		}
	}
}

// TestStreamingRouteCutsOffAStalledClientAfterTheHandler sends part of a
// chunked body and then nothing: the handler answers without reading it,
// and net/http's drain of the rest must end after Idle instead of holding
// the connection.
func TestStreamingRouteCutsOffAStalledClientAfterTheHandler(t *testing.T) {
	const idle = 200 * time.Millisecond
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	url := streamingServer(t, h, 0,
		WithStreamingRoutes(StreamingRoute{Pattern: "POST /files", Timeout: 10 * time.Second, Idle: idle}))

	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = io.WriteString(conn, "POST /files HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nhalf\r\n"); err != nil {
		t.Fatal(err)
	}
	// The response arrives, then the server drains the body the client
	// never finishes; it must close the connection after about Idle.
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = io.Copy(io.Discard, conn)
	if err != nil {
		t.Fatalf("connection not closed by the server: %v", err)
	}
	if waited := time.Since(start); waited > 10*idle {
		t.Fatalf("connection held for %v after the handler, want about %v", waited, idle)
	}
}
