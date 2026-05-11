package temporal_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/bds421/rho-kit/runtime/temporal/v2"
)

func TestConfig_ClientOptionsCarriesFields(t *testing.T) {
	cfg := temporal.Config{
		HostPort:  "temporal:7233",
		Namespace: "default",
		Identity:  "svc-1",
	}
	opts := cfg.ClientOptions(nil)
	assert.Equal(t, "temporal:7233", opts.HostPort)
	assert.Equal(t, "default", opts.Namespace)
	assert.Equal(t, "svc-1", opts.Identity)
	assert.NotNil(t, opts.Logger, "ClientOptions must always supply a logger")
}

func TestConnect_RejectsEmptyHostPort(t *testing.T) {
	_, err := temporal.Connect(context.Background(), client.Options{Namespace: "default"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HostPort")
}

func TestConnect_RejectsNilContext(t *testing.T) {
	var ctx context.Context
	_, err := temporal.Connect(ctx, client.Options{
		HostPort:  "temporal:7233",
		Namespace: "default",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestConnect_RejectsEmptyNamespace(t *testing.T) {
	_, err := temporal.Connect(context.Background(), client.Options{HostPort: "x:1234"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Namespace")
}

func TestConnect_DialErrorDoesNotReflectHostPort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	_, err := temporal.Connect(ctx, client.Options{
		HostPort:  "secret-token.invalid:7233",
		Namespace: "default",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial failed")
	assert.False(t, strings.Contains(err.Error(), "secret-token"), "dial error leaked HostPort: %v", err)
}

func TestNewWorker_PanicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil client")
		}
	}()
	_ = temporal.NewWorker(nil, "tq", worker.Options{})
}

// Empty-task-queue rejection is enforced inside NewWorker but
// requires a non-nil *Client to reach. Building a non-nil Client
// requires a live Temporal connection, so this precondition is
// covered by the integration suite.
