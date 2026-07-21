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

// CloseStatus extracts the RFC 6455 close status code from err, or
// returns -1 when err is nil or does not carry a close status. It is
// the kit-native equivalent of coder/websocket's CloseStatus and works
// through the [redact.WrapError] wrapping applied to read/write errors,
// so callers can switch on close codes without importing the upstream
// package (the same reason the [StatusCode] constants are mirrored
// here).
//
// Typical use is to distinguish a routine peer disconnect from a real
// failure in a handler's read loop:
//
//	for {
//		_, _, err := c.ReadMessage()
//		if err != nil {
//			if websocket.IsNormalClosure(err) {
//				return nil // peer went away; not an error
//			}
//			return err
//		}
//		// ... handle message ...
//	}
func CloseStatus(err error) StatusCode {
	return StatusCode(coderws.CloseStatus(err))
}

// IsNormalClosure reports whether err carries a close status that
// represents a routine, expected peer disconnect — StatusNormalClosure
// (1000) or StatusGoingAway (1001, sent by browsers on navigation/tab
// close). It returns false for nil, non-close errors, and abnormal
// close codes.
func IsNormalClosure(err error) bool {
	switch CloseStatus(err) {
	case StatusNormalClosure, StatusGoingAway:
		return true
	default:
		return false
	}
}

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
	cancel       context.CancelFunc // cancels ctx when the connection dies
	logger       *slog.Logger
	metrics      *Metrics
	writeTimeout time.Duration

	closeOnce sync.Once
	closed    atomic.Bool
	closeCode atomic.Int64 // last close code observed (peer or local), for metrics
}

// cancelCtx cancels the per-connection context if a cancel func is
// wired. It is safe to call repeatedly and from any goroutine: the
// stdlib CancelFunc is idempotent. This is what makes [Conn.Context]
// honour its documented contract — the context is cancelled when the
// connection closes for any reason (explicit Close, peer disconnect
// observed on a read/write, or heartbeat-driven close).
func (c *Conn) cancelCtx() {
	if c.cancel != nil {
		c.cancel()
	}
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
	typ, payload, err := c.ReadMessage()
	if err != nil {
		// ReadMessage already recorded close + redacted; re-prefix for JSON path.
		return redact.WrapError("httpx/websocket: read json", err)
	}
	if typ != MessageText {
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
	if err := c.WriteMessage(MessageText, payload); err != nil {
		return redact.WrapError("httpx/websocket: write json", err)
	}
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
	// Bound outbound ping by writeTimeout when set, matching WriteMessage.
	if c.writeTimeout > 0 {
		var cancel context.CancelFunc
		if ctx == nil {
			ctx = c.ctx
		}
		ctx, cancel = context.WithTimeout(ctx, c.writeTimeout)
		defer cancel()
	} else if ctx == nil {
		ctx = c.ctx
	}
	if err := c.inner.Ping(ctx); err != nil {
		// context.DeadlineExceeded is a pong-timeout (or write-timeout)
		// signal for the caller — the connection may still be usable
		// until they Close. Only cancel Conn.Context for true
		// connection-death errors so heartbeat can still classify a
		// pong-deadline as result="timeout" before Close cancels ctx.
		if !errors.Is(err, context.DeadlineExceeded) {
			c.recordCloseFromError(err)
		}
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
		// Cancel the per-connection context first so any handler parked
		// on <-ctx.Done() wakes immediately, rather than after the
		// (potentially multi-second) close handshake. This honours the
		// documented Context() contract: the context is cancelled when
		// the connection closes.
		c.cancelCtx()
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
// rather than always reporting StatusAbnormalClosure, and cancels the
// per-connection context.
//
// A non-nil error from a read or write means the connection is no
// longer usable (peer close, abnormal closure, network error), so the
// context is cancelled regardless of whether a close code could be
// parsed — this is what lets a concurrently-parked <-ctx.Done()
// observe the disconnect, per the [Conn.Context] contract.
func (c *Conn) recordCloseFromError(err error) {
	if err == nil {
		return
	}
	if code := coderws.CloseStatus(err); code != -1 {
		c.closeCode.CompareAndSwap(0, int64(code))
	}
	c.cancelCtx()
}
