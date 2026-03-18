// Package debughttp provides HTTP handlers for debugging AMQP message
// consumption and publishing. Import this package only if you need
// debug endpoints — it pulls in httpx as a dependency.
package debughttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/infra/messaging"
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
			httpx.WriteError(w, http.StatusBadRequest, "unknown message type: "+req.Type)
			return
		}

		msg, err := messaging.NewMessage(req.Type, req.Payload)
		if err != nil {
			logger.Error("debug consume: failed to create message", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create message")
			return
		}

		d := messaging.Delivery{
			Message: msg,
		}

		logger.Info("debug consume", "type", req.Type, "message_id", msg.ID)

		if err := handler(r.Context(), d); err != nil {
			logger.Error("debug consume handler failed", "type", req.Type, "error", err)
			httpx.WriteJSON(w, http.StatusInternalServerError, response{
				OK:        false,
				MessageID: msg.ID,
				Error:     "handler failed",
			})
			return
		}

		httpx.WriteJSON(w, http.StatusOK, response{OK: true, MessageID: msg.ID})
	}
}

// ConsumeTypesHandler returns an HTTP handler that lists all registered
// consumer message types. Useful to discover what types the consume endpoint accepts.
func ConsumeTypesHandler(handlers map[string]messaging.Handler) http.HandlerFunc {
	types := make([]string, 0, len(handlers))
	for t := range handlers {
		types = append(types, t)
	}
	sort.Strings(types)

	return func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, typesResponse{Types: types})
	}
}

// PublishHandler returns an HTTP handler that publishes a message
// to a RabbitMQ exchange via REST. This triggers the full messaging flow.
// The allowedExchanges parameter restricts which exchanges can be targeted,
// preventing cross-service message injection. Pass nil to allow all (not recommended).
//
// Request body: { "exchange": "...", "routing_key": "...", "payload": { ... } }
// Response:     { "ok": true, "message_id": "..." }
func PublishHandler(pub messaging.MessagePublisher, allowedExchanges []string, logger *slog.Logger) http.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedExchanges))
	for _, e := range allowedExchanges {
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

		if len(allowed) > 0 {
			if _, ok := allowed[req.Exchange]; !ok {
				httpx.WriteError(w, http.StatusBadRequest, "exchange not allowed")
				return
			}
		}

		msg, err := messaging.NewMessage(req.RoutingKey, req.Payload)
		if err != nil {
			logger.Error("debug publish: failed to create message", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create message")
			return
		}

		logger.Info("debug publish",
			"exchange", req.Exchange,
			"routing_key", req.RoutingKey,
			"message_id", msg.ID,
		)

		if err := pub.Publish(r.Context(), req.Exchange, req.RoutingKey, msg); err != nil {
			logger.Error("debug publish failed", "error", err)
			httpx.WriteJSON(w, http.StatusInternalServerError, response{
				OK:        false,
				MessageID: msg.ID,
				Error:     "publish failed",
			})
			return
		}

		httpx.WriteJSON(w, http.StatusOK, response{OK: true, MessageID: msg.ID})
	}
}
