package messaging_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// --- ValidateMessage tests ---

func TestValidateMessage_ValidPayloadPasses(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age":  {"type": "integer"}
		},
		"required": ["name", "age"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	msg := messaging.Message{
		ID:            "msg-1",
		Type:          "user.created",
		Payload:       json.RawMessage(`{"name":"Alice","age":30}`),
		SchemaVersion: 1,
	}

	err := reg.ValidateMessage(msg)
	assert.NoError(t, err)
}

func TestValidateMessage_InvalidPayloadFails(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age":  {"type": "integer"}
		},
		"required": ["name", "age"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	msg := messaging.Message{
		ID:            "msg-2",
		Type:          "user.created",
		Payload:       json.RawMessage(`{"name":"secret-token","age":"not-a-number"}`),
		SchemaVersion: 1,
	}

	err := reg.ValidateMessage(msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation failed")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestValidateMessage_MissingRequiredFieldFails(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"required": ["name"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	msg := messaging.Message{
		ID:            "msg-3",
		Type:          "user.created",
		Payload:       json.RawMessage(`{"email":"alice@example.com"}`),
		SchemaVersion: 1,
	}

	err := reg.ValidateMessage(msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation failed")
}

// TestValidateMessage_PreservesUnderlyingCause asserts the jsonschema
// failure cause survives in the error chain (errors.Unwrap) rather than
// being discarded — operators need the underlying triage signal, while
// the rendered text stays redacted.
func TestValidateMessage_PreservesUnderlyingCause(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"age": {"type": "integer"}},
		"required": ["age"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	msg := messaging.Message{
		ID:            "msg-cause",
		Type:          "user.created",
		Payload:       json.RawMessage(`{"age":"not-a-number"}`),
		SchemaVersion: 1,
	}

	err := reg.ValidateMessage(msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation failed")
	require.NotNil(t, errors.Unwrap(err),
		"validation error must wrap the jsonschema cause, not discard it")
}

func TestValidateMessage_UnknownVersionPasses(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type": "object"}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	msg := messaging.Message{
		ID:            "msg-4",
		Type:          "user.created",
		Payload:       json.RawMessage(`{"anything":"goes"}`),
		SchemaVersion: 99,
	}

	err := reg.ValidateMessage(msg)
	assert.NoError(t, err)
}

func TestValidateMessage_UnversionedMessagePasses(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type": "object", "required": ["name"]}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	// Version 0 = unversioned; no schema registered for v0 -> passes.
	msg := messaging.Message{
		ID:            "msg-5",
		Type:          "user.created",
		Payload:       json.RawMessage(`{}`),
		SchemaVersion: 0,
	}

	err := reg.ValidateMessage(msg)
	assert.NoError(t, err)
}

func TestValidateMessage_UnknownMessageTypePasses(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()

	msg := messaging.Message{
		ID:            "msg-6",
		Type:          "unknown.event",
		Payload:       json.RawMessage(`{"anything":"goes"}`),
		SchemaVersion: 1,
	}

	err := reg.ValidateMessage(msg)
	assert.NoError(t, err)
}

// --- Schema compilation error at registration time ---

func TestRegister_InvalidJSONSchemaFails(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	invalidSchema := json.RawMessage(`not valid json`)

	err := reg.Register("secret-token.created", 1, invalidSchema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compile schema")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestRegister_InvalidSchemaTypeFails(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	// "type" must be a string or array of strings, not an integer.
	badSchema := json.RawMessage(`{"type": 42}`)

	err := reg.Register("secret-token.created", 1, badSchema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compile schema")
	assert.NotContains(t, err.Error(), "secret-token")
}

// --- ValidatingHandler tests ---

func TestValidatingHandler_PassesValidPayload(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	var nextCalled bool
	next := func(_ context.Context, _ messaging.Delivery) error {
		nextCalled = true
		return nil
	}

	h := messaging.NewValidatingHandler(reg, next)

	d := messaging.Delivery{
		SchemaVersion: 1,
		Message: messaging.Message{
			ID:            "msg-v1",
			Type:          "user.created",
			Payload:       json.RawMessage(`{"name":"Bob"}`),
			SchemaVersion: 1,
		},
	}

	err := h(context.Background(), d)
	require.NoError(t, err)
	assert.True(t, nextCalled)
}

func TestValidatingHandler_RejectsInvalidPayload(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"]
	}`)
	require.NoError(t, reg.Register("secret-token.created", 1, schema))

	var nextCalled bool
	next := func(_ context.Context, _ messaging.Delivery) error {
		nextCalled = true
		return nil
	}

	h := messaging.NewValidatingHandler(reg, next)

	d := messaging.Delivery{
		SchemaVersion: 1,
		Message: messaging.Message{
			ID:            "secret-token-id",
			Type:          "secret-token.created",
			Payload:       json.RawMessage(`{"name": 123}`),
			SchemaVersion: 1,
		},
	}

	err := h(context.Background(), d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message validation failed")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.False(t, nextCalled)
}

func TestValidatingHandler_PassesUnregisteredVersion(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()

	var nextCalled bool
	next := func(_ context.Context, _ messaging.Delivery) error {
		nextCalled = true
		return nil
	}

	h := messaging.NewValidatingHandler(reg, next)

	d := messaging.Delivery{
		SchemaVersion: 5,
		Message: messaging.Message{
			ID:            "msg-v3",
			Type:          "unknown.type",
			Payload:       json.RawMessage(`{"anything":"goes"}`),
			SchemaVersion: 5,
		},
	}

	err := h(context.Background(), d)
	require.NoError(t, err)
	assert.True(t, nextCalled)
}

// TestValidatingHandler_StrictRejectsUnknownVersionForKnownType verifies
// the additive strict mode: when a message type HAS registered schemas
// but the delivered (transport-header-controlled) version is not among
// them, strict mode rejects rather than passing the unvalidated payload
// through. This closes the "X-Schema-Version: 999 skips validation"
// bypass for types that opted into schemas.
func TestValidatingHandler_StrictRejectsUnknownVersionForKnownType(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	var nextCalled bool
	next := func(_ context.Context, _ messaging.Delivery) error {
		nextCalled = true
		return nil
	}

	h := messaging.NewValidatingHandler(reg, next, messaging.WithStrictUnknownVersion())

	// Type user.created has schemas (v1) but the delivery claims v999.
	d := messaging.Delivery{
		SchemaVersion: 999,
		Message: messaging.Message{
			ID:      "msg-strict",
			Type:    "user.created",
			Payload: json.RawMessage(`{"name":123}`),
		},
	}

	err := h(context.Background(), d)
	require.Error(t, err)
	assert.False(t, nextCalled, "strict mode must not dispatch unvalidated payload")
}

// TestValidatingHandler_StrictAllowsUnversionedAndUnknownType verifies
// strict mode keeps the legacy pass-throughs that are intentional:
// version 0 (unversioned/legacy) and types with NO registered schemas.
func TestValidatingHandler_StrictAllowsUnversionedAndUnknownType(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type": "object", "required": ["name"]}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	cases := []messaging.Delivery{
		{ // version 0 = unversioned legacy, passes
			SchemaVersion: 0,
			Message:       messaging.Message{ID: "m0", Type: "user.created", Payload: json.RawMessage(`{}`)},
		},
		{ // type with no registered schemas at all, passes
			SchemaVersion: 5,
			Message:       messaging.Message{ID: "m1", Type: "unknown.type", Payload: json.RawMessage(`{"x":1}`)},
		},
	}

	for i, d := range cases {
		var nextCalled bool
		next := func(_ context.Context, _ messaging.Delivery) error {
			nextCalled = true
			return nil
		}
		h := messaging.NewValidatingHandler(reg, next, messaging.WithStrictUnknownVersion())
		if err := h(context.Background(), d); err != nil {
			t.Fatalf("case %d: strict mode rejected an intentional pass-through: %v", i, err)
		}
		if !nextCalled {
			t.Fatalf("case %d: strict mode dropped a message that should pass through", i)
		}
	}
}

// TestValidatingHandler_DefaultStillPassesUnknownVersion guards that the
// default (non-strict) behaviour is unchanged: an unknown version for a
// type with registered schemas still passes through.
func TestValidatingHandler_DefaultStillPassesUnknownVersion(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type": "object", "required": ["name"]}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	var nextCalled bool
	next := func(_ context.Context, _ messaging.Delivery) error {
		nextCalled = true
		return nil
	}
	h := messaging.NewValidatingHandler(reg, next) // default mode

	d := messaging.Delivery{
		SchemaVersion: 999,
		Message:       messaging.Message{ID: "m", Type: "user.created", Payload: json.RawMessage(`{}`)},
	}
	require.NoError(t, h(context.Background(), d))
	assert.True(t, nextCalled)
}

func TestValidatingHandler_PanicOnNilRegistry(t *testing.T) {
	assert.Panics(t, func() {
		messaging.NewValidatingHandler(nil, func(_ context.Context, _ messaging.Delivery) error {
			return nil
		})
	})
}

func TestValidatingHandler_PanicOnNilNext(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	assert.Panics(t, func() {
		messaging.NewValidatingHandler(reg, nil)
	})
}

func TestValidatingHandler_ComposesWithVersionedHandler(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"]
	}`)
	require.NoError(t, reg.Register("user.created", 1, schema))

	var handlerVersion int
	handlers := map[messaging.SchemaVersion]messaging.Handler{
		1: func(_ context.Context, _ messaging.Delivery) error {
			handlerVersion = 1
			return nil
		},
	}

	h := messaging.NewValidatingHandler(reg, messaging.NewVersionedHandler(handlers))

	d := messaging.Delivery{
		SchemaVersion: 1,
		Message: messaging.Message{
			ID:            "msg-composed",
			Type:          "user.created",
			Payload:       json.RawMessage(`{"name":"Charlie"}`),
			SchemaVersion: 1,
		},
	}

	err := h(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, 1, handlerVersion)
}

func TestValidatingHandler_UsesDeliverySchemaVersion(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"]
	}`)
	require.NoError(t, reg.Register("user.created", 2, schema))

	var nextCalled bool
	next := func(_ context.Context, _ messaging.Delivery) error {
		nextCalled = true
		return nil
	}

	h := messaging.NewValidatingHandler(reg, next)

	// Delivery SchemaVersion=2 matches registration; Message SchemaVersion
	// is irrelevant — the handler uses the delivery-level version.
	d := messaging.Delivery{
		SchemaVersion: 2,
		Message: messaging.Message{
			ID:      "msg-dv",
			Type:    "user.created",
			Payload: json.RawMessage(`{"name":"Dana"}`),
		},
	}

	err := h(context.Background(), d)
	require.NoError(t, err)
	assert.True(t, nextCalled)
}
