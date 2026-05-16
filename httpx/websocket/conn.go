package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Context is the per-connection context type. It is an alias for
// [context.Context] so callers can use the standard library APIs
// directly while the kit retains the freedom to attach connection-
// scoped values later without an API break.
type Context = context.Context

// MessageType identifies the WebSocket frame opcode. The kit exposes
// its own alias so callers do not have to import coder/websocket to
// switch on the message type.
type MessageType int

// MessageType constants. The numeric values mirror coder/websocket's
// MessageText/MessageBinary so a one-to-one conversion is correct in
// both directions.
const (
	MessageText   MessageType = MessageType(coderws.MessageText)
	MessageBinary MessageType = MessageType(coderws.MessageBinary)
)

// StatusCode is the WebSocket close status as defined in RFC 6455.
// Mirrors coder/websocket's StatusCode so callers can switch on close
// codes without importing the upstream package.
type StatusCode int

// Standard close codes from RFC 6455 §7.4.1 and the IANA registry.
const (
	StatusNormalClosure           StatusCode = StatusCode(coderws.StatusNormalClosure)
	StatusGoingAway               StatusCode = StatusCode(coderws.StatusGoingAway)
	StatusProtocolError           StatusCode = StatusCode(coderws.StatusProtocolError)
	StatusUnsupportedData         StatusCode = StatusCode(coderws.StatusUnsupportedData)
	StatusInvalidFramePayloadData StatusCode = StatusCode(coderws.StatusInvalidFramePayloadData)
	StatusPolicyViolation         StatusCode = StatusCode(coderws.StatusPolicyViolation)
	StatusMessageTooBig           StatusCode = StatusCode(coderws.StatusMessageTooBig)
	StatusMandatoryExtension      StatusCode = StatusCode(coderws.StatusMandatoryExtension)
	StatusInternalError           StatusCode = StatusCode(coderws.StatusInternalError)
	StatusServiceRestart          StatusCode = StatusCode(coderws.StatusServiceRestart)
	StatusTryAgainLater           StatusCode = StatusCode(coderws.StatusTryAgainLater)
	StatusBadGateway              StatusCode = StatusCode(coderws.StatusBadGateway)
)

// Conn is the kit wrapper around a coder/websocket Conn. It adds
// Prometheus metric emission on every read/write, idempotent close,
// and redacted error returns so backend error text never crosses a
// trust boundary verbatim.
//
// Methods on Conn are safe for concurrent use except as documented by
// the underlying coder/websocket Conn (the Reader/Read pair is not
// safe for concurrent reads).
type Conn struct {
	inner        *coderws.Conn
	ctx          context.Context
	logger       *slog.Logger
	metrics      *Metrics
	writeTimeout time.Duration

	closeOnce sync.Once
	closed    atomic.Bool
	closeCode atomic.Int64 // last close code observed (peer or local), for metrics
}

// writeCtx returns the context to use for a single outbound write. When
// a positive write timeout is configured it returns a derived context
// with that deadline; otherwise it returns the per-connection context
// and a no-op cancel.
//
// The WebSocket framing protocol cannot resume a partially-sent
// message, so when this context expires coder/websocket closes the
// connection — see [WithWriteTimeout] for the security rationale.
func (c *Conn) writeCtx() (context.Context, context.CancelFunc) {
	if c.writeTimeout <= 0 {
		return c.ctx, func() {}
	}
	return context.WithTimeout(c.ctx, c.writeTimeout)
}

// Subprotocol returns the subprotocol negotiated during the upgrade,
// or the empty string when none was selected.
func (c *Conn) Subprotocol() string {
	return c.inner.Subprotocol()
}

// Context returns the per-connection context. It is cancelled when
// the connection closes for any reason.
func (c *Conn) Context() context.Context {
	return c.ctx
}

