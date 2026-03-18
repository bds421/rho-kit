package amqpbackend

import amqp "github.com/rabbitmq/amqp091-go"

// xDeathCount extracts the rejection count from the x-death header for the
// given queue. RabbitMQ populates x-death as a []any of amqp.Table entries,
// with each entry tracking a dead-letter reason and count per queue.
//
// Only counts entries with reason "rejected" — this assumes the retry queue
// has a different name than the main queue (e.g., "foo.retry" vs "foo"),
// so "expired" deaths from the retry queue don't match the queue parameter.
func xDeathCount(headers amqp.Table, queue string) int {
	if headers == nil {
		return 0
	}

	xDeath, ok := headers["x-death"]
	if !ok {
		return 0
	}

	deaths, ok := xDeath.([]any)
	if !ok {
		return 0
	}

	for _, entry := range deaths {
		table, ok := entry.(amqp.Table)
		if !ok {
			continue
		}

		q, _ := table["queue"].(string)
		reason, _ := table["reason"].(string)

		if q == queue && reason == "rejected" {
			switch v := table["count"].(type) {
			case int:
				return v
			case int32:
				return int(v)
			case int64:
				return int(v)
			}
		}
	}

	return 0
}
