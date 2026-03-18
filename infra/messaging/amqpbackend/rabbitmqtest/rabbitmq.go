//go:build integration

package rabbitmqtest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

// defaultImage is the RabbitMQ Docker image used by Start.
// Override via RABBITMQ_TEST_IMAGE env var for testing against different versions.
const defaultImage = "rabbitmq:4.2.3-management"

var (
	once      sync.Once
	sharedURL string
	initErr   error
)

// Start returns the AMQP URL of a shared RabbitMQ container. The container
// is created on the first call and reused for all subsequent calls within
// the same test process. Testcontainers' Ryuk sidecar automatically
// terminates the container when the process exits.
//
// Since the container is shared, broker state (exchanges, queues, messages)
// leaks between tests. Tests MUST use unique exchange/queue names to avoid
// interference — e.g. include t.Name() in the exchange name.
func Start(t *testing.T) string {
	t.Helper()

	once.Do(func() {
		ctx := context.Background()

		image := defaultImage
		if envImage := os.Getenv("RABBITMQ_TEST_IMAGE"); envImage != "" {
			image = envImage
		}
		container, err := rabbitmq.Run(ctx, image)
		if err != nil {
			initErr = fmt.Errorf("start rabbitmq container: %w", err)
			return
		}

		url, err := container.AmqpURL(ctx)
		if err != nil {
			initErr = fmt.Errorf("get rabbitmq amqp url: %w", err)
			return
		}

		sharedURL = url
	})

	if initErr != nil {
		t.Fatalf("rabbitmq setup: %v", initErr)
	}

	return sharedURL
}
