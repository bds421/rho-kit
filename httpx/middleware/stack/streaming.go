package stack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// StreamingRoute names a route that moves large bodies, such as a file
// upload or download, and the deadlines it gets instead of the stack's
// buffering timeout. Its handler streams: nothing is buffered, so a
// response of any size reaches the client, and an overrun handler is not
// answered with 503; it sees its context end and its reads or writes fail.
type StreamingRoute struct {
	// Pattern is an [http.ServeMux] pattern, such as
	// "POST /files/{id}" or "GET /files/{id}/content" (GET also matches
	// HEAD). Requests are matched as a ServeMux would match them.
	Pattern string
	// Timeout bounds the whole request: the handler's context and the
	// connection's read and write deadlines end with it. It replaces the
	// server's ReadTimeout and WriteTimeout for this request, so those
	// can stay short for every other route. Required.
	Timeout time.Duration
	// Idle is the longest a read of the request body may wait for the
	// client's next bytes, and a write of the response for the client to
	// accept the next 32 KiB. A client that stalls is cut off after Idle.
	// Required, at most Timeout.
	Idle time.Duration
	// MinRate is the slowest transfer, in bytes per second, measured on
	// the bytes moved: all reads of the body together may wait at most
	// Idle + read/MinRate, and all writes of the response Idle +
	// written/MinRate. Only time spent waiting on the client counts, not
	// the time the handler spends between reads or writes. Zero leaves
	// only the per-read and per-write Idle bound.
	MinRate int64
}

// ErrTransferTooSlow is returned by a streaming route's body reads and
// response writes once the client moved bytes slower than the route's
// MinRate. It wraps [os.ErrDeadlineExceeded], as an expired connection
// deadline does.
var ErrTransferTooSlow = fmt.Errorf("stack: transfer slower than the minimum rate: %w", os.ErrDeadlineExceeded)

// WithStreamingRoutes gives the routes their own deadlines instead of the
// per-request timeout (see [StreamingRoute]); every other route keeps the
// buffering timeout of [WithTimeout]. The deadlines are set on the
// connection through [http.ResponseController], so the server's
// ReadTimeout and WriteTimeout can stay short: they still bound every
// other request. The routes apply with [WithoutTimeout] too.
// When the handler returns, both deadlines move to Idle from then, which
// bounds what net/http still writes and reads for the request. The server
// resets the read deadline for the next request on the connection; it
// resets the write deadline only when its WriteTimeout is set, so keep one
// (httpx.NewServer's default is 35 s): with WriteTimeout 0 the next request
// inherits that write deadline. A handler that hijacks the connection
// keeps the route's deadlines on it and must set its own.
//
// An empty pattern, a pattern [http.ServeMux] rejects or two that
// conflict, a non-positive Timeout or Idle, an Idle above Timeout, or a
// negative MinRate panics. Repeated options add routes.
func WithStreamingRoutes(routes ...StreamingRoute) Option {
	for _, r := range routes {
		if r.Pattern == "" || r.Timeout <= 0 || r.Idle <= 0 || r.Idle > r.Timeout || r.MinRate < 0 {
			panic("stack: WithStreamingRoutes requires a pattern, Timeout > 0, 0 < Idle <= Timeout and MinRate >= 0")
		}
	}
	cloned := append([]StreamingRoute(nil), routes...)
	// Fail on an invalid or conflicting pattern here, not at Default.
	newStreamingMatcher(cloned)
	return func(cfg *Config) { cfg.StreamingRoutes = append(cfg.StreamingRoutes, cloned...) }
}

// streamingMatcher finds a request's streaming route with ServeMux's own
// matching; each pattern's handler is its route.
type streamingMatcher struct{ mux *http.ServeMux }

func newStreamingMatcher(routes []StreamingRoute) *streamingMatcher {
	if len(routes) == 0 {
		return nil
	}
	mux := http.NewServeMux()
	for _, r := range routes {
		mux.Handle(r.Pattern, &streamingHandler{route: r})
	}
	return &streamingMatcher{mux: mux}
}

// match returns the request's route, or nil. A request ServeMux would
// redirect or answer 405 matches none and keeps the buffering timeout.
func (m *streamingMatcher) match(r *http.Request) *streamingHandler {
	if m == nil {
		return nil
	}
	h, pattern := m.mux.Handler(r)
	route, ok := h.(*streamingHandler)
	if !ok || pattern == "" {
		return nil
	}
	return route
}

// streamingHandler carries a route; it is never served by the matcher.
type streamingHandler struct{ route StreamingRoute }

