package timeout

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTimeout_NormalRequest(t *testing.T) {
	handler := Timeout(5 * time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestTimeout_WebSocketBypass(t *testing.T) {
	handlerCalled := false
	handler := Timeout(1*time.Millisecond, WithWebSocketUpgradeBypass())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("handler should be called for websocket upgrades")
	}
	if rec.Code != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", rec.Code)
	}
}

func TestTimeout_WebSocketBypass_CaseInsensitive(t *testing.T) {
	handlerCalled := false
	handler := Timeout(1*time.Millisecond, WithWebSocketUpgradeBypass())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "WebSocket")
	req.Header.Set("Connection", "keep-alive, upgrade")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("handler should be called for WebSocket (case-insensitive)")
	}
}

func TestTimeout_WebSocketBypassRequiresConnectionUpgrade(t *testing.T) {
	handler := Timeout(1*time.Millisecond, WithWebSocketUpgradeBypass())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(100 * time.Millisecond):
			w.WriteHeader(http.StatusSwitchingProtocols)
		case <-r.Context().Done():
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 without Connection: Upgrade", rec.Code)
	}
}

func TestTimeout_WebSocketBypassRejectsDuplicateUpgradeHeader(t *testing.T) {
	handler := Timeout(1*time.Millisecond, WithWebSocketUpgradeBypass())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(100 * time.Millisecond):
			w.WriteHeader(http.StatusSwitchingProtocols)
		case <-r.Context().Done():
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with duplicated Upgrade", rec.Code)
	}
}

func TestTimeout_SlowHandler_Returns503(t *testing.T) {
	handler := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			// respect context cancellation so the middleware can return
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "request timeout") {
		t.Errorf("expected body to contain %q, got %q", "request timeout", body)
	}
	if !strings.Contains(body, "TIMEOUT") {
		t.Errorf("expected body to contain %q, got %q", "TIMEOUT", body)
	}
}

func TestTimeout_ZeroDuration_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected Timeout(0) to panic, but it did not")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected panic value to be a string, got %T: %v", r, r)
		}
		if msg != "middleware/timeout: Timeout: duration must be positive" {
			t.Errorf("expected panic message %q, got %q", "middleware/timeout: Timeout: duration must be positive", msg)
		}
	}()
	Timeout(0)
}

func TestTimeout_NilOption_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected Timeout with nil option to panic, but it did not")
		}
	}()
	Timeout(time.Second, nil)
}

func TestTimeout_PanicBeforeDeadlinePropagates(t *testing.T) {
	handler := Timeout(time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("handler exploded")
	}))

	defer func() {
		r := recover()
		if r != "handler exploded" {
			t.Fatalf("panic = %v, want handler exploded", r)
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestTimeout_InvalidWriteHeaderPanics(t *testing.T) {
	handler := Timeout(time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(42)
	}))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected invalid WriteHeader code to panic")
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/bad-status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestTimeout_PanicAfterReturnIsCaptured(t *testing.T) {
	releasePanic := make(chan struct{})
	handler := Timeout(20*time.Millisecond, WithPostTimeoutWait(0))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		<-releasePanic
		panic("late panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/late-panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	close(releasePanic)
	time.Sleep(20 * time.Millisecond)
}

func TestTimeout_HardModeReturnsImmediatelyOnDeadline(t *testing.T) {
	// Hard mode must return BEFORE the slow handler exits.
	handlerExited := make(chan struct{})

	handler := Timeout(20*time.Millisecond, WithHard())(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// Ignore ctx; sleep past the deadline.
		time.Sleep(500 * time.Millisecond)
		close(handlerExited)
	}))

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("hard timeout took %v; should return on deadline (~20ms), not wait for handler", elapsed)
	}
	// Confirm handler is still running after we returned.
	select {
	case <-handlerExited:
		t.Error("handler exited before our return — hard mode behaved cooperatively")
	case <-time.After(50 * time.Millisecond):
		// Expected: handler still asleep.
	}
	// Wait for the leak so the test doesn't pollute later runs.
	<-handlerExited
}

