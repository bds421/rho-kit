package debughttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// --- ConsumeHandler ---

func TestConsumeHandler_Success(t *testing.T) {
	var received messaging.Delivery
	handlers := map[string]messaging.Handler{
		"order.created": func(_ context.Context, d messaging.Delivery) error {
			received = d
			return nil
		},
	}

	body := `{"type":"order.created","payload":{"id":"42"}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, discardLogger())(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
	assert.NotEmpty(t, resp.MessageID)

	assert.Equal(t, "order.created", received.Message.Type)
}

func TestConsumeHandler_InvalidJSON(t *testing.T) {
	handlers := map[string]messaging.Handler{}

	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(`{invalid`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestConsumeHandler_EmptyType(t *testing.T) {
	handlers := map[string]messaging.Handler{}

	body := `{"type":"","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "type is required")
}

func TestConsumeHandler_UnknownType(t *testing.T) {
	handlers := map[string]messaging.Handler{
		"order.created": func(_ context.Context, _ messaging.Delivery) error { return nil },
	}

	body := `{"type":"user.deleted","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown message type")
}

func TestConsumeHandler_HandlerError(t *testing.T) {
	handlers := map[string]messaging.Handler{
		"order.created": func(_ context.Context, _ messaging.Delivery) error {
			return errors.New("processing failed")
		},
	}

	body := `{"type":"order.created","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, discardLogger())(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.OK)
	assert.Equal(t, "handler failed", resp.Error)
	assert.NotEmpty(t, resp.MessageID)
}

// --- ConsumeTypesHandler ---

func TestConsumeTypesHandler_ListsTypes(t *testing.T) {
	handlers := map[string]messaging.Handler{
		"order.created": func(_ context.Context, _ messaging.Delivery) error { return nil },
		"user.updated":  func(_ context.Context, _ messaging.Delivery) error { return nil },
		"file.deleted":  func(_ context.Context, _ messaging.Delivery) error { return nil },
	}

	req := httptest.NewRequest(http.MethodGet, "/debug/consume/types", nil)
	rec := httptest.NewRecorder()

	ConsumeTypesHandler(handlers)(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp typesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Types should be sorted.
	assert.Equal(t, []string{"file.deleted", "order.created", "user.updated"}, resp.Types)
}

func TestConsumeTypesHandler_EmptyHandlers(t *testing.T) {
	handlers := map[string]messaging.Handler{}

	req := httptest.NewRequest(http.MethodGet, "/debug/consume/types", nil)
	rec := httptest.NewRecorder()

	ConsumeTypesHandler(handlers)(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp typesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.Types)
}

// --- PublishHandler ---

type fakePublisher struct {
	lastExchange   string
	lastRoutingKey string
	lastMsg        messaging.Message
	err            error
}

func (f *fakePublisher) Publish(_ context.Context, exchange, routingKey string, msg messaging.Message) error {
	f.lastExchange = exchange
	f.lastRoutingKey = routingKey
	f.lastMsg = msg
	return f.err
}

func TestPublishHandler_Success(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"events","routing_key":"order.created","payload":{"id":"1"}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, nil, discardLogger())(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
	assert.NotEmpty(t, resp.MessageID)

	assert.Equal(t, "events", pub.lastExchange)
	assert.Equal(t, "order.created", pub.lastRoutingKey)
}

func TestPublishHandler_InvalidJSON(t *testing.T) {
	pub := &fakePublisher{}

	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, nil, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPublishHandler_MissingExchange(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"","routing_key":"rk","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, nil, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "exchange is required")
}

func TestPublishHandler_MissingRoutingKey(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"ex","routing_key":"","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, nil, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "routing_key is required")
}

func TestPublishHandler_PublishError(t *testing.T) {
	pub := &fakePublisher{err: errors.New("broker down")}

	body := `{"exchange":"ex","routing_key":"rk","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, nil, discardLogger())(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.OK)
	assert.Equal(t, "publish failed", resp.Error)
}

func TestPublishHandler_ExchangeNotAllowed(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"forbidden","routing_key":"rk","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"allowed-exchange"}, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "exchange not allowed")
}

func TestPublishHandler_ExchangeAllowed(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"allowed-exchange","routing_key":"rk","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"allowed-exchange"}, discardLogger())(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "allowed-exchange", pub.lastExchange)
}