func (*streamingHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// serve runs next under the route's deadlines.
func (s *streamingHandler) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	end := time.Now().Add(s.route.Timeout)
	if err := setDeadline(rc.SetReadDeadline, end); err != nil {
		http.Error(w, "connection deadline", http.StatusInternalServerError)
		return
	}
	if err := setDeadline(rc.SetWriteDeadline, end); err != nil {
		http.Error(w, "connection deadline", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), end)
	defer cancel()
	r = r.WithContext(ctx)
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &budgetBody{ReadCloser: r.Body, budget: s.budget(end, rc.SetReadDeadline)}
	}
	// After the handler, a panic included, net/http still flushes the
	// response tail, drains up to 256 KiB of unread body (HTTP/1.1) or sends
	// the last frames under flow control (HTTP/2). Idle bounds that too, so
	// a client that stops reading or sending cannot pin the connection;
	// no deadline at all would leave it unbounded. A handler that hijacked
	// the connection keeps the deadlines set above on it (the controller can
	// no longer reach it) and must set its own.
	defer func() {
		tail := time.Now().Add(s.route.Idle)
		_ = rc.SetReadDeadline(tail)
		_ = rc.SetWriteDeadline(tail)
	}()
	next.ServeHTTP(&budgetWriter{ResponseWriter: w, rc: rc, budget: s.budget(end, rc.SetWriteDeadline)}, r)
}

func (s *streamingHandler) budget(end time.Time, set func(time.Time) error) *transferBudget {
	return &transferBudget{idle: s.route.Idle, minRate: s.route.MinRate, end: end, set: set, now: time.Now}
}

// setDeadline sets a connection deadline; a writer without deadlines (a
// test recorder) has none to set, and the server's timeouts stay.
func setDeadline(set func(time.Time) error, t time.Time) error {
	if err := set(t); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// transferBudget bounds one direction of a transfer. Before each read or
// write it arms the connection deadline at Idle from now, or what is left
// of Idle + moved/MinRate, never past end; afterwards it moves the
// deadline back to end. Disarming matters: on HTTP/2 a deadline is a
// timer that resets the stream even while the handler neither reads nor
// writes, and on HTTP/1.1 an expired read deadline ends the server's
// background read and cancels the handler.
type transferBudget struct {
	idle    time.Duration
	minRate int64
	end     time.Time
	set     func(time.Time) error
	now     func() time.Time

	moved int64
	spent time.Duration
}

// arm sets the deadline for the next operation and returns its start.
func (b *transferBudget) arm() (time.Time, error) {
	start := b.now()
	wait := b.idle
	if b.minRate > 0 {
		left := b.idle + atRate(b.moved, b.minRate) - b.spent
		if left <= 0 {
			// A deadline now also ends what the server still reads or writes.
			_ = b.set(start)
			return start, ErrTransferTooSlow
		}
		wait = min(wait, left)
	}
	deadline := start.Add(wait)
	if deadline.After(b.end) {
		deadline = b.end
	}
	return start, setDeadline(b.set, deadline)
}

// done records an operation that moved n bytes and disarms the deadline.
func (b *transferBudget) done(start time.Time, n int) {
	b.spent += b.now().Sub(start)
	b.moved += int64(n)
	_ = setDeadline(b.set, b.end)
}

// atRate is how long n bytes take at rate bytes per second, without
// overflowing for large n.
func atRate(n, rate int64) time.Duration {
	return time.Duration(n/rate)*time.Second + time.Duration(n%rate)*time.Second/time.Duration(rate)
}

// budgetBody bounds the reads of a request body by its budget.
type budgetBody struct {
	io.ReadCloser
	budget *transferBudget
}

func (b *budgetBody) Read(p []byte) (int, error) {
	start, err := b.budget.arm()
	if err != nil {
		return 0, err
	}
	n, err := b.ReadCloser.Read(p)
	b.budget.done(start, n)
	return n, err
}

// writeChunk is what one write deadline covers, so a large Write is not
// held to Idle as a whole.
const writeChunk = 32 << 10

// budgetWriter bounds the writes of a response by its budget. It hides an
// underlying io.ReaderFrom, so io.Copy writes in chunks under deadlines.
type budgetWriter struct {
	http.ResponseWriter
	rc     *http.ResponseController
	budget *transferBudget
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		start, err := w.budget.arm()
		if err != nil {
			return written, err
		}
		n, err := w.ResponseWriter.Write(p[:min(len(p), writeChunk)])
		w.budget.done(start, n)
		written += n
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

// Flush sends buffered bytes under the write deadline.
func (w *budgetWriter) Flush() {
	start, err := w.budget.arm()
	if err != nil {
		return
	}
	_ = w.rc.Flush()
	w.budget.done(start, 0)
}

// Unwrap lets the handler's [http.ResponseController] reach the connection.
func (w *budgetWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
