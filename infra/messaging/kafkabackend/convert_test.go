package kafkabackend

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func newTestMessage(t *testing.T) messaging.Message {
	t.Helper()
	msg, err := messaging.NewMessage("user.created", map[string]string{"name": "alice"})
	require.NoError(t, err)
	withHeader, err := msg.WithHeader(messaging.HeaderCorrelationID, "corr-1")
	require.NoError(t, err)
	return withHeader
}

func TestToKafkaMessage_PopulatesKitHeaders(t *testing.T) {
	msg := newTestMessage(t)
	km, err := toKafkaMessage("events", "user.created", msg)
	require.NoError(t, err)

	assert.Equal(t, "events", km.Topic)
	assert.Equal(t, "user.created", string(km.Key))
	got := map[string]string{}
	for _, h := range km.Headers {
		got[h.Key] = string(h.Value)
	}
	assert.Equal(t, "events", got[headerExchange])
	assert.Equal(t, "user.created", got[headerRoutingKey])
	assert.Equal(t, msg.ID, got[headerMessageID])
	assert.Equal(t, "user.created", got[headerMessageType])
	assert.Equal(t, "corr-1", got[messaging.HeaderCorrelationID])
}

func TestToKafkaMessage_EmptyRoutingKeyNoKey(t *testing.T) {
	msg := newTestMessage(t)
	km, err := toKafkaMessage("events", "", msg)
	require.NoError(t, err)
	assert.Nil(t, km.Key)
}

func TestToKafkaMessage_PropagatesSchemaVersion(t *testing.T) {
	msg := newTestMessage(t).WithSchemaVersion(3)
	km, err := toKafkaMessage("events", "v3", msg)
	require.NoError(t, err)
	found := false
	for _, h := range km.Headers {
		if h.Key == messaging.HeaderSchemaVersion {
			assert.Equal(t, "3", string(h.Value))
			found = true
		}
	}
	assert.True(t, found)
}

func TestFromKafkaMessage_RoundTrip(t *testing.T) {
	msg := newTestMessage(t).WithSchemaVersion(2)
	km, err := toKafkaMessage("events", "user.created", msg)
	require.NoError(t, err)

	d, err := fromKafkaMessage(km)
	require.NoError(t, err)
	assert.Equal(t, "events", d.Exchange)
	assert.Equal(t, "user.created", d.RoutingKey)
	assert.Equal(t, msg.ID, d.Message.ID)
	assert.Equal(t, "user.created", d.Message.Type)
	assert.Equal(t, uint(2), d.SchemaVersion)
	assert.Equal(t, uint(2), d.Message.SchemaVersion)
	assert.Equal(t, "corr-1", d.Message.Headers[messaging.HeaderCorrelationID])
	// Kit-internal envelope headers must not leak into Message.Headers.
	_, ok := d.Message.Headers[headerExchange]
	assert.False(t, ok, "Message.Headers must not include X-Exchange")
}

func TestFromKafkaMessage_FallsBackToTopicWhenHeaderMissing(t *testing.T) {
	msg := newTestMessage(t)
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	km := kafka.Message{
		Topic: "raw-topic",
		Key:   []byte("legacy.key"),
		Value: body,
		Time:  time.Now(),
	}
	d, err := fromKafkaMessage(km)
	require.NoError(t, err)
	assert.Equal(t, "raw-topic", d.Exchange)
	assert.Equal(t, "legacy.key", d.RoutingKey)
}

func TestFromKafkaMessage_RejectsOversizedBody(t *testing.T) {
	big := make([]byte, maxConsumerDeliveryBytes+1)
	km := kafka.Message{Topic: "t", Value: big}
	_, err := fromKafkaMessage(km)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestSplitHeaders_BoundedByByteBudget(t *testing.T) {
	long := strings.Repeat("a", maxDeliveryHeaderBytes/2+1)
	headers := []kafka.Header{
		{Key: "first", Value: []byte(long)},
		{Key: "second", Value: []byte(long)},
	}
	anyH, strH := splitHeaders(headers)
	// Both entries combined would exceed the byte budget; second must be dropped.
	assert.Len(t, anyH, 1)
	assert.Len(t, strH, 1)
}

func TestSplitHeaders_BoundedByCount(t *testing.T) {
	headers := make([]kafka.Header, maxDeliveryHeaders+10)
	for i := range headers {
		headers[i] = kafka.Header{Key: "h" + strings.Repeat("x", 1), Value: []byte("v")}
		headers[i].Key = "h" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	anyH, _ := splitHeaders(headers)
	assert.LessOrEqual(t, len(anyH), maxDeliveryHeaders)
}

func TestToKafkaMessage_DoesNotDuplicateEnvelopeHeaders(t *testing.T) {
	// A caller that smuggles an envelope-header key through
	// msg.Headers must not produce two record headers with the same
	// key — the kit-managed envelope value (set from msg.SchemaVersion,
	// msg.ID, etc.) is the source of truth and the user-supplied
	// duplicate is dropped.
	msg, err := messaging.NewMessage("user.created", map[string]string{"name": "alice"})
	require.NoError(t, err)
	msg = msg.WithSchemaVersion(7)
	smuggled, err := msg.WithHeader(messaging.HeaderSchemaVersion, "999")
	require.NoError(t, err)
	smuggled2, err := smuggled.WithHeader(headerMessageID, "spoofed-id")
	require.NoError(t, err)

	km, err := toKafkaMessage("events", "user.created", smuggled2)
	require.NoError(t, err)

	schemaCount := 0
	idCount := 0
	for _, h := range km.Headers {
		switch h.Key {
		case messaging.HeaderSchemaVersion:
			schemaCount++
			assert.Equal(t, "7", string(h.Value), "kit-managed schema version must win over user-supplied duplicate")
		case headerMessageID:
			idCount++
			assert.Equal(t, smuggled2.ID, string(h.Value), "kit-managed message ID must win over user-supplied duplicate")
		}
	}
	assert.Equal(t, 1, schemaCount, "exactly one X-Schema-Version header must be emitted")
	assert.Equal(t, 1, idCount, "exactly one X-Message-Id header must be emitted")
}

func TestParseSchemaVersion_RejectsNegative(t *testing.T) {
	headers := []kafka.Header{
		{Key: messaging.HeaderSchemaVersion, Value: []byte("-1")},
	}
	assert.Equal(t, uint(0), parseSchemaVersion(headers))
}

func TestParseSchemaVersion_RejectsNonInteger(t *testing.T) {
	headers := []kafka.Header{
		{Key: messaging.HeaderSchemaVersion, Value: []byte("not-an-int")},
	}
	assert.Equal(t, uint(0), parseSchemaVersion(headers))
}
