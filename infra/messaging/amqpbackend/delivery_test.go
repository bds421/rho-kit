package amqpbackend

import (
	"fmt"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
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

	result["key"] = "mutated"
	assert.Equal(t, "value", original["key"], "original should be unaffected by mutation of copy")
}

// --- deepCopyValue ---

// budgets for happy-path deep-copy tests (plenty of room).
func freshBudgets() (nodes, bytes int) {
	return maxHeaderNodes, maxHeaderBytes
}

func TestDeepCopyValue_Table(t *testing.T) {
	inner := amqp.Table{"nested-key": "nested-val"}
	n, b := freshBudgets()
	copy := deepCopyValue(inner, 0, &n, &b)

	copiedTable, ok := copy.(map[string]any)
	require.True(t, ok, "expected deep copy to return map[string]any for amqp.Table")
	assert.Equal(t, "nested-val", copiedTable["nested-key"])

	copiedTable["nested-key"] = "changed"
	assert.Equal(t, "nested-val", inner["nested-key"], "inner table should be unchanged")
}

func TestDeepCopyValue_Slice(t *testing.T) {
	original := []any{"a", int64(1), amqp.Table{"x": "y"}}
	copy := func() any { n, b := freshBudgets(); return deepCopyValue(original, 0, &n, &b) }()

	copiedSlice, ok := copy.([]any)
	require.True(t, ok, "expected deep copy to return []any for []any")
	assert.Len(t, copiedSlice, 3)
	assert.Equal(t, "a", copiedSlice[0])
	assert.Equal(t, int64(1), copiedSlice[1])

	copiedSlice[0] = "mutated"
	assert.Equal(t, "a", original[0], "original slice element should be unchanged")
}

func TestDeepCopyValue_Bytes(t *testing.T) {
	original := []byte{0x01, 0x02, 0x03}
	copy := func() any { n, b := freshBudgets(); return deepCopyValue(original, 0, &n, &b) }()

	copiedBytes, ok := copy.([]byte)
	require.True(t, ok, "expected deep copy to return []byte")
	assert.Equal(t, original, copiedBytes)

	copiedBytes[0] = 0xFF
	assert.Equal(t, byte(0x01), original[0], "original byte slice should be unchanged")
}

func TestDeepCopyValue_Scalar_Int(t *testing.T) {
	result := func() any { n, b := freshBudgets(); return deepCopyValue(int64(99), 0, &n, &b) }()
	assert.Equal(t, int64(99), result)
}

func TestDeepCopyValue_Scalar_String(t *testing.T) {
	result := func() any { n, b := freshBudgets(); return deepCopyValue("hello", 0, &n, &b) }()
	assert.Equal(t, "hello", result)
}

func TestDeepCopyValue_Scalar_Bool(t *testing.T) {
	result := func() any { n, b := freshBudgets(); return deepCopyValue(true, 0, &n, &b) }()
	assert.Equal(t, true, result)
}

