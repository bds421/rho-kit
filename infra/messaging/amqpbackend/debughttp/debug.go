// Package debughttp provides HTTP handlers for debugging AMQP message
// consumption and publishing. Import this package only if you need
// debug endpoints — it pulls in httpx as a dependency.
//
// SECURITY: ConsumeHandler and PublishHandler accept arbitrary input and
// invoke registered handlers / publish to brokers based on attacker-supplied
// JSON. They MUST be wrapped with [Guard] before being mounted on any
// listener that is reachable from outside the operator's debug environment.
// Guard requires a non-production environment AND an Authenticator (see
// [BasicAuth] / [AllowFromHeader]). Mounting unguarded handlers is a remote
// code-execution-equivalent risk for any service that performs side-effecting
// work in handlers.
package debughttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

type consumeRequest struct {
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	SchemaVersion uint            `json:"schema_version,omitempty"`
	RoutingKey    string          `json:"routing_key,omitempty"`
}

type publishRequest struct {
	Exchange   string          `json:"exchange"`
	RoutingKey string          `json:"routing_key"`
	Type       string          `json:"type,omitempty"`
	Payload    json.RawMessage `json:"payload"`
}

type response struct {
	OK        bool   `json:"ok"`
	MessageID string `json:"message_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

type typesResponse struct {
	Types []string `json:"types"`
}

// ConsumeHandler returns a [Guard]-wrapped HTTP handler that accepts a JSON
// body and dispatches it to the registered consumer handler, bypassing RabbitMQ.
// environment and auth are required so the RCE-equivalent surface cannot be
// mounted without both gates (use [UnguardedConsumeHandler] only in tests).
//
// Request body: { "type": "event.type", "payload": { ... } }
// Response:     { "ok": true, "message_id": "..." }
func ConsumeHandler(environment string, auth Authenticator, handlers map[string]messaging.Handler, logger *slog.Logger) http.Handler {
	return Guard(environment, auth, UnguardedConsumeHandler(handlers, logger))
}

// UnguardedConsumeHandler is the raw debug consume endpoint. Prefer
// [ConsumeHandler], which always applies [Guard].
func UnguardedConsumeHandler(handlers map[string]messaging.Handler, logger *slog.Logger) http.HandlerFunc {
	if handlers == nil {
		panic("debughttp: ConsumeHandler requires a non-nil handlers map")
	}
	handlers = cloneHandlers(handlers)
	if logger == nil {
		logger = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req consumeRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		if req.Type == "" {
			httpx.WriteError(w, http.StatusBadRequest, "type is required")
			return
		}

		handler, ok := handlers[req.Type]
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "unknown message type")
			return
		}

		msg, err := messaging.NewMessage(req.Type, req.Payload)
		if err != nil {
			// Client-controlled Type/Payload failed ValidateMessage — 400, not 500.
			logger.Info("debug consume: invalid message", redact.Error(err))
			httpx.WriteError(w, http.StatusBadRequest, "invalid message")
			return
		}
		if req.SchemaVersion != 0 {
			msg = msg.WithSchemaVersion(req.SchemaVersion)
		}

		d := messaging.Delivery{
			Message:       msg,
			RoutingKey:    req.RoutingKey,
			SchemaVersion: req.SchemaVersion,
		}

		logger.Info("debug consume", redact.String("type", req.Type), redact.String("message_id", msg.ID))

		if err := handler(r.Context(), d); err != nil {
			logger.Error("debug consume handler failed", redact.String("type", req.Type), redact.Error(err))
			_ = httpx.WriteJSON(w, r, http.StatusInternalServerError, response{
				OK:        false,
				MessageID: msg.ID,
				Error:     "handler failed",
			})
			return
		}

		_ = httpx.WriteJSON(w, r, http.StatusOK, response{OK: true, MessageID: msg.ID})
	}
}

func cloneHandlers(handlers map[string]messaging.Handler) map[string]messaging.Handler {
	owned := make(map[string]messaging.Handler, len(handlers))
	for msgType, handler := range handlers {
		if handler == nil {
			panic("debughttp: ConsumeHandler requires non-nil handlers")
		}
		owned[msgType] = handler
	}
	return owned
}

// ConsumeTypesHandler returns an HTTP handler that lists all registered
// consumer message types. Useful to discover what types the consume endpoint accepts.
func ConsumeTypesHandler(handlers map[string]messaging.Handler) http.HandlerFunc {
	if handlers == nil {
		panic("debughttp: ConsumeTypesHandler requires a non-nil handlers map")
	}
	types := make([]string, 0, len(handlers))
	for t := range handlers {
		types = append(types, t)
	}
	sort.Strings(types)

	return func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, r, http.StatusOK, typesResponse{Types: types})
	}
}

// PublishHandler returns a [Guard]-wrapped HTTP handler that publishes a
// message to a RabbitMQ exchange via REST. environment and auth are required
// so the surface cannot be mounted without both gates (use
// [UnguardedPublishHandler] only in tests).
// The allowedExchanges parameter restricts which exchanges can be targeted
// (exchange-only; any routing key within an allowed exchange is reachable).
// Pass nil panics; use []string{"*"} to opt into open-publish.
//
// Request body: { "exchange": "...", "routing_key": "...", "type": "...", "payload": { ... } }
// "type" is optional and defaults to routing_key. Empty routing_key is allowed for fanout.
// Response:     { "ok": true, "message_id": "..." }
func PublishHandler(environment string, auth Authenticator, pub messaging.Publisher, allowedExchanges []string, logger *slog.Logger) http.Handler {
	return Guard(environment, auth, UnguardedPublishHandler(pub, allowedExchanges, logger))
}

// UnguardedPublishHandler is the raw debug publish endpoint. Prefer
// [PublishHandler], which always applies [Guard].
func UnguardedPublishHandler(pub messaging.Publisher, allowedExchanges []string, logger *slog.Logger) http.HandlerFunc {
	if pub == nil {
		panic("debughttp: PublishHandler requires a non-nil publisher")
	}
	if logger == nil {
		logger = slog.Default()
	}
	// nil allowedExchanges is now treated as "deny all" rather than the
	// previous "allow all" — a missing allowlist on a publish endpoint
	// is RCE-equivalent and is the kind of misconfiguration a strict
	// kit must refuse. Operators that genuinely want every exchange
	// must pass an explicit "*" entry (handled below).
	if allowedExchanges == nil {
		panic("debughttp: PublishHandler requires a non-nil allowedExchanges (use []string{\"*\"} to opt into open-publish, []string{} to deny all)")
	}
	allowAny := false
	allowed := make(map[string]struct{}, len(allowedExchanges))
	for _, e := range allowedExchanges {
		if e == "*" {
			allowAny = true
			continue
		}
		allowed[e] = struct{}{}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req publishRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		if req.Exchange == "" {
			httpx.WriteError(w, http.StatusBadRequest, "exchange is required")
			return
		}
		// Empty routing_key is allowed (fanout / exchange-only), matching
		// messaging.ValidatePublishRoute. Message type defaults to routing_key
		// for backward compatibility when "type" is omitted.

		if !allowAny {
			if _, ok := allowed[req.Exchange]; !ok {
				httpx.WriteError(w, http.StatusBadRequest, "exchange not allowed")
				return
			}
		}

		msgType := req.Type
		if msgType == "" {
			msgType = req.RoutingKey
		}
		if msgType == "" {
			httpx.WriteError(w, http.StatusBadRequest, "type is required when routing_key is empty")
			return
		}
		msg, err := messaging.NewMessage(msgType, req.Payload)
		if err != nil {
			// Client-controlled type/payload failed ValidateMessage — 400.
			logger.Info("debug publish: invalid message", redact.Error(err))
			httpx.WriteError(w, http.StatusBadRequest, "invalid message")
			return
		}

		logger.Info("debug publish",
			redact.String("exchange", req.Exchange),
			redact.String("routing_key", req.RoutingKey),
			redact.String("message_id", msg.ID),
		)

		if err := pub.Publish(r.Context(), req.Exchange, req.RoutingKey, msg); err != nil {
			logger.Error("debug publish failed", redact.Error(err))
			_ = httpx.WriteJSON(w, r, http.StatusInternalServerError, response{
				OK:        false,
				MessageID: msg.ID,
				Error:     "publish failed",
			})
			return
		}

		_ = httpx.WriteJSON(w, r, http.StatusOK, response{OK: true, MessageID: msg.ID})
	}
}
