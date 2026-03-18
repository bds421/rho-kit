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
	handler := Timeout(1 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("handler should be called for websocket upgrades")
	}
}

func TestTimeout_WebSocketBypass_CaseInsensitive(t *testing.T) {
	handlerCalled := false
	handler := Timeout(1 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "WebSocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !handlerCalled {
		t.Error("handler should be called for WebSocket (case-insensitive)")
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
		if msg != "timeout: duration must be positive" {
			t.Errorf("expected panic message %q, got %q", "timeout: duration must be positive", msg)
		}
	}()
	Timeout(0)
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
	const overLimit = maxBufferSize + 1<<20 // 11 MiB — 1 MiB over the cap

	handler := Timeout(5 * time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bigPayload := bytes.Repeat([]byte("x"), overLimit)
		// First write truncates to maxBufferSize and returns ErrResponseTooLarge.
		n, err := w.Write(bigPayload)
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("first Write: expected ErrResponseTooLarge, got %v", err)
		}
		if n != maxBufferSize {
			t.Errorf("first Write: expected %d bytes written, got %d", maxBufferSize, n)
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

	// The response body must be truncated to at most maxBufferSize bytes.
	bodyLen := rec.Body.Len()
	if bodyLen > maxBufferSize {
		t.Errorf("response body exceeds maxBufferSize: got %d bytes, want <= %d", bodyLen, maxBufferSize)
	}
	if bodyLen == 0 {
		t.Error("response body is empty; expected truncated content")
	}
}