func TestDeepCopyValue_Nil(t *testing.T) {
	result := func() any { n, b := freshBudgets(); return deepCopyValue(nil, 0, &n, &b) }()
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
	assert.Equal(t, "trace-001", d.Message.Headers["X-Trace-Id"])
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

	assert.Nil(t, d.Message.Headers)
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

func TestFromAMQPDelivery_DetachesMessagePayload(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", map[string]string{"id": "42"})
	require.NoError(t, err)

	d := fromAMQPDelivery(amqp.Delivery{}, msg)
	msg.Payload[1] = 'X'
	d.Message.Payload[2] = 'Y'

	assert.Equal(t, `{Xid":"42"}`, string(msg.Payload))
	assert.Equal(t, `{"Yd":"42"}`, string(d.Message.Payload))
}

// --- extractSchemaVersion ---

func TestExtractSchemaVersion_FromInt32Header(t *testing.T) {
	h := amqp.Table{messaging.HeaderSchemaVersion: int32(3)}
	v := extractSchemaVersion(h, 0)
	assert.Equal(t, uint(3), v)
}

func TestExtractSchemaVersion_FromInt64Header(t *testing.T) {
	h := amqp.Table{messaging.HeaderSchemaVersion: int64(5)}
	v := extractSchemaVersion(h, 0)
	assert.Equal(t, uint(5), v)
}

func TestExtractSchemaVersion_FromIntHeader(t *testing.T) {
	h := amqp.Table{messaging.HeaderSchemaVersion: 7}
	v := extractSchemaVersion(h, 0)
	assert.Equal(t, uint(7), v)
}

func TestExtractSchemaVersion_MissingHeader_UsesFallback(t *testing.T) {
	h := amqp.Table{"other": "value"}
	v := extractSchemaVersion(h, 2)
	assert.Equal(t, uint(2), v)
}

func TestExtractSchemaVersion_NilHeaders_UsesFallback(t *testing.T) {
	v := extractSchemaVersion(nil, 4)
	assert.Equal(t, uint(4), v)
}

func TestExtractSchemaVersion_UnsupportedType_UsesFallback(t *testing.T) {
	h := amqp.Table{messaging.HeaderSchemaVersion: "not-a-number"}
	v := extractSchemaVersion(h, 1)
	assert.Equal(t, uint(1), v)
}

func TestExtractSchemaVersion_NegativeHeader_ClampsToZero(t *testing.T) {
	h := amqp.Table{messaging.HeaderSchemaVersion: int32(-5)}
	v := extractSchemaVersion(h, 99)
	assert.Equal(t, uint(0), v)
}

// --- fromAMQPDelivery schema version ---

func TestFromAMQPDelivery_SchemaVersionFromHeader(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	rawDelivery := amqp.Delivery{
		Exchange:   "events",
		RoutingKey: "test.event",
		Headers: amqp.Table{
			messaging.HeaderSchemaVersion: int32(2),
		},
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	assert.Equal(t, uint(2), d.SchemaVersion)
	assert.Equal(t, uint(2), d.Message.SchemaVersion)
}

func TestFromAMQPDelivery_SchemaVersionFallsBackToBody(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	msg.SchemaVersion = 4

	rawDelivery := amqp.Delivery{
		Exchange:   "events",
		RoutingKey: "test.event",
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	assert.Equal(t, uint(4), d.SchemaVersion)
	assert.Equal(t, uint(4), d.Message.SchemaVersion)
}

func TestFromAMQPDelivery_SchemaVersionZeroWhenAbsent(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	rawDelivery := amqp.Delivery{
		Exchange:   "events",
		RoutingKey: "test.event",
	}

	d := fromAMQPDelivery(rawDelivery, msg)

	assert.Equal(t, uint(0), d.SchemaVersion)
	assert.Equal(t, uint(0), d.Message.SchemaVersion)
}

// TestExtractStringHeaders_RejectsOversizedAggregate guards L134: a peer
// that emits a small number of headers but with very large values must
// not exhaust memory through the byte axis. The aggregate name+value
// budget caps materialised headers at maxHeaderBytes total.
func TestExtractStringHeaders_RejectsOversizedAggregate(t *testing.T) {
	// One header at exactly the byte budget admits.
	atBudget := amqp.Table{
		"k": strings.Repeat("x", maxHeaderBytes-1),
	}
	result := extractStringHeaders(atBudget)
	require.NotNil(t, result)
	require.Len(t, result, 1)

	// A single value larger than the budget is dropped — the loop breaks
	// before adding it. Result is nil (no headers materialised).
	overBudget := amqp.Table{
		"k": strings.Repeat("x", maxHeaderBytes+1),
	}
	result = extractStringHeaders(overBudget)
	require.Nil(t, result, "value larger than aggregate byte budget must not materialise")

	// Many small values that collectively exceed the budget: subsequent
	// headers after the budget is consumed are dropped. We accept
	// exactly as many as fit (map iteration order is non-deterministic
	// so we assert on byte sum, not specific keys).
	manyValues := amqp.Table{}
	const each = 1024
	const count = (maxHeaderBytes / each) + 16 // overshoot by 16 headers
	for i := 0; i < count; i++ {
		manyValues[fmt.Sprintf("k-%03d", i)] = strings.Repeat("v", each-len("k-000"))
	}
	result = extractStringHeaders(manyValues)
	require.NotNil(t, result)
	totalBytes := 0
	for k, v := range result {
		totalBytes += len(k) + len(v)
	}
	require.LessOrEqual(t, totalBytes, maxHeaderBytes,
		"materialised header bytes must not exceed aggregate budget")
}

// TestExtractStringHeaders_RejectsOversizedCount guards the existing
// node-count cap independent of the new byte budget — a peer emitting
// thousands of one-byte headers should still be capped at maxHeaderNodes.
func TestExtractStringHeaders_RejectsOversizedCount(t *testing.T) {
	h := amqp.Table{}
	for i := 0; i < maxHeaderNodes*2; i++ {
		h[fmt.Sprintf("k-%05d", i)] = "v"
	}
	result := extractStringHeaders(h)
	require.NotNil(t, result)
	require.LessOrEqual(t, len(result), maxHeaderNodes,
		"materialised header count must not exceed maxHeaderNodes")
}
