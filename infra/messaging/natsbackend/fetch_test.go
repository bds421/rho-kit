package natsbackend

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
)

func TestPullRequest_Validate(t *testing.T) {
	ok := PullRequest{MaxMessages: 10, MaxBytes: 1 << 20, Expires: 5 * time.Second, Heartbeat: time.Second}
	assert.NoError(t, ok.Validate())
	for name, r := range map[string]PullRequest{
		"no messages":        {Expires: time.Second},
		"too many":           {MaxMessages: MaxPullMessages + 1, Expires: time.Second},
		"negative bytes":     {MaxMessages: 1, MaxBytes: -1, Expires: time.Second},
		"no expiry":          {MaxMessages: 1},
		"heartbeat too long": {MaxMessages: 1, Expires: time.Second, Heartbeat: 600 * time.Millisecond},
		"negative heartbeat": {MaxMessages: 1, Expires: time.Second, Heartbeat: -1},
	} {
		assert.ErrorIs(t, r.Validate(), ErrPullRequest, name)
	}
}

func TestStatusEnds(t *testing.T) {
	cases := []struct {
		status, description string
		received            int
		done                bool
		err                 error
	}{
		{"100", "Idle Heartbeat", 0, false, nil},
		{"404", "No Messages", 0, true, nil},
		{"408", "Request Timeout", 3, true, nil},
		{"503", "", 0, true, nats.ErrNoResponders},
		{"409", "Message Size Exceeds MaxBytes", 0, true, ErrMessageTooLarge},
		{"409", "Message Size Exceeds MaxBytes", 2, true, nil},
		{"409", "Consumer Deleted", 1, true, jetstream.ErrConsumerDeleted},
		{"409", "Batch Completed", 1, true, nil},
	}
	for _, c := range cases {
		done, err := statusEnds(c.status, c.description, c.received)
		assert.Equal(t, c.done, done, c.status+" "+c.description)
		assert.ErrorIs(t, err, c.err, c.status+" "+c.description)
		if c.err == nil {
			assert.NoError(t, err, c.status+" "+c.description)
		}
	}
	done, err := statusEnds("409", "Exceeded MaxWaiting", 0)
	assert.True(t, done)
	assert.ErrorContains(t, err, "Exceeded MaxWaiting")
}

func TestAPIPrefix(t *testing.T) {
	assert.Equal(t, "$JS.API.", apiPrefix(jetstream.JetStreamOptions{}))
	assert.Equal(t, "$JS.hub.API.", apiPrefix(jetstream.JetStreamOptions{Domain: "hub"}))
	assert.Equal(t, "tenant.API.", apiPrefix(jetstream.JetStreamOptions{APIPrefix: "tenant.API"}))
}

func TestNameToken(t *testing.T) {
	assert.True(t, nameToken("EVENTS"))
	for _, name := range []string{"", "a.b", "a*", ">", "a b"} {
		assert.False(t, nameToken(name), name)
	}
}
