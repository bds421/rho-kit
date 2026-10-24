package redact

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSafeMessagesCollectsOnlySafeFrames(t *testing.T) {
	secret := errors.New("dial postgres://admin:hunter2@db.internal/app")
	err := fmt.Errorf("start feeder: %w", errors.Join(
		Safe("NATS_URL is required"),
		WrapError("connect database", secret),
	))

	got := SafeMessages(err)

	if got != "NATS_URL is required: connect database" {
		t.Fatalf("SafeMessages = %q", got)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "start feeder") {
		t.Fatalf("SafeMessages leaked a frame not marked safe: %q", got)
	}
}

func TestSafefFormatsMessage(t *testing.T) {
	err := Safef("%s must be %d bytes, hex encoded", "DEAD_LETTERS_KEY", 32)

	if err.Error() != "DEAD_LETTERS_KEY must be 32 bytes, hex encoded" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if SafeMessages(err) != err.Error() {
		t.Fatalf("SafeMessages = %q, want %q", SafeMessages(err), err.Error())
	}
}

func TestWrapSafeKeepsCauseButReportsOnlyItsMessage(t *testing.T) {
	cause := errors.New("open /run/secrets/token: permission denied")
	err := WrapSafe("TOKEN_FILE is unreadable", cause)

	if !errors.Is(err, cause) {
		t.Fatal("WrapSafe must keep the unwrap chain")
	}
	if err.Error() != "TOKEN_FILE is unreadable: "+cause.Error() {
		t.Fatalf("Error() = %q", err.Error())
	}
	if got := SafeMessages(err); got != "TOKEN_FILE is unreadable" {
		t.Fatalf("SafeMessages = %q", got)
	}
	if WrapSafe("x", nil) != nil {
		t.Fatal("WrapSafe(nil) must return nil")
	}
}

func TestSafeMessagesWithoutSafeFrames(t *testing.T) {
	if got := SafeMessages(fmt.Errorf("op: %w", errors.New("secret"))); got != "" {
		t.Fatalf("SafeMessages = %q, want empty", got)
	}
	if got := SafeMessages(nil); got != "" {
		t.Fatalf("SafeMessages(nil) = %q, want empty", got)
	}
}

func TestSafeMessagesBoundedOnWrapLoop(t *testing.T) {
	loop := &loopError{}
	loop.next = loop
	if got := SafeMessages(loop); strings.Count(got, "loop") != maxErrorFrames {
		t.Fatalf("SafeMessages on a wrap loop = %q", got)
	}
}

type loopError struct{ next error }

func (e *loopError) Error() string       { return "loop" }
func (e *loopError) SafeMessage() string { return "loop" }
func (e *loopError) Unwrap() error       { return e.next }

func TestErrorMessageAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logger.Error("application error",
		Error(fmt.Errorf("load: %w", errors.New("api key sk-123"))),
		ErrorMessage(fmt.Errorf("load: %w", errors.New("api key sk-123"))),
	)
	if strings.Contains(buf.String(), "error_message") || strings.Contains(buf.String(), "sk-123") {
		t.Fatalf("log without safe frames = %q", buf.String())
	}

	buf.Reset()
	logger.Error("application error", ErrorMessage(Safe("rate limiting must be declared explicitly")))
	if !strings.Contains(buf.String(), `error_message="rate limiting must be declared explicitly"`) {
		t.Fatalf("log = %q", buf.String())
	}
}
