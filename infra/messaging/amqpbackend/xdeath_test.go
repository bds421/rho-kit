package amqpbackend

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func TestXDeathCount_NilHeaders(t *testing.T) {
	assert.Equal(t, 0, xDeathCount(nil, "q"))
}

func TestXDeathCount_MissingHeader(t *testing.T) {
	headers := amqp.Table{"other": "value"}
	assert.Equal(t, 0, xDeathCount(headers, "q"))
}

func TestXDeathCount_WrongType(t *testing.T) {
	headers := amqp.Table{"x-death": "not-a-slice"}
	assert.Equal(t, 0, xDeathCount(headers, "q"))
}

func TestXDeathCount_MatchesQueueAndReason(t *testing.T) {
	headers := amqp.Table{
		"x-death": []any{
			amqp.Table{
				"queue":  "other.queue",
				"reason": "rejected",
				"count":  int64(3),
			},
			amqp.Table{
				"queue":  "my.queue",
				"reason": "expired",
				"count":  int64(5),
			},
			amqp.Table{
				"queue":  "my.queue",
				"reason": "rejected",
				"count":  int64(2),
			},
		},
	}

	assert.Equal(t, 2, xDeathCount(headers, "my.queue"))
}

func TestXDeathCount_NoMatchingQueue(t *testing.T) {
	headers := amqp.Table{
		"x-death": []any{
			amqp.Table{
				"queue":  "other.queue",
				"reason": "rejected",
				"count":  int64(1),
			},
		},
	}

	assert.Equal(t, 0, xDeathCount(headers, "my.queue"))
}

func TestXDeathCount_IntVariants(t *testing.T) {
	tests := []struct {
		name  string
		count any
		want  int
	}{
		{"int", int(4), 4},
		{"int32", int32(4), 4},
		{"int64", int64(4), 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := amqp.Table{
				"x-death": []any{
					amqp.Table{
						"queue":  "q",
						"reason": "rejected",
						"count":  tt.count,
					},
				},
			}
			assert.Equal(t, tt.want, xDeathCount(headers, "q"))
		})
	}
}

func TestXDeathCount_MalformedEntry(t *testing.T) {
	headers := amqp.Table{
		"x-death": []any{
			"not-a-table",
			amqp.Table{
				"queue":  "q",
				"reason": "rejected",
				"count":  int64(1),
			},
		},
	}

	assert.Equal(t, 1, xDeathCount(headers, "q"))
}
