package authz_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/authz/v2"
)

type recordingSink struct {
	mu     sync.Mutex
	events []authz.AuditEvent
}

func (r *recordingSink) LogAuthz(_ context.Context, event authz.AuditEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordingSink) snapshot() []authz.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]authz.AuditEvent, len(r.events))
	copy(out, r.events)
	return out
}

func newTextBuffer(level slog.Level) (*bytes.Buffer, *slog.Logger) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
	return buf, logger
}

func TestLogged_DenyEmitsInfoExactlyOnce(t *testing.T) {
	buf, logger := newTextBuffer(slog.LevelDebug)
	sink := &recordingSink{}
	mem := authz.NewMemoryStore()

	dec := authz.Logged(mem, authz.WithLogger(logger), authz.WithAuditSink(sink))

	err := dec.Allow(context.Background(), "alice", "read", "doc:1")
	require.Error(t, err)
	assert.ErrorIs(t, err, authz.ErrDenied)

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "authz decision"),
		"deny path must emit exactly one record, got: %s", out)
	assert.Contains(t, out, "action=authz.deny")
	// Identifier-shaped fields are redacted in the slog stream (operator-
	// facing) but preserved in the structured audit sink (compliance).
	assert.NotContains(t, out, "actor=alice",
		"slog must not leak raw subject identifiers; the audit sink keeps full values")
	assert.NotContains(t, out, "resource=doc:1")
	assert.NotContains(t, out, "verb=read")
	assert.Contains(t, out, "<redacted, <16 bytes>",
		"redacted bucket stamps must still appear for actor/resource")
	assert.Contains(t, out, "outcome=deny")
	assert.Contains(t, out, "reason=denied")
	assert.Contains(t, out, "level=INFO")

	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "authz.deny", events[0].Action)
	assert.Equal(t, "deny", events[0].Outcome)
	assert.Equal(t, "denied", events[0].Reason)
	// AuditEvent keeps full values for the compliance/forensic sink.
	assert.Equal(t, "alice", events[0].Actor)
	assert.Equal(t, "doc:1", events[0].Resource)
	assert.Equal(t, "read", events[0].Verb)
}

func TestLogged_AllowEmitsDebugOnly(t *testing.T) {
	// At info level the allow path must be silent (default operator stance).
	infoBuf, infoLogger := newTextBuffer(slog.LevelInfo)
	sink := &recordingSink{}
	mem := authz.NewMemoryStore()
	mem.Grant("alice", "read", "doc:1")

	dec := authz.Logged(mem, authz.WithLogger(infoLogger), authz.WithAuditSink(sink))
	require.NoError(t, dec.Allow(context.Background(), "alice", "read", "doc:1"))

	assert.Empty(t, infoBuf.String(), "allow path must not produce info-level records")

	// At debug level the allow path emits exactly one record at debug level.
	debugBuf, debugLogger := newTextBuffer(slog.LevelDebug)
	dec2 := authz.Logged(mem, authz.WithLogger(debugLogger))
	require.NoError(t, dec2.Allow(context.Background(), "alice", "read", "doc:1"))
	out := debugBuf.String()
	assert.Equal(t, 1, strings.Count(out, "authz decision"))
	assert.Contains(t, out, "action=authz.allow")
	assert.Contains(t, out, "outcome=success")
	assert.Contains(t, out, "level=DEBUG")

	// Sink always receives allow events regardless of slog level.
	require.Len(t, sink.snapshot(), 1)
	assert.Equal(t, "authz.allow", sink.snapshot()[0].Action)
	assert.Equal(t, "success", sink.snapshot()[0].Outcome)
}

func TestLogged_ClassifiesErrors(t *testing.T) {
	buf, logger := newTextBuffer(slog.LevelDebug)
	sink := &recordingSink{}

	// invalid request → reason=invalid_request, outcome=deny
	mem := authz.NewMemoryStore()
	dec := authz.Logged(mem, authz.WithLogger(logger), authz.WithAuditSink(sink))
	err := dec.Allow(context.Background(), "alice", "read", "bad resource")
	require.Error(t, err)
	assert.ErrorIs(t, err, authz.ErrInvalidRequest)

	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "invalid_request", events[0].Reason)
	assert.Equal(t, "deny", events[0].Outcome)
	assert.Contains(t, buf.String(), "reason=invalid_request")
}

func TestLogged_EngineErrorClassified(t *testing.T) {
	sink := &recordingSink{}
	infraErr := errors.New("openfga unreachable")
	failing := deciderFunc(func(context.Context, string, string, string) error { return infraErr })
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	dec := authz.Logged(failing, authz.WithLogger(logger), authz.WithAuditSink(sink))
	err := dec.Allow(context.Background(), "alice", "read", "doc:1")
	require.ErrorIs(t, err, infraErr)

	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "engine_error", events[0].Reason)
	assert.Equal(t, "error", events[0].Outcome)
}

func TestLogged_NilInnerReturnsNoDecider(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	dec := authz.Logged(nil, authz.WithLogger(logger))
	err := dec.Allow(context.Background(), "alice", "read", "doc:1")
	assert.ErrorIs(t, err, authz.ErrNoDecider)
}

func TestLogged_PanicsWithoutSinkOrLogger(t *testing.T) {
	require.Panics(t, func() { authz.Logged(authz.NewMemoryStore()) })
}

func TestLogged_PanicsOnNilOption(t *testing.T) {
	require.Panics(t, func() { authz.Logged(authz.NewMemoryStore(), nil) })
}

func TestWithLogger_PanicsOnNil(t *testing.T) {
	require.Panics(t, func() { authz.WithLogger(nil) })
}

func TestWithAuditSink_PanicsOnNil(t *testing.T) {
	require.Panics(t, func() { authz.WithAuditSink(nil) })
}

// deciderFunc is a function adapter so tests can build ad-hoc Deciders.
type deciderFunc func(context.Context, string, string, string) error

func (f deciderFunc) Allow(ctx context.Context, subject, action, resource string) error {
	return f(ctx, subject, action, resource)
}

// TestLogged_SatisfiesDecider is a runtime check (staticcheck's QF1011 flags
// the equivalent compile-time `var _ authz.Decider = ...` form) that the
// wrapped value still satisfies the Decider interface contract.
func TestLogged_SatisfiesDecider(t *testing.T) {
	d := authz.Logged(authz.NewMemoryStore(),
		authz.WithLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))))
	if _, ok := any(d).(authz.Decider); !ok {
		t.Fatalf("Logged did not return an authz.Decider, got %T", d)
	}
}

type panickingSink struct{}

func (panickingSink) LogAuthz(context.Context, authz.AuditEvent) {
	panic("audit sink boom")
}

func TestLogged_SinkPanicDoesNotPropagate(t *testing.T) {
	mem := authz.NewMemoryStore()
	mem.Grant("alice", "read", "doc:1")
	dec := authz.Logged(mem, authz.WithAuditSink(panickingSink{}))
	err := dec.Allow(context.Background(), "alice", "read", "doc:1")
	require.NoError(t, err, "sink panic must not convert allow into crash/error")
}
