package amqpbackend

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

// --- extractStringHeaders ---

func TestExtractStringHeaders_NilHeaders(t *testing.T) {
	result := extractStringHeaders(nil)
	assert.Nil(t, result)
}

func TestExtractStringHeaders_EmptyTable(t *testing.T) {
	result := extractStringHeaders(amqp.Table{})
	assert.Nil(t, result)
}

func TestExtractStringHeaders_OnlyNonString(t *testing.T) {
	h := amqp.Table{
		"x-death": []any{amqp.Table{"queue": "q"}},
		"count":   int64(3),
		"flag":    true,
	}
	result := extractStringHeaders(h)
	assert.Nil(t, result, "all non-string values should yield nil")
}

func TestExtractStringHeaders_MixedTypes(t *testing.T) {
	h := amqp.Table{
		"trace-id":   "abc-123",
		"request-id": "req-456",
		"x-death":    []any{amqp.Table{"queue": "q"}},
		"retries":    int64(2),
	}
	result := extractStringHeaders(h)
	require.NotNil(t, result)
	assert.Equal(t, "abc-123", result["trace-id"])
	assert.Equal(t, "req-456", result["request-id"])
	assert.NotContains(t, result, "x-death")
	assert.NotContains(t, result, "retries")
}

func TestExtractStringHeaders_AllStrings(t *testing.T) {
	h := amqp.Table{
		"a": "alpha",
		"b": "beta",
	}
	result := extractStringHeaders(h)
	require.NotNil(t, result)
	assert.Equal(t, map[string]string{"a": "alpha", "b": "beta"}, result)
}

// --- headerToMap ---

func TestHeaderToMap_Nil(t *testing.T) {
	result := headerToMap(nil)
	assert.Nil(t, result)
}

func TestHeaderToMap_EmptyTable(t *testing.T) {
	result := headerToMap(amqp.Table{})
	require.NotNil(t, result)
	assert.Empty(t, result)
}

func TestHeaderToMap_CopiesTable(t *testing.T) {
	original := amqp.Table{
		"key": "value",
		"num": int64(42),
	}
	result := headerToMap(original)
	require.NotNil(t, result)
	assert.Equal(t, "value", result["key"])
	assert.Equal(t, int64(42), result["num"])

	// Mutating the copy must not affect the original.
	result["key"] = "mutated"
	assert.Equal(t, "value", original["key"], "original should be unaffected by mutation of copy")
}

// --- deepCopyValue ---

func TestDeepCopyValue_Table(t *testing.T) {
	inner := amqp.Table{"nested-key": "nested-val"}
	copy := deepCopyValue(inner)

	copiedTable, ok := copy.(map[string]any)
	require.True(t, ok, "expected deep copy to return map[string]any for amqp.Table")
	assert.Equal(t, "nested-val", copiedTable["nested-key"])

	// Mutating the copy must not affect the original.
	copiedTable["nested-key"] = "changed"
	assert.Equal(t, "nested-val", inner["nested-key"], "inner table should be unchanged")
}

func TestDeepCopyValue_Slice(t *testing.T) {
	original := []any{"a", int64(1), amqp.Table{"x": "y"}}
	copy := deepCopyValue(original)

	copiedSlice, ok := copy.([]any)
	require.True(t, ok, "expected deep copy to return []any for []any")
	assert.Len(t, copiedSlice, 3)
	assert.Equal(t, "a", copiedSlice[0])
	assert.Equal(t, int64(1), copiedSlice[1])

	// Mutating the copied slice must not affect the original slice length.
	copiedSlice[0] = "mutated"
	assert.Equal(t, "a", original[0], "original slice element should be unchanged")
}

func TestDeepCopyValue_Bytes(t *testing.T) {
	original := []byte{0x01, 0x02, 0x03}
	copy := deepCopyValue(original)

	copiedBytes, ok := copy.([]byte)
	require.True(t, ok, "expected deep copy to return []byte")
	assert.Equal(t, original, copiedBytes)

	// Mutating the copy must not affect the original.
	copiedBytes[0] = 0xFF
	assert.Equal(t, byte(0x01), original[0], "original byte slice should be unchanged")
}

func TestDeepCopyValue_Scalar_Int(t *testing.T) {
	result := deepCopyValue(int64(99))
	assert.Equal(t, int64(99), result)
}

func TestDeepCopyValue_Scalar_String(t *testing.T) {
	result := deepCopyValue("hello")
	assert.Equal(t, "hello", result)
}

func TestDeepCopyValue_Scalar_Bool(t *testing.T) {
	result := deepCopyValue(true)
	assert.Equal(t, true, result)
}

func TestDeepCopyValue_Nil(t *testing.T) {
	result := deepCopyValue(nil)
	assert.Nil(t, result)
}

// --- fromAMQPDelivery ---

func TestFromAMQPDelivery_FieldMapping(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", map[string]string{"payload": "data"})
	require.NoError(t, err)

	rawDelivery := amqp.Delivery{
		ReplyTo:       "reply.queue",
		CorrelationId: "corr-789",
		Exchange:      "events",
		RoutingKey:    "events.created",
		Redelivered:   true,
		Headers: amqp.Table{
			"X-Trace-Id": "trace-001",
			"x-death":    []any{amqp.Table{"queue": "q", "reason": "rejected", "count": int64(1)}},
		},
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	assert.Equal(t, "reply.queue", d.ReplyTo)
	assert.Equal(t, "corr-789", d.CorrelationID)
	assert.Equal(t, "events", d.Exchange)
	assert.Equal(t, "events.created", d.RoutingKey)
	assert.True(t, d.Redelivered)

	// String headers are extracted into msg.Headers.
	assert.Equal(t, "trace-001", d.Message.Headers["X-Trace-Id"])

	// Full headers (including non-string) are deep-copied into d.Headers.
	require.NotNil(t, d.Headers)
	assert.Equal(t, "trace-001", d.Headers["X-Trace-Id"])
	assert.Contains(t, d.Headers, "x-death")
}

func TestFromAMQPDelivery_NoStringHeaders(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	rawDelivery := amqp.Delivery{
		Exchange:   "ex",
		RoutingKey: "rk",
		Headers: amqp.Table{
			"x-death": []any{amqp.Table{"queue": "q", "reason": "rejected", "count": int64(1)}},
		},
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	// No string-valued headers — msg.Headers should be nil.
	assert.Nil(t, d.Message.Headers)
	// Full headers should still be present.
	assert.Contains(t, d.Headers, "x-death")
}

func TestFromAMQPDelivery_NilHeaders(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	rawDelivery := amqp.Delivery{
		Exchange:   "ex",
		RoutingKey: "rk",
		Headers:    nil,
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	assert.Nil(t, d.Message.Headers)
	assert.Nil(t, d.Headers)
}

func TestFromAMQPDelivery_PreservesMessageID(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", map[string]string{"id": "42"})
	require.NoError(t, err)
	originalID := msg.ID

	rawDelivery := amqp.Delivery{
		Exchange:   "orders",
		RoutingKey: "order.created",
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	assert.Equal(t, originalID, d.Message.ID)
	assert.Equal(t, "order.created", d.Message.Type)
}