func TestTimeout_DefaultReturnsAfterPostTimeoutWait(t *testing.T) {
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerExited := make(chan struct{})

	handler := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(handlerEntered)
		<-releaseHandler
		close(handlerExited)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ignores-context", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	select {
	case <-handlerEntered:
	default:
		t.Fatal("handler did not start")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("timeout returned after %v; should return after deadline plus bounded grace", elapsed)
	}
	select {
	case <-handlerExited:
		t.Error("handler exited before release")
	default:
	}

	close(releaseHandler)
	select {
	case <-handlerExited:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler cleanup")
	}
}

func TestTimeout_CooperativeModeWaitsForHandler(t *testing.T) {
	handlerExited := make(chan struct{})

	handler := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			// honour cancellation
		case <-time.After(2 * time.Second):
			t.Error("handler ran past timeout without seeing ctx.Done()")
		}
		close(handlerExited)
	}))

	req := httptest.NewRequest(http.MethodGet, "/cooperative", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Handler must have already exited by the time we return.
	select {
	case <-handlerExited:
		// expected
	case <-time.After(50 * time.Millisecond):
		t.Fatal("cooperative mode returned before handler exited")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestTimeout_NegativeDuration_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected Timeout(-1) to panic, but it did not")
		}
	}()
	Timeout(-1)
}

func TestTimeout_WithPostTimeoutWaitPanicsOnNegative(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected WithPostTimeoutWait(-1) to panic, but it did not")
		}
	}()
	WithPostTimeoutWait(-1)
}

func TestTimeout_WithPostTimeoutWaitZeroReturnsImmediately(t *testing.T) {
	releaseHandler := make(chan struct{})
	handlerExited := make(chan struct{})

	handler := Timeout(20*time.Millisecond, WithPostTimeoutWait(0))(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-releaseHandler
		close(handlerExited)
	}))

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("zero post-timeout wait took %v; should return on deadline", elapsed)
	}
	close(releaseHandler)
	select {
	case <-handlerExited:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler cleanup")
	}
}

func TestTimeout_WriteAfterTimeout(t *testing.T) {
	// writeErr receives the error returned by w.Write after the timeout fires.
	writeErr := make(chan error, 1)

	handler := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wait until the context deadline fires, then try to write.
		<-r.Context().Done()
		// Brief yield to let the middleware's select case call writeTimeout()
		// and set the written flag before we attempt our Write.
		time.Sleep(5 * time.Millisecond)
		_, err := w.Write([]byte("too late"))
		writeErr <- err
	}))

	req := httptest.NewRequest(http.MethodGet, "/late-write", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The middleware must have returned a 503 timeout response.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	// The Write call inside the handler must have returned ErrHandlerTimeout.
	err := <-writeErr
	if err != http.ErrHandlerTimeout {
		t.Errorf("expected http.ErrHandlerTimeout from Write after timeout, got %v", err)
	}
}

func TestTimeout_BufferOverflow(t *testing.T) {
	const overLimit = defaultMaxBufferSize + 1<<20 // 11 MiB — 1 MiB over the cap

	handler := Timeout(5 * time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bigPayload := bytes.Repeat([]byte("x"), overLimit)
		// First write truncates to defaultMaxBufferSize and returns ErrResponseTooLarge.
		n, err := w.Write(bigPayload)
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("first Write: expected ErrResponseTooLarge, got %v", err)
		}
		if n != defaultMaxBufferSize {
			t.Errorf("first Write: expected %d bytes written, got %d", defaultMaxBufferSize, n)
		}
		// Second write hits the full buffer and returns ErrResponseTooLarge.
		n, err = w.Write([]byte("more"))
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("second Write: expected ErrResponseTooLarge, got %v", err)
		}
		if n != 0 {
			t.Errorf("second Write: expected 0 bytes written, got %d", n)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/big", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// The response body must be truncated to at most defaultMaxBufferSize bytes.
	bodyLen := rec.Body.Len()
	if bodyLen > defaultMaxBufferSize {
		t.Errorf("response body exceeds defaultMaxBufferSize: got %d bytes, want <= %d", bodyLen, defaultMaxBufferSize)
	}
	if bodyLen == 0 {
		t.Error("response body is empty; expected truncated content")
	}
}
