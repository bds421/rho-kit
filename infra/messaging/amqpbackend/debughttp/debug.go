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
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type publishRequest struct {
	Exchange   string          `json:"exchange"`
	RoutingKey string          `json:"routing_key"`
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

// ConsumeHandler returns an HTTP handler that accepts a JSON body
// and dispatches it to the registered consumer handler, bypassing RabbitMQ.
//
// Request body: { "type": "event.type", "payload": { ... } }
// Response:     { "ok": true, "message_id": "..." }
func ConsumeHandler(handlers map[string]messaging.Handler, logger *slog.Logger) http.HandlerFunc {
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
			logger.Error("debug consume: failed to create message", redact.Error(err))
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create message")
			return
		}

		// Mirror real consumption as closely as the request allows so handlers
		// that branch on transport metadata behave the same under this debug
		// endpoint as in production. The dispatch key (req.Type) doubles as the
		// routing key the AMQP backend would carry, and the message's schema
		// version is surfaced on the delivery just as the consumer does.
		d := messaging.Delivery{
			Message:       msg,
			RoutingKey:    req.Type,
			SchemaVersion: msg.SchemaVersion,
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

// PublishHandler returns an HTTP handler that publishes a message
// to a RabbitMQ exchange via REST. This triggers the full messaging flow.
// The allowedExchanges parameter restricts which exchanges can be targeted,
// preventing cross-service message injection. Pass nil to allow all (not recommended).
//
// Request body: { "exchange": "...", "routing_key": "...", "payload": { ... } }
// Response:     { "ok": true, "message_id": "..." }
func PublishHandler(pub messaging.Publisher, allowedExchanges []string, logger *slog.Logger) http.HandlerFunc {
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
		if req.RoutingKey == "" {
			httpx.WriteError(w, http.StatusBadRequest, "routing_key is required")
			return
		}

		if !allowAny {
			if _, ok := allowed[req.Exchange]; !ok {
				httpx.WriteError(w, http.StatusBadRequest, "exchange not allowed")
				return
			}
		}

		msg, err := messaging.NewMessage(req.RoutingKey, req.Payload)
		if err != nil {
			logger.Error("debug publish: failed to create message", redact.Error(err))
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create message")
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
