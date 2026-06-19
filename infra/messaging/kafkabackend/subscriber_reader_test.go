package kafkabackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewReader_ScopesToBindingTopic guards against the multi-topic
// cross-delivery bug: a Subscriber constructed with several topics must
// build a Reader scoped to the single binding topic, never the whole
// topic set. If GroupTopics carried every constructor topic, records
// for one topic would reach a handler bound to another.
func TestNewReader_ScopesToBindingTopic(t *testing.T) {
	tests := []struct {
		name    string
		topics  []string
		binding string
	}{
		{name: "single topic", topics: []string{"events"}, binding: "events"},
		{name: "multi topic first", topics: []string{"events", "audit"}, binding: "events"},
		{name: "multi topic second", topics: []string{"events", "audit"}, binding: "audit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := NewSubscriberWithConfig(Config{
				Brokers:       []string{"localhost:9092"},
				AllowInsecure: true,
			}, "orders", tc.topics)
			require.NoError(t, err)

			reader, err := sub.newReader(tc.binding)
			require.NoError(t, err)
			t.Cleanup(func() { _ = reader.Close() })

			rc := reader.Config()
			assert.Equal(t, tc.binding, rc.Topic,
				"reader must be scoped to the single binding topic")
			assert.Empty(t, rc.GroupTopics,
				"reader must not subscribe to the whole topic set via GroupTopics")
			assert.Equal(t, "orders", rc.GroupID)
		})
	}
}