// ReadMessage reads a single message from the connection. The kit
// MessageType alias is returned so callers do not have to switch on
// coder/websocket's enum.
//
// On error the underlying coder/websocket error is wrapped with
// [redact.WrapError] so callers can still [errors.Is] against the
// upstream sentinels (e.g. [io.EOF], [net.ErrClosed]) but the rendered
// text never embeds inner driver detail.
func (c *Conn) ReadMessage() (MessageType, []byte, error) {
	typ, payload, err := c.inner.Read(c.ctx)
	if err != nil {
		c.recordCloseFromError(err)
		return 0, nil, redact.WrapError("httpx/websocket: read", err)
	}
	c.metrics.observeMessage(directionIn, len(payload))
	return MessageType(typ), payload, nil
}

// WriteMessage writes a single message to the connection.
//
// When [WithWriteTimeout] is set the write is bounded by that
// duration; on deadline expiry coder/websocket closes the connection
// because a partial frame cannot be resumed.
//
// Errors are wrapped with [redact.WrapError]; the inner driver
// message is never embedded verbatim.
func (c *Conn) WriteMessage(typ MessageType, payload []byte) error {
	ctx, cancel := c.writeCtx()
	defer cancel()
	if err := c.inner.Write(ctx, coderws.MessageType(typ), payload); err != nil {
		c.recordCloseFromError(err)
		return redact.WrapError("httpx/websocket: write", err)
	}
	c.metrics.observeMessage(directionOut, len(payload))
	return nil
}

// ReadJSON reads a single text message and decodes it as JSON into v.
//
// On decode failure the connection is NOT closed (the caller may
// choose to send an application-level error frame and continue), but
// the error is wrapped with [redact.WrapError].
func (c *Conn) ReadJSON(v any) error {
	typ, payload, err := c.inner.Read(c.ctx)
	if err != nil {
		c.recordCloseFromError(err)
		return redact.WrapError("httpx/websocket: read json", err)
	}
	c.metrics.observeMessage(directionIn, len(payload))
	if typ != coderws.MessageText {
		return redact.WrapError("httpx/websocket: read json", errors.New("expected text message"))
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return redact.WrapError("httpx/websocket: decode json", err)
	}
	return nil
}

// WriteJSON serialises v as JSON and writes it as a text message.
//
// When [WithWriteTimeout] is set the write is bounded by that
// duration; on deadline expiry coder/websocket closes the connection
// because a partial frame cannot be resumed.
func (c *Conn) WriteJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return redact.WrapError("httpx/websocket: encode json", err)
	}
	ctx, cancel := c.writeCtx()
	defer cancel()
	if err := c.inner.Write(ctx, coderws.MessageText, payload); err != nil {
		c.recordCloseFromError(err)
		return redact.WrapError("httpx/websocket: write json", err)
	}
	c.metrics.observeMessage(directionOut, len(payload))
	return nil
}

// Ping sends a WebSocket Ping control frame and blocks until the
// peer's Pong reply arrives or ctx is cancelled. Useful when an
// application wants to drive its own heartbeat instead of (or in
// addition to) [WithPingInterval].
//
// Errors are wrapped with [redact.WrapError]; the inner driver text
// is not embedded verbatim.
func (c *Conn) Ping(ctx context.Context) error {
	if err := c.inner.Ping(ctx); err != nil {
		return redact.WrapError("httpx/websocket: ping", err)
	}
	return nil
}

// Close performs the WebSocket close handshake idempotently. The
// first call drives the close on the underlying connection; later
// calls are no-ops and return nil so callers can defer Close
// unconditionally even when the handler already called it explicitly.
func (c *Conn) Close(code StatusCode, reason string) error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeCode.CompareAndSwap(0, int64(code))
		err := c.inner.Close(coderws.StatusCode(code), reason)
		if err != nil {
			closeErr = redact.WrapError("httpx/websocket: close", err)
		}
		c.metrics.connClosed(int(c.closeCode.Load()))
	})
	return closeErr
}

// recordCloseFromError records the close code observed on a peer
// disconnect so the metric label reflects the real close reason
// rather than always reporting StatusAbnormalClosure.
func (c *Conn) recordCloseFromError(err error) {
	if err == nil {
		return
	}
	code := coderws.CloseStatus(err)
	if code == -1 {
		return
	}
	c.closeCode.CompareAndSwap(0, int64(code))
}
