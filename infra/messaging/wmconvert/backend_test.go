package wmconvert_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/wmconvert"
)

func TestBackend_PublishViaAdapter(t *testing.T) {
	goChan := gochannel.NewGoChannel(gochannel.Config{}, watermill.NopLogger{})

	backend := wmconvert.NewBackend(goChan, goChan, nil)
	defer func() { _ = backend.Connector().Close() }()

	assert.NotNil(t, backend.Publisher())
	assert.NotNil(t, backend.Consumer())
	assert.True(t, backend.Connector().Healthy())

	// Subscribe to GoChannel BEFORE publishing.
	msgs, err := goChan.Subscribe(context.Background(), "events")
	require.NoError(t, err)

	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "user.created",
		Payload: json.RawMessage(`{"user_id":"u1"}`),
	}

	err = backend.Publisher().Publish(context.Background(), "events", "user.created", msg)
	require.NoError(t, err)

	wmMsg := <-msgs
	assert.Equal(t, "msg-1", wmMsg.UUID)
	assert.Equal(t, "user.created", wmMsg.Metadata.Get(wmconvert.MetaMessageType))
	assert.Equal(t, "events", wmMsg.Metadata.Get(wmconvert.MetaExchange))
	wmMsg.Ack()
}

func TestBackend_NilPublisher(t *testing.T) {
	goChan := gochannel.NewGoChannel(gochannel.Config{}, watermill.NopLogger{})
	backend := wmconvert.NewBackend(nil, goChan, nil)
	defer func() { _ = backend.Connector().Close() }()

	assert.Nil(t, backend.Publisher())
	assert.NotNil(t, backend.Consumer())
}

func TestBackend_NilSubscriber(t *testing.T) {
	goChan := gochannel.NewGoChannel(gochannel.Config{}, watermill.NopLogger{})
	backend := wmconvert.NewBackend(goChan, nil, nil)
	defer func() { _ = backend.Connector().Close() }()

	assert.NotNil(t, backend.Publisher())
	assert.Nil(t, backend.Consumer())
}

func TestBackend_HealthFunc(t *testing.T) {
	healthy := true
	backend := wmconvert.NewBackend(nil, nil, nil,
		wmconvert.WithHealthFunc(func() bool { return healthy }),
	)

	assert.True(t, backend.Connector().Healthy())
	healthy = false
	assert.False(t, backend.Connector().Healthy())
}
