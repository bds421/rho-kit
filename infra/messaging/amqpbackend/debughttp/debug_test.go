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

	"github.com/bds421/rho-kit/infra/v2/messaging"
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

func TestConsumeHandler_PanicsOnNilHandlers(t *testing.T) {
	require.Panics(t, func() {
		ConsumeHandler(nil, discardLogger())
	})
}

func TestConsumeHandler_PanicsOnNilHandlerValue(t *testing.T) {
	require.PanicsWithValue(t, "debughttp: ConsumeHandler requires non-nil handlers", func() {
		ConsumeHandler(map[string]messaging.Handler{"order.created": nil}, discardLogger())
	})
}

func TestConsumeHandler_DetachesHandlersMap(t *testing.T) {
	calledOriginal := false
	handlers := map[string]messaging.Handler{
		"order.created": func(_ context.Context, _ messaging.Delivery) error {
			calledOriginal = true
			return nil
		},
	}

	h := ConsumeHandler(handlers, discardLogger())
	handlers["order.created"] = func(_ context.Context, _ messaging.Delivery) error {
		t.Fatal("mutated handler map was used")
		return nil
	}
	delete(handlers, "order.created")

	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(`{"type":"order.created","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, calledOriginal)
}

func TestConsumeHandler_NilLoggerUsesDefault(t *testing.T) {
	handlers := map[string]messaging.Handler{
		"order.created": func(_ context.Context, _ messaging.Delivery) error { return nil },
	}
	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(`{"type":"order.created","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, nil)(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
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

	body := `{"type":"user.deleted.secret-token","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/consume", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ConsumeHandler(handlers, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown message type")
	assert.NotContains(t, rec.Body.String(), "user.deleted.secret-token")
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

func TestConsumeTypesHandler_PanicsOnNilHandlers(t *testing.T) {
	require.Panics(t, func() {
		ConsumeTypesHandler(nil)
	})
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

	PublishHandler(pub, []string{"*"}, discardLogger())(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
	assert.NotEmpty(t, resp.MessageID)

	assert.Equal(t, "events", pub.lastExchange)
	assert.Equal(t, "order.created", pub.lastRoutingKey)
}

func TestPublishHandler_PanicsOnNilPublisher(t *testing.T) {
	require.Panics(t, func() {
		PublishHandler(nil, []string{"*"}, discardLogger())
	})
}

func TestPublishHandler_NilLoggerUsesDefault(t *testing.T) {
	pub := &fakePublisher{}
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(`{"exchange":"events","routing_key":"order.created","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"*"}, nil)(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestPublishHandler_InvalidJSON(t *testing.T) {
	pub := &fakePublisher{}

	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"*"}, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPublishHandler_MissingExchange(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"","routing_key":"rk","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"*"}, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "exchange is required")
}

func TestPublishHandler_MissingRoutingKey(t *testing.T) {
	pub := &fakePublisher{}

	body := `{"exchange":"ex","routing_key":"","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"*"}, discardLogger())(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "routing_key is required")
}

func TestPublishHandler_PublishError(t *testing.T) {
	pub := &fakePublisher{err: errors.New("broker down")}

	body := `{"exchange":"ex","routing_key":"rk","payload":{}}`
	req := httptest.NewRequest(http.MethodPost, "/debug/publish", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	PublishHandler(pub, []string{"*"}, discardLogger())(rec, req)

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

func TestGuard_PanicsOnNilHandler(t *testing.T) {
	assert.Panics(t, func() {
		Guard("development", func(*http.Request) bool { return true }, nil)
	})
}

func TestGuard_HidesWhenNonDevelopment(t *testing.T) {
	called := false
	h := Guard("production", func(*http.Request) bool { return true }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/debug", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, called)
}

func TestGuard_HidesWhenAuthFails(t *testing.T) {
	called := false
	h := Guard("development", func(*http.Request) bool { return false }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/debug", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, `Basic realm="rho-kit debug"`, rec.Header().Get("WWW-Authenticate"))
	assert.False(t, called)
}

func TestGuard_AllowsWhenDevelopmentAndAuthenticated(t *testing.T) {
	h := Guard("development", func(*http.Request) bool { return true }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/debug", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestBasicAuth(t *testing.T) {
	auth := BasicAuth(map[string]string{"alice": "secret", "bob": "hunter2"})

	req := httptest.NewRequest(http.MethodGet, "/debug", nil)
	req.SetBasicAuth("alice", "secret")
	assert.True(t, auth(req))

	req = httptest.NewRequest(http.MethodGet, "/debug", nil)
	req.SetBasicAuth("alice", "wrong")
	assert.False(t, auth(req))

	req = httptest.NewRequest(http.MethodGet, "/debug", nil)
	req.SetBasicAuth("mallory", "secret")
	assert.False(t, auth(req))

	req = httptest.NewRequest(http.MethodGet, "/debug", nil)
	assert.False(t, auth(req))
}

func TestBasicAuth_PanicsOnInvalidConfig(t *testing.T) {
	assert.Panics(t, func() { BasicAuth(nil) })
	assert.Panics(t, func() { BasicAuth(map[string]string{}) })
	assert.Panics(t, func() { BasicAuth(map[string]string{"": "secret"}) })
	assert.Panics(t, func() { BasicAuth(map[string]string{"alice": ""}) })
}

func TestAllowFromHeader(t *testing.T) {
	auth := AllowFromHeader("X-Debug-Token", "secret")

	req := httptest.NewRequest(http.MethodGet, "/debug", nil)
	req.Header.Set("X-Debug-Token", "secret")
	assert.True(t, auth(req))

	req = httptest.NewRequest(http.MethodGet, "/debug", nil)
	req.Header.Set("X-Debug-Token", "wrong")
	assert.False(t, auth(req))
}

func TestAllowFromHeader_RejectsDuplicateHeader(t *testing.T) {
	auth := AllowFromHeader("X-Debug-Token", "secret")

	req := httptest.NewRequest(http.MethodGet, "/debug", nil)
	req.Header.Add("X-Debug-Token", "secret")
	req.Header.Add("X-Debug-Token", "attacker")

	assert.False(t, auth(req))
}

func TestAllowFromHeader_RejectsAmbiguousHeaderValues(t *testing.T) {
	auth := AllowFromHeader("X-Debug-Token", "secret")

	for name, value := range map[string]string{
		"edge whitespace": " secret",
		"internal space":  "sec ret",
		"comma combined":  "secret,attacker",
		"control":         "secret\n",
		"invalid utf8":    string([]byte{'s', 'e', 'c', 'r', 'e', 't', 0xff}),
		"horizontal tab":  "sec\tret",
		"unicode newline": "secret\u2028",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/debug", nil)
			req.Header.Set("X-Debug-Token", value)

			assert.False(t, auth(req))
		})
	}
}

func TestAllowFromHeader_PanicsOnInvalidConfig(t *testing.T) {
	assert.Panics(t, func() { AllowFromHeader("", "secret") })
	assert.Panics(t, func() { AllowFromHeader("Bad Header", "secret") })
	assert.Panics(t, func() { AllowFromHeader("X-Debug-Token", "") })
	assert.Panics(t, func() { AllowFromHeader("X-Debug-Token", "secret\n") })
	assert.Panics(t, func() { AllowFromHeader("X-Debug-Token", "sec ret") })
	assert.Panics(t, func() { AllowFromHeader("X-Debug-Token", "secret,attacker") })
	assert.Panics(t, func() { AllowFromHeader("X-Debug-Token", string([]byte{'s', 'e', 'c', 'r', 'e', 't', 0xff})) })
}
